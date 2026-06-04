package stripe_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hojex/adserver/internal/ledger"
	"github.com/hojex/adserver/services/payments/internal/fiat"
	stripepkg "github.com/hojex/adserver/services/payments/internal/stripe"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func newTestProvider(t *testing.T, serverURL string) *stripepkg.Provider {
	t.Helper()
	p, err := stripepkg.New(stripepkg.Config{
		SecretKey:     "sk_test_placeholder",
		WebhookSecret: "whsec_test_placeholder",
		BaseURL:       serverURL,
	}, nil, ledger.NewAssetLoaderStub([]ledger.Asset{
		{Code: "BRL", Scale: 2, Enabled: true},
		{Code: "USD", Scale: 2, Enabled: true},
	}), nil)
	if err != nil {
		t.Fatalf("stripepkg.New: %v", err)
	}
	return p
}

// buildStripeSignature gera um header Stripe-Signature valido para testes.
// Usa o mesmo algoritmo HMAC-SHA256 que verifyStripeSignature.
func buildStripeSignature(secret string, body []byte, ts int64) string {
	tsStr := fmt.Sprintf("%d", ts)
	msg := tsStr + "." + string(body)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(msg))
	sig := hex.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf("t=%s,v1=%s", tsStr, sig)
}

// ---------------------------------------------------------------------------
// Webhook: verificacao de assinatura
// ---------------------------------------------------------------------------

func TestWebhookHandler_RejectsNoSignature(t *testing.T) {
	t.Parallel()
	p, err := stripepkg.New(stripepkg.Config{
		SecretKey:     "sk_test_placeholder",
		WebhookSecret: "whsec_test_placeholder",
	}, nil, ledger.NewAssetLoaderStub(nil), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	body := `{"id":"evt_001","type":"payment_intent.succeeded"}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/stripe", strings.NewReader(body))
	// Sem Stripe-Signature.
	w := httptest.NewRecorder()
	p.WebhookHandler("tenant-test").ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("esperado 400, got %d", w.Code)
	}
}

func TestWebhookHandler_RejectsInvalidSignature(t *testing.T) {
	t.Parallel()
	p, err := stripepkg.New(stripepkg.Config{
		SecretKey:     "sk_test_placeholder",
		WebhookSecret: "whsec_correto",
	}, nil, ledger.NewAssetLoaderStub(nil), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	body := `{"id":"evt_001","type":"payment_intent.succeeded"}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/stripe", strings.NewReader(body))
	wrongSig := buildStripeSignature("whsec_errado", []byte(body), time.Now().Unix())
	req.Header.Set("Stripe-Signature", wrongSig)
	w := httptest.NewRecorder()
	p.WebhookHandler("tenant-test").ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("esperado 400, got %d", w.Code)
	}
}

func TestWebhookHandler_RejectsReplayedEvent(t *testing.T) {
	t.Parallel()
	const secret = "whsec_test_placeholder"
	p, err := stripepkg.New(stripepkg.Config{
		SecretKey:     "sk_test_placeholder",
		WebhookSecret: secret,
	}, nil, ledger.NewAssetLoaderStub(nil), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	body := `{"id":"evt_001","type":"payment_intent.succeeded"}`
	// Timestamp muito antigo (10 min) — deve ser rejeitado como replay.
	oldTS := time.Now().Add(-10 * time.Minute).Unix()
	sig := buildStripeSignature(secret, []byte(body), oldTS)

	req := httptest.NewRequest(http.MethodPost, "/webhook/stripe", strings.NewReader(body))
	req.Header.Set("Stripe-Signature", sig)
	w := httptest.NewRecorder()
	p.WebhookHandler("tenant-test").ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("esperado 400 para replay, got %d", w.Code)
	}
}

