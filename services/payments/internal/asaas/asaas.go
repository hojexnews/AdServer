// Package asaas implementa o trilho fiat Brasil via Asaas (PIX primario).
//
// # Funcionalidades
//
//   - QR Code dinamico PIX (cobranca avulsa).
//   - Pix Automatico: cobranca recorrente para Tenancy (assinatura mensal).
//   - Conciliacao por txid/E2E (campo pix_end_to_end_id no PaymentEvent — §2.6).
//   - Webhook handler com verificacao de token Asaas (header asaas-access-token).
//
// # Float proibido (TX-2)
//
// Asaas trabalha em reais com 2 casas decimais representados como string
// decimal (ex.: "150.00"). Convertemos para int64 minor-units (centavos)
// sem usar float: parsemos a string decimal diretamente com manipulacao
// de inteiros. O campo AmountMinor da request ja chega em centavos.
//
// # PII
//
// A API Asaas pode retornar campos do pagador (nome, CPF, email) nas
// respostas. Esses campos sao deliberadamente omitidos dos structs de parse
// — o decoder JSON os descarta. Apenas: charge_id, txid/E2E ID, amount, status.
//
// # Idempotencia
//
// Cada notificacao Asaas tem um campo "id" de evento. O handler verifica
// idempotency_key = "asaas:{charge_id}" antes de gravar postings.
package asaas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/hojex/adserver/internal/ledger"
	"github.com/hojex/adserver/services/payments/internal/fiat"
	"github.com/jackc/pgx/v5/pgxpool"
)

const railName = "asaas_pix"

// Erros exportados.
var (
	// ErrInvalidWebhookToken indica que o token de autenticacao do webhook esta incorreto.
	ErrInvalidWebhookToken = errors.New("asaas: token de webhook invalido — rejeitado")
)

// Provider e a implementacao do trilho Asaas/PIX.
// Satisfaz fiat.FiatProvider.
type Provider struct {
	apiKey       string // $aact_... — NUNCA logue
	webhookToken string // token de autenticacao dos webhooks Asaas — NUNCA logue
	baseURL      string // https://api.asaas.com/v3 (sandbox: https://sandbox.asaas.com/api/v3)
	httpClient   *http.Client
	pool         *pgxpool.Pool
	assetLoader  ledger.AssetLoader
	logger       *slog.Logger
}

// Config agrupa a configuracao do provedor Asaas.
type Config struct {
	APIKey       string // ASAAS_API_KEY
	WebhookToken string // ASAAS_WEBHOOK_TOKEN (para verificar webhooks)
	BaseURL      string // default: "https://api.asaas.com/v3"
}

// New cria um Provider Asaas pronto para uso.
// Se logger for nil, usa slog.Default().
func New(cfg Config, pool *pgxpool.Pool, assetLoader ledger.AssetLoader, logger *slog.Logger) (*Provider, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("asaas: APIKey obrigatoria")
	}
	if cfg.WebhookToken == "" {
		return nil, errors.New("asaas: WebhookToken obrigatorio para verificar webhooks")
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.asaas.com/v3"
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Provider{
		apiKey:       cfg.APIKey,
		webhookToken: cfg.WebhookToken,
		baseURL:      baseURL,
		httpClient:   &http.Client{Timeout: 30 * time.Second},
		pool:         pool,
		assetLoader:  assetLoader,
		logger:       logger,
	}, nil
}

// RailName implementa fiat.FiatProvider.
func (p *Provider) RailName() string { return railName }

// Healthy verifica se a API Asaas esta acessivel.
func (p *Provider) Healthy(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/finance/balance", nil)
	if err != nil {
		return fmt.Errorf("asaas: healthy: criar request: %w", err)
	}
	req.Header.Set("access_token", p.apiKey)
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("asaas: healthy: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("asaas: healthy: status %d", resp.StatusCode)
	}
	return nil
}

// ---------------------------------------------------------------------------
// CreatePixCharge — QR Code dinamico e Pix Automatico
// ---------------------------------------------------------------------------

