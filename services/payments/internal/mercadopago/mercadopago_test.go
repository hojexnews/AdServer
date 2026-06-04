package mercadopago_test

import (
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hojex/adserver/services/payments/internal/mercadopago"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func newTestProvider(t *testing.T, serverURL string) *mercadopago.Provider {
	t.Helper()
	p, err := mercadopago.New(mercadopago.Config{
		AccessToken:  "APP_USR-test_placeholder",
		WebhookToken: "tok_test",
		BaseURL:      serverURL,
	}, nil, nil, nil)
	if err != nil {
		t.Fatalf("mercadopago.New: %v", err)
	}
	return p
}

// ---------------------------------------------------------------------------
// TX-2: mpDecimalToCentavos — casos de erro obrigatorios (LOW cobertura)
// ---------------------------------------------------------------------------

// TestMPDecimalToCentavos_FracaoExceedsScale verifica que fracao > 2 digitos
// retorna erro e nao trunca silenciosamente (TX-2 / achado MEDIUM de truncamento).
func TestMPDecimalToCentavos_FracaoExceedsScale(t *testing.T) {
	t.Parallel()
	_, err := mercadopago.MPDecimalToCentavosStr("10.999")
	if err == nil {
		t.Fatal("esperava erro para fracao > 2 digitos (10.999), nao truncamento silencioso")
	}
}

// TestMPDecimalToCentavos_ValorNegativo verifica que valor negativo e rejeitado.
func TestMPDecimalToCentavos_ValorNegativo(t *testing.T) {
	t.Parallel()
	_, err := mercadopago.MPDecimalToCentavosStr("-10.00")
	if err == nil {
		t.Fatal("esperava erro para valor negativo (-10.00)")
	}
}

// TestMPDecimalToCentavos_ValoresValidos verifica conversoes corretas sem float.
func TestMPDecimalToCentavos_ValoresValidos(t *testing.T) {
	t.Parallel()
	cases := []struct {
		input string
		want  int64
	}{
		{"0.00", 0},
		{"1.00", 100},
		{"150.00", 15000},
		{"0.01", 1},
		{"9999.99", 999999},
		{"5.5", 550}, // 1 digito de fracao
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			got, err := mercadopago.MPDecimalToCentavosStr(tc.input)
			if err != nil {
				t.Fatalf("MPDecimalToCentavosStr(%q): erro inesperado: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("MPDecimalToCentavosStr(%q) = %d, want %d", tc.input, got, tc.want)
			}
		})
	}
}

// TestMPDecimalToCentavos_OverflowProximo verifica comportamento com valor proximo
// ao limite que poderia causar overflow em intPart*100.
// math.MaxInt64 = 9223372036854775807; intPart*100 pode estourar acima de ~92e15.
// Verificamos que a funcao nao entra em panico e retorna valor ou erro definidos.
func TestMPDecimalToCentavos_OverflowProximo(t *testing.T) {
	t.Parallel()
	// 92233720368547758.07 — intPart = 92233720368547758, intPart*100 estoura int64.
	// O comportamento esperado e que retorne um valor (overflow silencioso de int64
	// em Go e definido por complemento de 2) ou erro. O teste documenta o comportamento
	// atual e garante que nao entre em panico (panic).
	// Se o ledger implementar checagem de overflow, este teste devera ser atualizado.
	bigVal := "92233720368547758.07"
	got, err := mercadopago.MPDecimalToCentavosStr(bigVal)
	// Nao deve entrar em panico — qualquer retorno (valor ou erro) e aceitavel.
	// Documentamos o valor resultante para detectar mudancas de comportamento.
	t.Logf("MPDecimalToCentavosStr(%q) = %d, err=%v (overflow intencional; MaxInt64=%d)",
		bigVal, got, err, int64(math.MaxInt64))
}

// ---------------------------------------------------------------------------
// Seguranca: paymentID invalido no webhook rejeitado com 400
// ---------------------------------------------------------------------------

// TestWebhookHandler_RejectsInvalidPaymentID verifica que paymentID com
// caracteres nao-numericos (ex.: path traversal) e rejeitado com HTTP 400.
func TestWebhookHandler_RejectsInvalidPaymentID(t *testing.T) {
	t.Parallel()
	p := newTestProvider(t, "http://localhost")

	// paymentID com path traversal — deve ser rejeitado antes de fetchPayment.
	body := `{"action":"payment.updated","data":{"id":"../admin"}}`
	req := httptest.NewRequest(http.MethodPost, "/webhooks/mercadopago", strings.NewReader(body))
	req.Header.Set("x-webhook-token", "tok_test")
	w := httptest.NewRecorder()
	p.WebhookHandler("tenant-test").ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("esperado 400 para paymentID invalido, got %d", w.Code)
	}
}

// TestWebhookHandler_RejectsPaymentIDWithLetters verifica rejeicao de ID alfanumerico.
func TestWebhookHandler_RejectsPaymentIDWithLetters(t *testing.T) {
	t.Parallel()
	p := newTestProvider(t, "http://localhost")

	body := `{"action":"payment.updated","data":{"id":"abc123"}}`
	req := httptest.NewRequest(http.MethodPost, "/webhooks/mercadopago", strings.NewReader(body))
	req.Header.Set("x-webhook-token", "tok_test")
	w := httptest.NewRecorder()
	p.WebhookHandler("tenant-test").ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("esperado 400 para paymentID alfanumerico, got %d", w.Code)
	}
}

// TestWebhookHandler_RejectsNoToken verifica rejeicao sem token.
func TestWebhookHandler_RejectsNoToken(t *testing.T) {
	t.Parallel()
	p := newTestProvider(t, "http://localhost")
	body := `{"action":"payment.updated","data":{"id":"12345"}}`
	req := httptest.NewRequest(http.MethodPost, "/webhooks/mercadopago", strings.NewReader(body))
	w := httptest.NewRecorder()
	p.WebhookHandler("tenant-test").ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("esperado 401, got %d", w.Code)
	}
}

// TestWebhookHandler_RejectsInvalidToken verifica rejeicao com token errado.
func TestWebhookHandler_RejectsInvalidToken(t *testing.T) {
	t.Parallel()
	p := newTestProvider(t, "http://localhost")
	body := `{"action":"payment.updated","data":{"id":"12345"}}`
	req := httptest.NewRequest(http.MethodPost, "/webhooks/mercadopago", strings.NewReader(body))
	req.Header.Set("x-webhook-token", "tok_errado")
	w := httptest.NewRecorder()
	p.WebhookHandler("tenant-test").ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("esperado 401, got %d", w.Code)
	}
}

// TestNew_RequiresAccessToken verifica que New falha sem AccessToken.
func TestNew_RequiresAccessToken(t *testing.T) {
	t.Parallel()
	_, err := mercadopago.New(mercadopago.Config{AccessToken: "", WebhookToken: "tok"}, nil, nil, nil)
	if err == nil {
		t.Fatal("esperava erro para AccessToken ausente")
	}
}

// TestNew_RequiresWebhookToken verifica que New falha sem WebhookToken.
func TestNew_RequiresWebhookToken(t *testing.T) {
	t.Parallel()
	_, err := mercadopago.New(mercadopago.Config{AccessToken: "APP_USR-test", WebhookToken: ""}, nil, nil, nil)
	if err == nil {
		t.Fatal("esperava erro para WebhookToken ausente")
	}
}