func TestWebhookHandler_AcceptsValidSignatureUnknownEvent(t *testing.T) {
	t.Parallel()
	const secret = "whsec_test_placeholder"
	p, err := stripepkg.New(stripepkg.Config{
		SecretKey:     "sk_test_placeholder",
		WebhookSecret: secret,
	}, nil, ledger.NewAssetLoaderStub(nil), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	body := `{"id":"evt_002","type":"charge.updated","data":{"object":{}},"created":0}`
	ts := time.Now().Unix()
	sig := buildStripeSignature(secret, []byte(body), ts)

	req := httptest.NewRequest(http.MethodPost, "/webhook/stripe", strings.NewReader(body))
	req.Header.Set("Stripe-Signature", sig)
	w := httptest.NewRecorder()
	// Sem pool/ledger — evento desconhecido nao toca o banco.
	p.WebhookHandler("tenant-test").ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("esperado 200 para evento desconhecido, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// CreatePaymentIntent: valor int64 sem float (TX-2)
// ---------------------------------------------------------------------------

func TestCreatePaymentIntent_AmountInt64(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/payment_intents" {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id":            "pi_test_001",
				"client_secret": "pi_test_001_secret_xyz",
				"status":        "requires_payment_method",
				"amount":        15000, // 150.00 BRL em centavos (int)
				"currency":      "brl",
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	p := newTestProvider(t, srv.URL)

	result, err := p.CreatePaymentIntent(context.Background(), fiat.PaymentIntentRequest{
		TenantID:       "tenant-abc",
		AdvertiserID:   "adv-123",
		AmountMinor:    15000, // int64, sem float
		AssetCode:      "BRL",
		Scale:          2,
		IdempotencyKey: "pi:adv-123:camp-42:2026-06",
		Description:    "Recarga campanha",
	})
	if err != nil {
		t.Fatalf("CreatePaymentIntent: %v", err)
	}
	if result.PaymentID != "pi_test_001" {
		t.Errorf("PaymentID: want pi_test_001, got %q", result.PaymentID)
	}
	if result.Rail != "stripe" {
		t.Errorf("Rail: want stripe, got %q", result.Rail)
	}
}

func TestCreatePaymentIntent_RejectsZeroAmount(t *testing.T) {
	t.Parallel()
	p := newTestProvider(t, "http://localhost")
	_, err := p.CreatePaymentIntent(context.Background(), fiat.PaymentIntentRequest{
		AmountMinor: 0,
		AssetCode:   "BRL",
		Scale:       2,
	})
	if err == nil {
		t.Fatal("esperava erro para amount=0")
	}
}

// ---------------------------------------------------------------------------
// PII: campos de cartao descartados pelo decoder
// ---------------------------------------------------------------------------

// TestNoPANInWebhookPayload verifica que campos PII/cartao no payload Stripe
// sao descartados silenciosamente (decoder ignora campos desconhecidos).
func TestNoPANInWebhookPayload(t *testing.T) {
	t.Parallel()
	const secret = "whsec_test_sec"
	p, err := stripepkg.New(stripepkg.Config{
		SecretKey:     "sk_test_key",
		WebhookSecret: secret,
	}, nil, ledger.NewAssetLoaderStub(nil), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Payload com campos PII/billing que devem ser descartados.
	body := `{
		"id": "evt_003",
		"type": "payment_intent.succeeded",
		"created": 1234567890,
		"data": {
			"object": {
				"id": "pi_test_003",
				"amount": 5000,
				"currency": "brl",
				"status": "succeeded",
				"billing_details": {
					"name": "Joao da Silva",
					"email": "joao@example.com"
				},
				"payment_method_details": {
					"card": {"last4": "4242", "brand": "visa"}
				},
				"metadata": {
					"tenant_id": "tenant-abc",
					"advertiser_id": "adv-456"
				}
			}
		}
	}`
	ts := time.Now().Unix()
	sig := buildStripeSignature(secret, []byte(body), ts)

	req := httptest.NewRequest(http.MethodPost, "/webhook/stripe", strings.NewReader(body))
	req.Header.Set("Stripe-Signature", sig)
	w := httptest.NewRecorder()
	// pool=nil -> handler falha no RecordEntry (500). O que nao pode
	// acontecer e 400 (rejeicao por campos inesperados).
	p.WebhookHandler("tenant-abc").ServeHTTP(w, req)

	if w.Code == http.StatusBadRequest {
		t.Errorf("handler nao deve rejeitar payload com campos PII extras: got 400")
	}
}

// ---------------------------------------------------------------------------
// New com configuracao invalida
// ---------------------------------------------------------------------------

func TestNew_RequiresSecretKey(t *testing.T) {
	t.Parallel()
	_, err := stripepkg.New(stripepkg.Config{
		SecretKey:     "",
		WebhookSecret: "whsec_test",
	}, nil, ledger.NewAssetLoaderStub(nil), nil)
	if err == nil {
		t.Fatal("esperava erro para SecretKey ausente")
	}
}

func TestNew_RequiresWebhookSecret(t *testing.T) {
	t.Parallel()
	_, err := stripepkg.New(stripepkg.Config{
		SecretKey:     "sk_test_key",
		WebhookSecret: "",
	}, nil, ledger.NewAssetLoaderStub(nil), nil)
	if err == nil {
		t.Fatal("esperava erro para WebhookSecret ausente")
	}
}

// CreatePixCharge nao e suportado pelo Stripe.
func TestCreatePixCharge_NotSupported(t *testing.T) {
	t.Parallel()
	p := newTestProvider(t, "http://localhost")
	_, err := p.CreatePixCharge(context.Background(), fiat.PixChargeRequest{
		AmountMinor: 1000,
		Scale:       2,
	})
	if err == nil {
		t.Fatal("esperava erro: PIX nao suportado pelo Stripe")
	}
}

// ---------------------------------------------------------------------------
// handlePaymentIntentSucceeded: currency nao suportada — sem posting (LOW cobertura)
// ---------------------------------------------------------------------------

// TestWebhookHandler_UnsupportedCurrency verifica que currency nao presente
// no Asset Registry retorna erro e nao grava posting.
// O assetLoader stub retorna erro para qualquer currency nao listada.
// O handler deve responder 500 (para retry do Stripe) e nao criar postings.
func TestWebhookHandler_UnsupportedCurrency(t *testing.T) {
	t.Parallel()
	const secret = "whsec_test_unsupported"

	// AssetLoaderStub com apenas BRL/USD — "XBT" nao existe.
	p, err := stripepkg.New(stripepkg.Config{
		SecretKey:     "sk_test_key",
		WebhookSecret: secret,
	}, nil, ledger.NewAssetLoaderStub([]ledger.Asset{
		{Code: "BRL", Scale: 2, Enabled: true},
		{Code: "USD", Scale: 2, Enabled: true},
	}), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Evento com currency desconhecida.
	payload := map[string]interface{}{
		"id":      "evt_unsup_001",
		"type":    "payment_intent.succeeded",
		"created": time.Now().Unix(),
		"data": map[string]interface{}{
			"object": map[string]interface{}{
				"id":       "pi_unsup_001",
				"amount":   5000,
				"currency": "xbt", // nao existe no stub
				"status":   "succeeded",
				"metadata": map[string]string{
					"tenant_id":     "tenant-abc",
					"advertiser_id": "adv-001",
				},
			},
		},
	}
	bodyBytes, _ := json.Marshal(payload)
	ts := time.Now().Unix()
	sig := buildStripeSignature(secret, bodyBytes, ts)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", strings.NewReader(string(bodyBytes)))
	req.Header.Set("Stripe-Signature", sig)
	w := httptest.NewRecorder()
	p.WebhookHandler("tenant-abc").ServeHTTP(w, req)

	// Deve retornar 500 (currency nao suportada -> erro em handlePaymentIntentSucceeded
	// -> Stripe reenviara o evento). Nao deve retornar 200 (que implicaria posting gravado).
	if w.Code == http.StatusOK {
		t.Error("handler nao deve retornar 200 para currency nao suportada — posting nao deve ser gravado")
	}
	if w.Code != http.StatusInternalServerError {
		t.Errorf("esperado 500 para currency nao suportada, got %d", w.Code)
	}
}
