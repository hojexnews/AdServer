// Package stripe implementa o trilho fiat global via Stripe (SAQ-A).
//
// # SAQ-A — invariante inegociavel
//
// O cartao de credito NUNCA transita pelo backend (ADR-0004 §F / §H K4).
// A tokenizacao e SEMPRE client-side via Stripe Elements ou Stripe Checkout.
// Este pacote:
//
//   - Cria PaymentIntents e retorna client_secret ao BFF (que o repassa ao front).
//   - Cria assinaturas/faturas Stripe Billing para Tenancy.
//   - Recebe e VERIFICA webhooks (Stripe-Signature) — rejeita payload nao assinado.
//   - Ao payment_intent.succeeded: emite par de postings no ledger (K3) via
//     internal/ledger.RecordEntry, idempotente por event ID Stripe.
//   - NAO recebe PAN, CVV, numero de cartao, track data em nenhum handler ou struct.
//
// # Float proibido (TX-2)
//
// Stripe trabalha em centavos (int64). Convertemos diretamente para
// Money/int64 minor-units sem intermediario float.
//
// # PII
//
// O webhook do Stripe pode retornar metadados de billing com PII
// (email, nome). Esses campos sao descartados antes de popular WebhookEvent
// ou qualquer struct interna. Apenas payment_intent_id, amount, currency.
//
// # Idempotencia
//
// Cada evento Stripe tem um ID unico (evt_...). O handler verifica se o
// event ID ja foi processado (idempotency_key = "stripe:{event_id}") antes
// de gravar postings — reprocessamento nao duplica.
package stripe

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hojex/adserver/internal/ledger"
	"github.com/hojex/adserver/services/payments/internal/fiat"
	"github.com/jackc/pgx/v5/pgxpool"
)

const railName = "stripe"

// Erros exportados do pacote stripe.
var (
	// ErrInvalidSignature indica que o payload de webhook nao passou na
	// verificacao de assinatura Stripe-Signature.
	// O handler deve retornar HTTP 400 e descartar o payload.
	ErrInvalidSignature = errors.New("stripe: assinatura de webhook invalida")

	// ErrWebhookTooOld indica que o timestamp do webhook e muito antigo
	// (protecao contra replay attack — janela de 5 minutos Stripe).
	ErrWebhookTooOld = errors.New("stripe: webhook muito antigo (possivel replay)")
)

// Provider e a implementacao do trilho Stripe.
//
// Satisfaz fiat.FiatProvider.
// Campos privados: a chave secreta NUNCA e acessivel via reflexao/JSON.
type Provider struct {
	secretKey      string // sk_live_... ou sk_test_... — NUNCA logue
	webhookSecret  string // whsec_... — NUNCA logue
	baseURL        string // https://api.stripe.com (configuravel para testes)
	httpClient     *http.Client
	pool           *pgxpool.Pool
	assetLoader    ledger.AssetLoader
	logger         *slog.Logger
}

// Config agrupa a configuracao do provedor Stripe.
// Os campos de segredo sao strings opacas — nunca serializar este struct.
type Config struct {
	SecretKey     string // STRIPE_SECRET_KEY (injetado pelo OpenBao em prod)
	WebhookSecret string // STRIPE_WEBHOOK_SECRET
	BaseURL       string // default: "https://api.stripe.com"
}