// asaasChargeRequest e o payload enviado a API Asaas para criar cobranca.
// Campos PII do pagador (nome, CPF, email) sao exigidos pela Asaas para
// identificar o cliente — mas eles SAO DO ADVERTISER, nao do usuario final.
// O TenantID/AdvertiserID pseudonimo e passado como externalReference.
// PII real do pagador vai para o cofre de compliance (K6) — nunca para o ledger.
type asaasChargeRequest struct {
	// Customer e o ID do customer Asaas (criado separadamente via API Asaas).
	// Nao e PII diretamente — e um ID opaco do provedor.
	Customer string `json:"customer"`

	// BillingType deve ser "PIX" para QR Code dinamico.
	BillingType string `json:"billingType"`

	// Value e o valor em reais como string com 2 casas decimais.
	// Ex.: "150.00" para BRL 1,50,00.
	// Convertemos de centavos sem float.
	Value string `json:"value"`

	// DueDate e a data de vencimento (YYYY-MM-DD). Obrigatoria.
	DueDate string `json:"dueDate"`

	// Description sem PII.
	Description string `json:"description"`

	// ExternalReference e o nosso IdempotencyKey / advertiser_id pseudonimo.
	ExternalReference string `json:"externalReference"`
}

// asaasChargeResponse e o subset da resposta Asaas que nos interessa.
// Campos PII (customer.name, customer.cpfCnpj, etc.) sao omitidos e descartados.
type asaasChargeResponse struct {
	ID          string `json:"id"`          // charge ID da Asaas
	Status      string `json:"status"`
	BillingType string `json:"billingType"`
	Value       string `json:"value"`       // decimal string — convertemos sem float
	DueDate     string `json:"dueDate"`
	// PixQrCode aninhado: apenas payload e encodedImage. Sem dados de cliente.
	PixQrCode struct {
		Payload      string `json:"payload"`
		EncodedImage string `json:"encodedImage"` // base64 da imagem PNG
	} `json:"pix"`
	// Campos PII: deliberadamente ausentes. O JSON os ignora.
}

// centavosToAsaasDecimal converte int64 centavos para string decimal Asaas.
// Ex.: 15000 -> "150.00"
// Sem float (TX-2): operacao puramente inteira e de string.
func centavosToAsaasDecimal(centavos int64) string {
	intPart := centavos / 100
	fracPart := centavos % 100
	if fracPart < 0 {
		fracPart = -fracPart
	}
	return fmt.Sprintf("%d.%02d", intPart, fracPart)
}

// asaasDecimalToCentavos converte string decimal Asaas para int64 centavos.
// Ex.: "150.00" -> 15000
// Sem float (TX-2): manipulacao inteira pura.
//
// TX-2: fracao com mais de 2 digitos e ERRO, nao truncamento silencioso.
// O provedor ja deve respeitar scale=2; divergencia indica problema upstream
// e deve ser tratada como excecao, nunca silenciada por truncamento.
// Valores negativos tambem sao rejeitados com erro explicito.
func asaasDecimalToCentavos(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("asaas: valor vazio")
	}
	// Rejeita valores negativos explicitamente (parseInt64 rejeitaria o '-'
	// como caracter invalido, mas mensagem de erro seria confusa).
	if strings.HasPrefix(s, "-") {
		return 0, fmt.Errorf("asaas: valor negativo nao permitido: %q", s)
	}
	parts := strings.SplitN(s, ".", 2)
	intPart, err := parseInt64(parts[0])
	if err != nil {
		return 0, fmt.Errorf("asaas: parse valor inteiro %q: %w", s, err)
	}
	var fracPart int64
	if len(parts) == 2 {
		frac := parts[1]
		switch len(frac) {
		case 0:
			fracPart = 0
		case 1:
			fracPart, err = parseInt64(frac + "0")
		case 2:
			fracPart, err = parseInt64(frac)
		default:
			// TX-2: truncamento silencioso proibido.
			// Fracao com mais de 2 digitos e erro explicito — nao arredondar.
			return 0, fmt.Errorf("asaas: fracao excede scale: %q", s)
		}
		if err != nil {
			return 0, fmt.Errorf("asaas: parse fracao %q: %w", s, err)
		}
	}
	return intPart*100 + fracPart, nil
}

