// Package mercadopago implementa o trilho fiat Brasil via Mercado Pago (failover).
//
// # Posicionamento
//
// Mercado Pago e o provedor FAILOVER para o trilho Brasil.
// Primario: Asaas (PIX). Failover: Mercado Pago (PIX + cartao Brasil).
// O roteamento de failover e feito pelo FailoverProvider (provider_failover.go).
//
// # Float proibido (TX-2)
//
// Mercado Pago representa valores em reais como number JSON (ex.: 150.00).
// Para evitar float, lemos o JSON como string e convertemos para int64
// via manipulacao inteira. NUNCA usamos json.Number -> float64.
//
// # PII
//
// A API do Mercado Pago pode retornar dados do pagador (payer.email,
// payer.identification.number). Esses campos sao deliberadamente omitidos
// dos structs de parse. Apenas: payment_id, status, amount, pix_data.
//
// # SAQ-A
//
// Para pagamentos com cartao, o Mercado Pago usa tokenizacao client-side
// via SDK JS (MercadoPago.js). O backend recebe apenas o card_token opaco.
// Nenhum dado PAN/CVV aparece neste pacote.
package mercadopago

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/hojex/adserver/internal/ledger"
	"github.com/hojex/adserver/services/payments/internal/fiat"
	"github.com/jackc/pgx/v5/pgxpool"
)

// rePaymentID valida que o payment ID do webhook contem apenas digitos.
// Compilada uma vez no nivel do pacote para eficiencia.
// Previne injecao de parametro na URL de fetchPayment (ex.: "../admin").
var rePaymentID = regexp.MustCompile(`^[0-9]+$`)

const railName = "mercadopago"

// ErrInvalidWebhookSignature indica falha na verificacao de assinatura de webhook.
var ErrInvalidWebhookSignature = errors.New("mercadopago: assinatura de webhook invalida")

// Provider e a implementacao do trilho Mercado Pago.
// Satisfaz fiat.FiatProvider.
type Provider struct {
	accessToken  string // APP_USR-... — NUNCA logue
	webhookToken string // token de verificacao de webhook — NUNCA logue
	baseURL      string // https://api.mercadopago.com
	httpClient   *http.Client
	pool         *pgxpool.Pool
	assetLoader  ledger.AssetLoader
	logger       *slog.Logger
}

// Config agrupa a configuracao do provedor Mercado Pago.
type Config struct {
	AccessToken  string // MERCADOPAGO_ACCESS_TOKEN
	WebhookToken string // MERCADOPAGO_WEBHOOK_TOKEN
	BaseURL      string // default: "https://api.mercadopago.com"
}