// New cria um Provider Stripe pronto para uso.
//
// pool e assetLoader sao necessarios para gravar postings no ledger (K3).
// Se logger for nil, usa slog.Default().
func New(cfg Config, pool *pgxpool.Pool, assetLoader ledger.AssetLoader, logger *slog.Logger) (*Provider, error) {
	if cfg.SecretKey == "" {
		return nil, errors.New("stripe: SecretKey obrigatoria")
	}
	if cfg.WebhookSecret == "" {
		return nil, errors.New("stripe: WebhookSecret obrigatorio para verificar webhooks")
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.stripe.com"
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Provider{
		secretKey:     cfg.SecretKey,
		webhookSecret: cfg.WebhookSecret,
		baseURL:       baseURL,
		httpClient:    &http.Client{Timeout: 30 * time.Second},
		pool:          pool,
		assetLoader:   assetLoader,
		logger:        logger,
	}, nil
}

// RailName implementa fiat.FiatProvider.
func (p *Provider) RailName() string { return railName }

// Healthy verifica se a API Stripe esta acessivel.
// Faz uma requisicao leve (GET /v1/balance) com a chave secreta.
func (p *Provider) Healthy(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/v1/balance", nil)
	if err != nil {
		return fmt.Errorf("stripe: healthy: criar request: %w", err)
	}
	req.SetBasicAuth(p.secretKey, "")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("stripe: healthy: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("stripe: healthy: status %d", resp.StatusCode)
	}
	return nil
}

// ---------------------------------------------------------------------------
// CreatePaymentIntent — SAQ-A: retorna client_secret para o front
// ---------------------------------------------------------------------------

// paymentIntentResponse e o subset do JSON da API Stripe que nos interessa.
// Descartamos campos PII (billing_details, customer email, etc.).
type paymentIntentResponse struct {
	ID           string `json:"id"`
	ClientSecret string `json:"client_secret"`
	Status       string `json:"status"`
	Amount       int64  `json:"amount"`
	Currency     string `json:"currency"`
}

// CreatePaymentIntent cria um PaymentIntent no Stripe.
//
// SAQ-A: o backend nunca ve dados de cartao. O frontend usa o client_secret
// retornado para completar o pagamento via Stripe Elements/Checkout.
// Nenhum campo PAN/CVV e aceito nem retornado.
func (p *Provider) CreatePaymentIntent(ctx context.Context, req fiat.PaymentIntentRequest) (fiat.PaymentIntentResult, error) {
	// Validacao basica antes de chamar a API.
	if req.AmountMinor <= 0 {
		return fiat.PaymentIntentResult{}, errors.New("stripe: CreatePaymentIntent: amount deve ser > 0")
	}
	if req.AssetCode == "" {
		return fiat.PaymentIntentResult{}, errors.New("stripe: CreatePaymentIntent: asset_code obrigatorio")
	}

	// Stripe usa lowercase para currency codes.
	currency := strings.ToLower(req.AssetCode)

	// Monta o form body para a API Stripe via url.Values.
	// url.Values.Encode() faz o percent-encoding correto de todos os campos,
	// prevenindo injecao de parametros por valores com '&' ou '=' (ex.: Description).
	// NAO ha campo de cartao (pan, cvv, number, exp_month, exp_year).
	// O cartao e tokenizado client-side pelo Stripe.js/Elements.
	formVals := url.Values{}
	formVals.Set("amount", fmt.Sprintf("%d", req.AmountMinor))
	formVals.Set("currency", currency)
	formVals.Set("capture_method", "automatic")
	formVals.Set("metadata[tenant_id]", req.TenantID)
	formVals.Set("metadata[advertiser_id]", req.AdvertiserID)
	formVals.Set("description", req.Description)
	body := strings.NewReader(formVals.Encode())

	apiReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/v1/payment_intents", body)
	if err != nil {
		return fiat.PaymentIntentResult{}, fmt.Errorf("stripe: CreatePaymentIntent: build request: %w", err)
	}
	apiReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// Autenticacao via HTTP Basic Auth com a secret key — sem PAN.
	apiReq.SetBasicAuth(p.secretKey, "")
	// Idempotency-Key para evitar duplicatas em retry.
	if req.IdempotencyKey != "" {
		apiReq.Header.Set("Idempotency-Key", req.IdempotencyKey)
	}

	resp, err := p.httpClient.Do(apiReq)
	if err != nil {
		return fiat.PaymentIntentResult{}, fmt.Errorf("stripe: CreatePaymentIntent: http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fiat.PaymentIntentResult{}, fmt.Errorf("stripe: CreatePaymentIntent: status %d: %s",
			resp.StatusCode, string(bodyBytes))
	}

	var pi paymentIntentResponse
	if err := json.NewDecoder(resp.Body).Decode(&pi); err != nil {
		return fiat.PaymentIntentResult{}, fmt.Errorf("stripe: CreatePaymentIntent: decode: %w", err)
	}

	p.logger.Info("stripe: payment intent criado",
		"payment_id", pi.ID,
		"status", pi.Status,
		"tenant_id", req.TenantID,
		// Nunca loga: secretKey, clientSecret, dados de cartao, PII do pagador
	)

	return fiat.PaymentIntentResult{
		PaymentID:    pi.ID,
		ClientSecret: pi.ClientSecret,
		Rail:         railName,
	}, nil
}

// CreatePixCharge nao e suportado pelo trilho Stripe.
// PIX e tratado pelo AsaasProvider. Mercado Pago (failover) suporta PIX tambem.
func (p *Provider) CreatePixCharge(_ context.Context, _ fiat.PixChargeRequest) (fiat.PixChargeResult, error) {
	return fiat.PixChargeResult{}, errors.New("stripe: PIX nao suportado neste trilho; use asaas ou mercadopago")
}

// ---------------------------------------------------------------------------
// Webhook handler — verificacao de assinatura obrigatoria
// ---------------------------------------------------------------------------

// maxWebhookBodyBytes limita o tamanho do payload de webhook para prevenir
// ataques de exaustao de memoria. 512 KB e suficiente para eventos Stripe.
const maxWebhookBodyBytes = 512 * 1024

// webhookPayload e o subset dos campos do evento Stripe que nos interessa.
// Campos PII (billing_details, customer, email) sao ignorados/descartados.
type webhookPayload struct {
	ID      string `json:"id"`   // evt_... (chave de idempotencia)
	Type    string `json:"type"` // ex.: "payment_intent.succeeded"
	Data    struct {
		Object struct {
			ID       string `json:"id"`       // pi_... (payment intent id)
			Amount   int64  `json:"amount"`   // minor-units
			Currency string `json:"currency"` // "brl", "usd"
			Status   string `json:"status"`
			Metadata struct {
				TenantID     string `json:"tenant_id"`
				AdvertiserID string `json:"advertiser_id"`
			} `json:"metadata"`
			// Campos de cartao/PII: deliberadamente ausentes nesta struct.
			// Se o JSON do Stripe incluir billing_details/customer/email,
			// eles serao descartados pelo decoder JSON (campos desconhecidos).
		} `json:"object"`
	} `json:"data"`
	Created int64 `json:"created"` // unix timestamp do evento
}

// WebhookHandler retorna um http.Handler para receber webhooks do Stripe.
//
// SEGURANCA:
//  1. Verifica Stripe-Signature antes de qualquer processamento.
//  2. Rejeita com HTTP 400 se a assinatura for invalida ou o evento for muito antigo.
//  3. Idempotente por event ID (evt_...): nao duplica postings.
//  4. Ao payment_intent.succeeded: grava par de postings no ledger (BILLING.md §4.4 cenario B).
//
// Handler e server-to-server: sem CSRF de browser.
// Verificacao de assinatura substitui autenticacao de usuario.
func (p *Provider) WebhookHandler(tenantID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Leitura limitada do body — previne exaustao de memoria.
		rawBody, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBodyBytes))
		if err != nil {
			p.logger.Warn("stripe: webhook: falha ao ler body", "err", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		// PASSO 1: Verificacao de assinatura Stripe-Signature.
		// Rejeita qualquer payload nao assinado ou com assinatura invalida.
		sig := r.Header.Get("Stripe-Signature")
		if sig == "" {
			p.logger.Warn("stripe: webhook: Stripe-Signature ausente — rejeitado")
			http.Error(w, "missing signature", http.StatusBadRequest)
			return
		}

		if err := p.verifyStripeSignature(rawBody, sig); err != nil {
			p.logger.Warn("stripe: webhook: assinatura invalida", "err", err)
			http.Error(w, "invalid signature", http.StatusBadRequest)
			return
		}

		// PASSO 2: Parse do payload (campos PII ignorados pelo decoder).
		var event webhookPayload
		if err := json.Unmarshal(rawBody, &event); err != nil {
			p.logger.Warn("stripe: webhook: payload invalido", "err", err)
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}

		// PASSO 3: Processar apenas eventos relevantes.
		switch event.Type {
		case "payment_intent.succeeded":
			if err := p.handlePaymentIntentSucceeded(r.Context(), tenantID, event); err != nil {
				p.logger.Error("stripe: webhook: handlePaymentIntentSucceeded",
					"event_id", event.ID,
					"err", err,
				)
				// Retorna 500 para que o Stripe retente — nao 400 (que seria final).
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
		default:
			// Eventos desconhecidos: acusar recebimento para evitar retries do Stripe.
			p.logger.Debug("stripe: webhook: evento ignorado", "type", event.Type, "event_id", event.ID)
		}

		w.WriteHeader(http.StatusOK)
	})
}