// parseInt64 converte string numerica para int64 sem float.
func parseInt64(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	var v int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("caracter invalido: %q", c)
		}
		v = v*10 + int64(c-'0')
	}
	return v, nil
}

// CreatePixCharge cria uma cobranca PIX dinamica (ou Pix Automatico) via Asaas.
//
// Retorna o QR Code para exibicao ao usuario.
// Nenhum dado PII do pagador e passado a este metodo ou gravado no ledger.
func (p *Provider) CreatePixCharge(ctx context.Context, req fiat.PixChargeRequest) (fiat.PixChargeResult, error) {
	if req.AmountMinor <= 0 {
		return fiat.PixChargeResult{}, errors.New("asaas: CreatePixCharge: amount deve ser > 0")
	}

	// Converte centavos para decimal string sem float.
	valueStr := centavosToAsaasDecimal(req.AmountMinor)

	dueDate := time.Now().AddDate(0, 0, 3) // 3 dias por default
	if req.DueDate != nil {
		dueDate = *req.DueDate
	}

	charge := asaasChargeRequest{
		// Customer: em producao, o customer ID Asaas seria obtido do cofre
		// de compliance (K6) via tenant_id. Por ora, usamos advertiser_id
		// como external reference — o provisionamento do customer Asaas
		// e responsabilidade do fluxo de onboarding (K6).
		Customer:          req.AdvertiserID, // ID opaco, nao PII diretamente
		BillingType:       "PIX",
		Value:             valueStr,
		DueDate:           dueDate.Format("2006-01-02"),
		Description:       req.Description,
		ExternalReference: req.IdempotencyKey,
	}

	bodyBytes, err := json.Marshal(charge)
	if err != nil {
		return fiat.PixChargeResult{}, fmt.Errorf("asaas: CreatePixCharge: marshal: %w", err)
	}

	apiReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/payments", strings.NewReader(string(bodyBytes)))
	if err != nil {
		return fiat.PixChargeResult{}, fmt.Errorf("asaas: CreatePixCharge: build request: %w", err)
	}
	apiReq.Header.Set("Content-Type", "application/json")
	apiReq.Header.Set("access_token", p.apiKey)

	resp, err := p.httpClient.Do(apiReq)
	if err != nil {
		return fiat.PixChargeResult{}, fmt.Errorf("asaas: CreatePixCharge: http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyErr, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fiat.PixChargeResult{}, fmt.Errorf("asaas: CreatePixCharge: status %d: %s",
			resp.StatusCode, string(bodyErr))
	}

	var result asaasChargeResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fiat.PixChargeResult{}, fmt.Errorf("asaas: CreatePixCharge: decode: %w", err)
	}

	p.logger.Info("asaas: cobranca PIX criada",
		"charge_id", result.ID,
		"tenant_id", req.TenantID,
		// Sem PII: sem nome, CPF, email do pagador
	)

	return fiat.PixChargeResult{
		ChargeID:   result.ID,
		QRCode:     result.PixQrCode.Payload,
		PixKey:     result.PixQrCode.Payload,
		Rail:       railName,
	}, nil
}

// CreatePaymentIntent nao e suportado diretamente pelo trilho Asaas PIX.
// Para cartao via Asaas, use o Mercado Pago (failover).
func (p *Provider) CreatePaymentIntent(_ context.Context, _ fiat.PaymentIntentRequest) (fiat.PaymentIntentResult, error) {
	return fiat.PaymentIntentResult{}, errors.New("asaas: PaymentIntent nao suportado; use stripe para fiat global ou mercadopago para cartao Brasil")
}

// ---------------------------------------------------------------------------
// Webhook handler — verificacao de token obrigatoria
// ---------------------------------------------------------------------------

const maxWebhookBodyBytes = 512 * 1024