// New cria um Provider Mercado Pago pronto para uso.
// Se logger for nil, usa slog.Default().
func New(cfg Config, pool *pgxpool.Pool, assetLoader ledger.AssetLoader, logger *slog.Logger) (*Provider, error) {
	if cfg.AccessToken == "" {
		return nil, errors.New("mercadopago: AccessToken obrigatorio")
	}
	if cfg.WebhookToken == "" {
		return nil, errors.New("mercadopago: WebhookToken obrigatorio para verificar webhooks")
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.mercadopago.com"
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Provider{
		accessToken:  cfg.AccessToken,
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

// Healthy verifica se a API do Mercado Pago esta acessivel.
func (p *Provider) Healthy(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		p.baseURL+"/v1/payment_methods", nil)
	if err != nil {
		return fmt.Errorf("mercadopago: healthy: criar request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.accessToken)
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("mercadopago: healthy: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("mercadopago: healthy: status %d", resp.StatusCode)
	}
	return nil
}

// ---------------------------------------------------------------------------
// CreatePaymentIntent — cartao tokenizado client-side (SAQ-A)
// ---------------------------------------------------------------------------

// mpPaymentRequest e o payload para criar um pagamento no Mercado Pago.
// Para cartao: usa card_token (opaco, gerado client-side pelo MercadoPago.js).
// Para PIX: transaction_amount + payment_method_id: "pix".
// Nenhum PAN/CVV aparece aqui.
type mpPaymentRequest struct {
	TransactionAmount json.Number `json:"transaction_amount"` // decimal como string JSON
	Description       string      `json:"description"`
	PaymentMethodID   string      `json:"payment_method_id"`
	ExternalReference string      `json:"external_reference"` // nosso idempotency key
	// Pix: nenhum campo adicional necessario.
	// Cartao: card_token (token opaco, nao PAN) — omitido aqui, adicionado
	// dinamicamente quando presente.
	Installments int `json:"installments,omitempty"`
}

// mpPaymentResponse e o subset da resposta de pagamento do Mercado Pago.
// Campos PII (payer.email, identification.number) omitidos e descartados.
type mpPaymentResponse struct {
	ID     int64  `json:"id"`
	Status string `json:"status"` // "approved", "pending", "rejected"
	// PointOfInteraction contem o QR Code para pagamento PIX.
	PointOfInteraction struct {
		TransactionData struct {
			QRCode      string `json:"qr_code"`
			QRCodeBase64 string `json:"qr_code_base64"`
			TicketURL   string `json:"ticket_url"`
		} `json:"transaction_data"`
	} `json:"point_of_interaction"`
	// Campos PII (payer.email, payer.identification.*): ausentes desta struct.
}

// centavosToMPDecimal converte int64 centavos para json.Number sem float.
// Ex.: 15000 -> json.Number("150.00")
func centavosToMPDecimal(centavos int64) json.Number {
	intPart := centavos / 100
	fracPart := centavos % 100
	if fracPart < 0 {
		fracPart = -fracPart
	}
	return json.Number(fmt.Sprintf("%d.%02d", intPart, fracPart))
}

// mpDecimalToCentavos converte json.Number do Mercado Pago para int64 centavos.
// Sem float (TX-2): operacao inteira pura.
func mpDecimalToCentavos(n json.Number) (int64, error) {
	s := n.String()
	return asaasDecimalToCentavos(s)
}

// asaasDecimalToCentavos e reimplementada aqui para evitar dependencia circular.
// (Mesmo algoritmo de asaas.asaasDecimalToCentavos, com mesma correccao TX-2.)
//
// TX-2: fracao com mais de 2 digitos e ERRO, nao truncamento silencioso.
// O provedor ja deve respeitar scale=2; divergencia indica problema upstream.
func asaasDecimalToCentavos(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("mercadopago: valor vazio")
	}
	// Rejeita valores negativos (o sinal '-' invalida parseI64 abaixo,
	// mas checamos explicitamente para mensagem de erro clara).
	if strings.HasPrefix(s, "-") {
		return 0, fmt.Errorf("mercadopago: valor negativo nao permitido: %q", s)
	}
	parts := strings.SplitN(s, ".", 2)
	intPart, err := parseI64(parts[0])
	if err != nil {
		return 0, fmt.Errorf("mercadopago: parse inteiro %q: %w", s, err)
	}
	var fracPart int64
	if len(parts) == 2 {
		frac := parts[1]
		switch len(frac) {
		case 0:
			fracPart = 0
		case 1:
			fracPart, err = parseI64(frac + "0")
		case 2:
			fracPart, err = parseI64(frac)
		default:
			// TX-2: truncamento silencioso proibido.
			// Fração com mais de 2 digitos e erro explicito.
			return 0, fmt.Errorf("mercadopago: fracao excede scale: %q", s)
		}
		if err != nil {
			return 0, fmt.Errorf("mercadopago: parse fracao %q: %w", s, err)
		}
	}
	return intPart*100 + fracPart, nil
}

func parseI64(s string) (int64, error) {
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

// CreatePaymentIntent cria um pagamento no Mercado Pago.
//
// Para fiat Brasil com cartao: o front tokeniza o cartao via MercadoPago.js
// e envia o card_token opaco ao BFF. O BFF passa o token neste request.
// O PAN nunca chega ao backend (SAQ-A analogico para Mercado Pago).
//
// NOTA: Este metodo aceita payment com payment_method_id = "pix" tambem,
// mas o caminho preferido para PIX e CreatePixCharge.
func (p *Provider) CreatePaymentIntent(ctx context.Context, req fiat.PaymentIntentRequest) (fiat.PaymentIntentResult, error) {
	if req.AmountMinor <= 0 {
		return fiat.PaymentIntentResult{}, errors.New("mercadopago: CreatePaymentIntent: amount deve ser > 0")
	}

	mpReq := mpPaymentRequest{
		TransactionAmount: centavosToMPDecimal(req.AmountMinor),
		Description:       req.Description,
		PaymentMethodID:   "pix", // default para failover de PIX
		ExternalReference: req.IdempotencyKey,
		Installments:      1,
	}

	bodyBytes, err := json.Marshal(mpReq)
	if err != nil {
		return fiat.PaymentIntentResult{}, fmt.Errorf("mercadopago: marshal: %w", err)
	}

	apiReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/v1/payments", strings.NewReader(string(bodyBytes)))
	if err != nil {
		return fiat.PaymentIntentResult{}, fmt.Errorf("mercadopago: CreatePaymentIntent: build request: %w", err)
	}
	apiReq.Header.Set("Content-Type", "application/json")
	apiReq.Header.Set("Authorization", "Bearer "+p.accessToken)
	if req.IdempotencyKey != "" {
		apiReq.Header.Set("X-Idempotency-Key", req.IdempotencyKey)
	}

	resp, err := p.httpClient.Do(apiReq)
	if err != nil {
		return fiat.PaymentIntentResult{}, fmt.Errorf("mercadopago: CreatePaymentIntent: http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyErr, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fiat.PaymentIntentResult{}, fmt.Errorf("mercadopago: CreatePaymentIntent: status %d: %s",
			resp.StatusCode, string(bodyErr))
	}

	var result mpPaymentResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fiat.PaymentIntentResult{}, fmt.Errorf("mercadopago: CreatePaymentIntent: decode: %w", err)
	}

	p.logger.Info("mercadopago: payment intent criado",
		"payment_id", result.ID,
		"status", result.Status,
		"tenant_id", req.TenantID,
		// Sem PII: sem email, CPF, dados de cartao
	)

	return fiat.PaymentIntentResult{
		PaymentID: fmt.Sprintf("%d", result.ID),
		// ClientSecret: Mercado Pago nao tem client_secret no mesmo modelo Stripe.
		// Para PIX, o QR code e retornado via PointOfInteraction.
		ClientSecret: result.PointOfInteraction.TransactionData.QRCode,
		Rail:         railName,
	}, nil
}

// CreatePixCharge cria uma cobranca PIX via Mercado Pago.
// Usado como failover quando Asaas esta indisponivel.
func (p *Provider) CreatePixCharge(ctx context.Context, req fiat.PixChargeRequest) (fiat.PixChargeResult, error) {
	if req.AmountMinor <= 0 {
		return fiat.PixChargeResult{}, errors.New("mercadopago: CreatePixCharge: amount deve ser > 0")
	}

	mpReq := mpPaymentRequest{
		TransactionAmount: centavosToMPDecimal(req.AmountMinor),
		Description:       req.Description,
		PaymentMethodID:   "pix",
		ExternalReference: req.IdempotencyKey,
		Installments:      1,
	}

	bodyBytes, err := json.Marshal(mpReq)
	if err != nil {
		return fiat.PixChargeResult{}, fmt.Errorf("mercadopago: CreatePixCharge: marshal: %w", err)
	}

	apiReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/v1/payments", strings.NewReader(string(bodyBytes)))
	if err != nil {
		return fiat.PixChargeResult{}, fmt.Errorf("mercadopago: CreatePixCharge: build request: %w", err)
	}
	apiReq.Header.Set("Content-Type", "application/json")
	apiReq.Header.Set("Authorization", "Bearer "+p.accessToken)
	if req.IdempotencyKey != "" {
		apiReq.Header.Set("X-Idempotency-Key", req.IdempotencyKey)
	}

	resp, err := p.httpClient.Do(apiReq)
	if err != nil {
		return fiat.PixChargeResult{}, fmt.Errorf("mercadopago: CreatePixCharge: http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyErr, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fiat.PixChargeResult{}, fmt.Errorf("mercadopago: CreatePixCharge: status %d: %s",
			resp.StatusCode, string(bodyErr))
	}

	var result mpPaymentResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fiat.PixChargeResult{}, fmt.Errorf("mercadopago: CreatePixCharge: decode: %w", err)
	}

	qrCode := result.PointOfInteraction.TransactionData.QRCode

	p.logger.Info("mercadopago: PIX criado",
		"payment_id", result.ID,
		"tenant_id", req.TenantID,
		// Sem PII
	)

	return fiat.PixChargeResult{
		ChargeID: fmt.Sprintf("%d", result.ID),
		QRCode:   qrCode,
		PixKey:   qrCode,
		Rail:     railName,
	}, nil
}

// ---------------------------------------------------------------------------
// Webhook handler
// ---------------------------------------------------------------------------

const maxWebhookBodyBytes = 512 * 1024

// mpWebhookPayload e o subset da notificacao do Mercado Pago.
// Campos PII (payer.email, identification.*): omitidos e descartados.
type mpWebhookPayload struct {
	Action string `json:"action"` // ex.: "payment.updated"
	Data   struct {
		ID string `json:"id"` // payment ID como string
	} `json:"data"`
}

// mpPaymentDetail e o detalhe de um pagamento consultado via API.
// Campos PII omitidos.
type mpPaymentDetail struct {
	ID                  int64       `json:"id"`
	Status              string      `json:"status"`
	StatusDetail        string      `json:"status_detail"`
	TransactionAmount   json.Number `json:"transaction_amount"`
	CurrencyID          string      `json:"currency_id"` // "BRL"
	ExternalReference   string      `json:"external_reference"`
	// PixData: apenas o E2E ID para conciliacao. Sem PII do pagador.
	PointOfInteraction struct {
		TransactionData struct {
			E2EID string `json:"e2e_id"` // PIX End-to-End ID
		} `json:"transaction_data"`
	} `json:"point_of_interaction"`
	// Campos PII (payer.*): ausentes desta struct.
}

// WebhookHandler retorna um http.Handler para notificacoes do Mercado Pago.
//
// SEGURANCA:
//  1. Verifica header "x-webhook-token" contra o token configurado.
//  2. Rejeita com HTTP 401 se invalido ou ausente.
//  3. Idempotente por payment_id: nao duplica postings.
//  4. Ao payment.updated com status "approved": grava par de postings.
func (p *Provider) WebhookHandler(tenantID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// PASSO 1: Verificacao do token de webhook.
		token := r.Header.Get("x-webhook-token")
		if token == "" {
			p.logger.Warn("mercadopago: webhook: token ausente — rejeitado")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !hmacEqual(token, p.webhookToken) {
			p.logger.Warn("mercadopago: webhook: token invalido — rejeitado")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		rawBody, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBodyBytes))
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		var event mpWebhookPayload
		if err := json.Unmarshal(rawBody, &event); err != nil {
			p.logger.Warn("mercadopago: webhook: payload invalido", "err", err)
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}

		if event.Action == "payment.updated" && event.Data.ID != "" {
			// Valida paymentID antes de usar na URL de fetchPayment.
			// event.Data.ID vem do corpo do webhook (input externo); sem validacao
			// poderia injetar segmentos de path na chamada API (ex.: "../admin").
			if !rePaymentID.MatchString(event.Data.ID) {
				p.logger.Warn("mercadopago: webhook: payment ID invalido — rejeitado",
					"payment_id", event.Data.ID)
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if err := p.handlePaymentApproved(r.Context(), tenantID, event.Data.ID); err != nil {
				p.logger.Error("mercadopago: webhook: handlePaymentApproved",
					"payment_id", event.Data.ID,
					"err", err,
				)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
		}

		w.WriteHeader(http.StatusOK)
	})
}

// hmacEqual compara em tempo constante.
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

// handlePaymentApproved consulta o pagamento e grava postings se aprovado.
//
// Idempotencia: idempotency_key = "mercadopago:{payment_id}".
func (p *Provider) handlePaymentApproved(ctx context.Context, tenantID, paymentID string) error {
	// Consulta o pagamento na API para obter detalhes atualizados.
	detail, err := p.fetchPayment(ctx, paymentID)
	if err != nil {
		return fmt.Errorf("mercadopago: fetchPayment: %w", err)
	}

	if detail.Status != "approved" {
		p.logger.Debug("mercadopago: webhook: pagamento nao aprovado",
			"payment_id", paymentID, "status", detail.Status)
		return nil
	}

	// Converte valor para centavos sem float (TX-2).
	amountMinor, err := mpDecimalToCentavos(detail.TransactionAmount)
	if err != nil {
		return fmt.Errorf("mercadopago: parse amount: %w", err)
	}
	if amountMinor <= 0 {
		return fmt.Errorf("mercadopago: amount invalido: %d", amountMinor)
	}

	// Mercado Pago Brasil usa BRL.
	const assetCode = "BRL"

	// Obtem scale autoritativo do Asset Registry (invariante DA-10/TX-2).
	// Scale hardcoded removido: o Asset Registry e a unica fonte de verdade.
	var scale int32
	if p.assetLoader != nil {
		asset, err := p.assetLoader.LoadAsset(ctx, assetCode)
		if err != nil {
			return fmt.Errorf("mercadopago: asset registry BRL: %w", err)
		}
		scale = asset.Scale
	} else {
		// Caminho de teste sem assetLoader.
		scale = 2
	}

	if p.pool == nil {
		return fmt.Errorf("mercadopago: pool de banco de dados nao configurado (ambiente de teste sem banco?)")
	}

	cashAcc, err := ledger.EnsureAccount(ctx, p.pool, p.assetLoader, ledger.EnsureAccountParams{
		TenantID:  tenantID,
		Code:      "platform:cash:BRL",
		Name:      "Caixa BRL",
		Kind:      ledger.AccountKindAsset,
		AssetCode: assetCode,
		Scale:     scale,
	})
	if err != nil {
		return fmt.Errorf("mercadopago: EnsureAccount cash: %w", err)
	}

	receivableAcc, err := ledger.EnsureAccount(ctx, p.pool, p.assetLoader, ledger.EnsureAccountParams{
		TenantID:  tenantID,
		Code:      "platform:receivable:BRL",
		Name:      "Contas a Receber BRL",
		Kind:      ledger.AccountKindAsset,
		AssetCode: assetCode,
		Scale:     scale,
	})
	if err != nil {
		return fmt.Errorf("mercadopago: EnsureAccount receivable: %w", err)
	}

	e2eID := detail.PointOfInteraction.TransactionData.E2EID
	idempKey := fmt.Sprintf("mercadopago:%s", paymentID)

	_, err = ledger.RecordEntry(ctx, p.pool, p.assetLoader, ledger.RecordEntryParams{
		TenantID:       tenantID,
		IdempotencyKey: idempKey,
		Description:    fmt.Sprintf("Recebimento MercadoPago: BRL payment=%s", paymentID),
		Status:         ledger.EntryStatusPosted,
		EffectiveAt:    time.Now().UTC(),
		RefType:        "payment_event",
		Metadata: []byte(fmt.Sprintf(
			`{"provider":"mercadopago","payment_id":%q,"pix_e2e_id":%q,"tenant_id":%q}`,
			paymentID, e2eID, tenantID,
			// Sem PII: sem email, CPF, identificacao do pagador
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
			p.logger.Info("mercadopago: webhook idempotente — posting ja existe",
				"payment_id", paymentID)
			return nil
		}
		return fmt.Errorf("mercadopago: RecordEntry: %w", err)
	}

	p.logger.Info("mercadopago: pagamento aprovado — posting gravado",
		"payment_id", paymentID,
		"amount_minor", amountMinor,
		"pix_e2e_id", e2eID,
		"tenant_id", tenantID,
		// Sem PII
	)

	return nil
}

// fetchPayment consulta os detalhes de um pagamento na API do Mercado Pago.
// URL fixa (sem SSRF por URL controlada por usuario).
func (p *Provider) fetchPayment(ctx context.Context, paymentID string) (mpPaymentDetail, error) {
	// URL fixa: baseURL e configurado em Config, nao vem de input do usuario.
	url := fmt.Sprintf("%s/v1/payments/%s", p.baseURL, paymentID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return mpPaymentDetail{}, fmt.Errorf("mercadopago: fetchPayment: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.accessToken)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return mpPaymentDetail{}, fmt.Errorf("mercadopago: fetchPayment: http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return mpPaymentDetail{}, fmt.Errorf("mercadopago: fetchPayment: status %d", resp.StatusCode)
	}

	var detail mpPaymentDetail
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		return mpPaymentDetail{}, fmt.Errorf("mercadopago: fetchPayment: decode: %w", err)
	}
	return detail, nil
}