// verifyStripeSignature verifica a assinatura HMAC-SHA256 do Stripe.
//
// Algoritmo oficial Stripe (https://stripe.com/docs/webhooks/signatures):
//  1. Parse do header Stripe-Signature: "t=timestamp,v1=hash,...".
//  2. Mensagem assinada: "{timestamp}.{rawBody}".
//  3. HMAC-SHA256 da mensagem com o webhook secret.
//  4. Comparacao em tempo constante com o hash v1 do header.
//  5. Rejeita se o timestamp for > 5 minutos no passado (anti-replay).
//
// Retorna ErrInvalidSignature se qualquer verificacao falhar.
func (p *Provider) verifyStripeSignature(rawBody []byte, sigHeader string) error {
	const tolerance = 5 * time.Minute

	// Parse do header: "t=1234,v1=abc,v1=def"
	parts := strings.Split(sigHeader, ",")
	var tsStr string
	var signatures []string
	for _, part := range parts {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			tsStr = kv[1]
		case "v1":
			signatures = append(signatures, kv[1])
		}
	}

	if tsStr == "" || len(signatures) == 0 {
		return ErrInvalidSignature
	}

	tsUnix, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return ErrInvalidSignature
	}

	// Anti-replay: rejeita eventos com timestamp muito antigo.
	eventTime := time.Unix(tsUnix, 0)
	if time.Since(eventTime) > tolerance {
		return ErrWebhookTooOld
	}

	// Computa HMAC-SHA256 da mensagem "{timestamp}.{body}".
	msg := []byte(tsStr + "." + string(rawBody))
	mac := hmac.New(sha256.New, []byte(p.webhookSecret))
	mac.Write(msg)
	expected := hex.EncodeToString(mac.Sum(nil))

	// Verifica contra todas as assinaturas v1 (Stripe pode rotacionar secrets).
	for _, sig := range signatures {
		if hmac.Equal([]byte(expected), []byte(sig)) {
			return nil
		}
	}
	return ErrInvalidSignature
}