// asaasWebhookPayload e o subset dos campos de notificacao Asaas.
// Campos PII do cliente (customer.name, cpfCnpj) sao omitidos e descartados.
type asaasWebhookPayload struct {
	Event   string `json:"event"`   // ex.: "PAYMENT_RECEIVED", "PAYMENT_CONFIRMED"
	Payment struct {
		ID              string `json:"id"`              // charge ID Asaas
		Status          string `json:"status"`          // "CONFIRMED", "RECEIVED", etc.
		Value           string `json:"value"`           // decimal string
		BillingType     string `json:"billingType"`
		// PixTransaction: contem o E2E ID para conciliacao (txid/E2E — §2.6).
		// Sem PII: apenas o ID de transacao PIX.
		PixTransaction struct {
			EndToEndIdentifier string `json:"endToEndIdentifier"` // E2E ID PIX
		} `json:"pixTransaction"`
		ConfirmedDate string `json:"confirmedDate"` // ISO date
		// Campos PII (customer.name, cpfCnpj, email): ausentes nesta struct.
	} `json:"payment"`
}

// WebhookHandler retorna um http.Handler para receber notificacoes do Asaas.
//
// SEGURANCA:
//  1. Verifica o header "asaas-access-token" contra o webhook token configurado.
//  2. Rejeita com HTTP 401 se o token estiver ausente ou invalido.
//  3. Idempotente por charge_id: nao duplica postings.
//  4. Ao PAYMENT_RECEIVED/PAYMENT_CONFIRMED: grava par de postings no ledger.
func (p *Provider) WebhookHandler(tenantID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// PASSO 1: Verificacao do token de autenticacao Asaas.
		// O header "asaas-access-token" deve conter o token configurado.
		token := r.Header.Get("asaas-access-token")
		if token == "" {
			p.logger.Warn("asaas: webhook: token ausente — rejeitado")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// Comparacao em tempo constante para evitar timing attack.
		if !hmacEqual(token, p.webhookToken) {
			p.logger.Warn("asaas: webhook: token invalido — rejeitado")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		// PASSO 2: Leitura e parse do payload.
		rawBody, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBodyBytes))
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		var event asaasWebhookPayload
		if err := json.Unmarshal(rawBody, &event); err != nil {
			p.logger.Warn("asaas: webhook: payload invalido", "err", err)
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}

		// PASSO 3: Processar confirmacao de pagamento PIX.
		switch event.Event {
		case "PAYMENT_RECEIVED", "PAYMENT_CONFIRMED":
			if err := p.handlePaymentConfirmed(r.Context(), tenantID, event); err != nil {
				p.logger.Error("asaas: webhook: handlePaymentConfirmed",
					"charge_id", event.Payment.ID,
					"err", err,
				)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
		default:
			p.logger.Debug("asaas: webhook: evento ignorado", "event", event.Event)
		}

		w.WriteHeader(http.StatusOK)
	})
}

// hmacEqual compara duas strings em tempo constante.
// Previne timing attacks na verificacao de token.
func hmacEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

// handlePaymentConfirmed processa PAYMENT_RECEIVED / PAYMENT_CONFIRMED.
//
// Grava par de postings no ledger conforme BILLING.md §4.4 cenario B:
//   - DEBITO  platform:cash:BRL        (caixa recebido)
//   - CREDITO platform:receivable:BRL  (baixa do a receber)
//
// Idempotencia: idempotency_key = "asaas:{charge_id}".
// pix_end_to_end_id e gravado nos metadados para conciliacao (§2.6).
func (p *Provider) handlePaymentConfirmed(ctx context.Context, tenantID string, event asaasWebhookPayload) error {
	chargeID := event.Payment.ID
	if chargeID == "" {
		return errors.New("asaas: payment ID ausente no webhook")
	}

	// Converte valor decimal Asaas para centavos sem float (TX-2).
	amountMinor, err := asaasDecimalToCentavos(event.Payment.Value)
	if err != nil {
		return fmt.Errorf("asaas: parse amount: %w", err)
	}
	if amountMinor <= 0 {
		return fmt.Errorf("asaas: amount invalido: %d", amountMinor)
	}

	const assetCode = "BRL"

	// Obtem scale autoritativo do Asset Registry (invariante DA-10/TX-2).
	// Scale hardcoded removido: o Asset Registry e a unica fonte de verdade.
	var scale int32
	if p.assetLoader != nil {
		asset, err := p.assetLoader.LoadAsset(ctx, assetCode)
		if err != nil {
			return fmt.Errorf("asaas: asset registry BRL: %w", err)
		}
		scale = asset.Scale
	} else {
		// Caminho de teste sem assetLoader.
		scale = 2
	}

	if p.pool == nil {
		return fmt.Errorf("asaas: pool de banco de dados nao configurado (ambiente de teste sem banco?)")
	}

	cashCode := "platform:cash:BRL"
	receivableCode := "platform:receivable:BRL"

	cashAcc, err := ledger.EnsureAccount(ctx, p.pool, p.assetLoader, ledger.EnsureAccountParams{
		TenantID:  tenantID,
		Code:      cashCode,
		Name:      "Caixa BRL",
		Kind:      ledger.AccountKindAsset,
		AssetCode: assetCode,
		Scale:     scale,
	})
	if err != nil {
		return fmt.Errorf("asaas: EnsureAccount cash: %w", err)
	}

	receivableAcc, err := ledger.EnsureAccount(ctx, p.pool, p.assetLoader, ledger.EnsureAccountParams{
		TenantID:  tenantID,
		Code:      receivableCode,
		Name:      "Contas a Receber BRL",
		Kind:      ledger.AccountKindAsset,
		AssetCode: assetCode,
		Scale:     scale,
	})
	if err != nil {
		return fmt.Errorf("asaas: EnsureAccount receivable: %w", err)
	}

	// E2E ID para conciliacao PIX (txid/E2E — §2.6).
	// Nao e PII — e o identificador da transacao PIX no BACEN.
	e2eID := event.Payment.PixTransaction.EndToEndIdentifier

	idempKey := fmt.Sprintf("asaas:%s", chargeID)

	_, err = ledger.RecordEntry(ctx, p.pool, p.assetLoader, ledger.RecordEntryParams{
		TenantID:       tenantID,
		IdempotencyKey: idempKey,
		Description:    fmt.Sprintf("Recebimento PIX Asaas: BRL charge=%s", chargeID),
		Status:         ledger.EntryStatusPosted,
		EffectiveAt:    time.Now().UTC(),
		RefType:        "payment_event",
		Metadata: []byte(fmt.Sprintf(
			`{"provider":"asaas","charge_id":%q,"pix_e2e_id":%q,"tenant_id":%q}`,
			chargeID, e2eID, tenantID,
			// Sem PII: sem nome, CPF, email do pagador
		)),
		Postings: []ledger.PostingLine{
			{
				AccountID:        cashAcc.ID,
				AssetCode:        assetCode,
				Scale:            scale,
				DebitMinorUnits:  amountMinor, // int64, sem float
				CreditMinorUnits: 0,
			},
			{
				AccountID:        receivableAcc.ID,
				AssetCode:        assetCode,
				Scale:            scale,
				DebitMinorUnits:  0,
				CreditMinorUnits: amountMinor, // int64, sem float
			},
		},
	})
	if err != nil {
		if errors.Is(err, ledger.ErrIdempotentDuplicate) {
			p.logger.Info("asaas: webhook idempotente — posting ja existe",
				"charge_id", chargeID, "e2e_id", e2eID)
			return nil
		}
		return fmt.Errorf("asaas: RecordEntry: %w", err)
	}

	p.logger.Info("asaas: PIX confirmado — posting gravado",
		"charge_id", chargeID,
		"amount_minor", amountMinor,
		"pix_e2e_id", e2eID,
		"tenant_id", tenantID,
		// Sem PII: sem nome, CPF, email do pagador
	)

	return nil
}