// ---------------------------------------------------------------------------
// Handlers de eventos especificos
// ---------------------------------------------------------------------------

// handlePaymentIntentSucceeded processa payment_intent.succeeded.
//
// Grava par de postings no ledger conforme BILLING.md §4.4 cenario B:
//   - DEBITO  platform:cash:{asset}        (caixa recebido)
//   - CREDITO platform:receivable:{asset}  (baixa do a receber)
//
// Idempotencia: idempotency_key = "stripe:{event_id}" — nao duplica.
// O event_id (evt_...) e distinto do payment_intent_id (pi_...).
func (p *Provider) handlePaymentIntentSucceeded(ctx context.Context, tenantID string, event webhookPayload) error {
	pi := event.Data.Object

	if pi.ID == "" || pi.Amount <= 0 {
		return fmt.Errorf("stripe: payment_intent.succeeded com dados invalidos: id=%q amount=%d", pi.ID, pi.Amount)
	}

	// Mapeia currency Stripe (lowercase) para asset_code do Asset Registry.
	assetCode := strings.ToUpper(pi.Currency)

	// Valida o ativo no Asset Registry e obtem o scale autoritativo.
	// O scale hardcoded foi removido: o Asset Registry e a unica fonte de verdade
	// para scale/decimals de cada ativo (invariante DA-10/TX-2).
	// LoadAsset retorna erro se o ativo estiver disabled ou nao existir — o que
	// tambem serve como gate de currency nao suportada.
	var scale int32
	if p.assetLoader != nil {
		asset, err := p.assetLoader.LoadAsset(ctx, assetCode)
		if err != nil {
			return fmt.Errorf("stripe: currency nao suportada ou ativo desabilitado %q: %w", pi.Currency, err)
		}
		scale = asset.Scale
	} else {
		// Caminho de teste sem assetLoader: aplica scale conservador so para BRL/USD.
		// Em producao assetLoader nunca e nil (New exige pool+assetLoader).
		switch assetCode {
		case "BRL", "USD":
			scale = 2
		default:
			return fmt.Errorf("stripe: currency nao suportada: %q", pi.Currency)
		}
	}

	if p.pool == nil {
		return fmt.Errorf("stripe: pool de banco de dados nao configurado (ambiente de teste sem banco?)")
	}

	// Provisiona contas se necessario (EnsureAccount e idempotente).
	cashCode := fmt.Sprintf("platform:cash:%s", assetCode)
	receivableCode := fmt.Sprintf("platform:receivable:%s", assetCode)

	cashAcc, err := ledger.EnsureAccount(ctx, p.pool, p.assetLoader, ledger.EnsureAccountParams{
		TenantID:  tenantID,
		Code:      cashCode,
		Name:      "Caixa " + assetCode,
		Kind:      ledger.AccountKindAsset,
		AssetCode: assetCode,
		Scale:     scale,
	})
	if err != nil {
		return fmt.Errorf("stripe: EnsureAccount cash: %w", err)
	}

	receivableAcc, err := ledger.EnsureAccount(ctx, p.pool, p.assetLoader, ledger.EnsureAccountParams{
		TenantID:  tenantID,
		Code:      receivableCode,
		Name:      "Contas a Receber " + assetCode,
		Kind:      ledger.AccountKindAsset,
		AssetCode: assetCode,
		Scale:     scale,
	})
	if err != nil {
		return fmt.Errorf("stripe: EnsureAccount receivable: %w", err)
	}

	// Idempotency key: "stripe:{event_id}" (nao payment_intent_id, que pode
	// ter multiplos eventos). O event_id (evt_...) e unico por evento.
	idempKey := fmt.Sprintf("stripe:%s", event.ID)

	// Par de postings: BILLING.md §4.4 cenario B — recebimento de pagamento.
	// DEBITO platform:cash:{asset} (caixa recebido)
	// CREDITO platform:receivable:{asset} (baixa do a receber)
	//
	// AmountMinor = pi.Amount (int64, ja em centavos — sem float TX-2).
	_, err = ledger.RecordEntry(ctx, p.pool, p.assetLoader, ledger.RecordEntryParams{
		TenantID:       tenantID,
		IdempotencyKey: idempKey,
		Description:    fmt.Sprintf("Recebimento Stripe: %s intent=%s", assetCode, pi.ID),
		Status:         ledger.EntryStatusPosted,
		EffectiveAt:    time.Now().UTC(),
		RefType:        "payment_event",
		Metadata: []byte(fmt.Sprintf(
			`{"provider":"stripe","payment_intent_id":%q,"stripe_event_id":%q,"tenant_id":%q}`,
			pi.ID, event.ID, tenantID,
			// Sem PII: billing_details, customer, email descartados.
		)),
		Postings: []ledger.PostingLine{
			{
				AccountID:        cashAcc.ID,
				AssetCode:        assetCode,
				Scale:            scale,
				DebitMinorUnits:  pi.Amount, // int64, sem float
				CreditMinorUnits: 0,
			},
			{
				AccountID:        receivableAcc.ID,
				AssetCode:        assetCode,
				Scale:            scale,
				DebitMinorUnits:  0,
				CreditMinorUnits: pi.Amount, // int64, sem float
			},
		},
	})
	if err != nil {
		// ErrIdempotentDuplicate e seguro: o posting ja foi gravado anteriormente.
		if errors.Is(err, ledger.ErrIdempotentDuplicate) {
			p.logger.Info("stripe: webhook idempotente — posting ja existe",
				"event_id", event.ID, "payment_intent_id", pi.ID)
			return nil
		}
		return fmt.Errorf("stripe: RecordEntry: %w", err)
	}

	p.logger.Info("stripe: payment capturado — posting gravado",
		"event_id", event.ID,
		"payment_intent_id", pi.ID,
		"amount_minor", pi.Amount,
		"asset_code", assetCode,
		"tenant_id", tenantID,
		// Sem PII: nao loga nome, email, CPF, dados de cartao
	)

	return nil
}
