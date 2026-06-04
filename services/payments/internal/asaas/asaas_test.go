package asaas_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hojex/adserver/services/payments/internal/asaas"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func newTestProvider(t *testing.T, serverURL, webhookToken string) *asaas.Provider {
	t.Helper()
	p, err := asaas.New(asaas.Config{
		APIKey:       "aact_test_placeholder",
		WebhookToken: webhookToken,
		BaseURL:      serverURL,
	}, nil, nil, nil)
	if err != nil {
		t.Fatalf("asaas.New: %v", err)
	}
	return p
}

// ---------------------------------------------------------------------------
// Webhook: verificacao de token
// ---------------------------------------------------------------------------

func TestWebhookHandler_RejectsNoToken(t *testing.T) {
	t.Parallel()
	p := newTestProvider(t, "http://localhost", "token_secreto")
	body := `{"event":"PAYMENT_RECEIVED","payment":{"id":"cob_001","value":"150.00"}}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/asaas", strings.NewReader(body))
	// Sem header asaas-access-token.
	w := httptest.NewRecorder()
	p.WebhookHandler("tenant-test").ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("esperado 401, got %d", w.Code)
	}
}

func TestWebhookHandler_RejectsInvalidToken(t *testing.T) {
	t.Parallel()
	p := newTestProvider(t, "http://localhost", "token_correto")
	body := `{"event":"PAYMENT_RECEIVED","payment":{"id":"cob_001","value":"150.00"}}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/asaas", strings.NewReader(body))
	req.Header.Set("asaas-access-token", "token_errado")
	w := httptest.NewRecorder()
	p.WebhookHandler("tenant-test").ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("esperado 401 para token invalido, got %d", w.Code)
	}
}

func TestWebhookHandler_IgnoresUnknownEvent(t *testing.T) {
	t.Parallel()
	const token = "token_secreto"
	p := newTestProvider(t, "http://localhost", token)
	body := `{"event":"PAYMENT_CREATED","payment":{"id":"cob_002","value":"50.00"}}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/asaas", strings.NewReader(body))
	req.Header.Set("asaas-access-token", token)
	w := httptest.NewRecorder()
	// Evento desconhecido com token valido deve retornar 200 sem processar.
	p.WebhookHandler("tenant-test").ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("esperado 200 para evento desconhecido, got %d", w.Code)
	}
}

// TestWebhookHandler_ConfirmedEventWithValidToken verifica que evento
// PAYMENT_CONFIRMED com token valido passa a autenticacao (falha com 500
// por ausencia de banco, nao com 401 ou 400).
func TestWebhookHandler_ConfirmedEventWithValidToken(t *testing.T) {
	t.Parallel()
	const token = "token_secreto"
	p := newTestProvider(t, "http://localhost", token)
	body := `{
		"event": "PAYMENT_CONFIRMED",
		"payment": {
			"id": "cob_003",
			"status": "CONFIRMED",
			"value": "200.00",
			"billingType": "PIX",
			"pixTransaction": {"endToEndIdentifier": "E12345678202606041200000001"},
			"confirmedDate": "2026-06-04"
		}
	}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/asaas", strings.NewReader(body))
	req.Header.Set("asaas-access-token", token)
	w := httptest.NewRecorder()
	p.WebhookHandler("tenant-test").ServeHTTP(w, req)
	// 401 indicaria falha de autenticacao.
	if w.Code == http.StatusUnauthorized {
		t.Error("handler rejeitou com 401 apesar de token valido")
	}
	// 400 indicaria payload invalido.
	if w.Code == http.StatusBadRequest {
		t.Error("handler rejeitou com 400 apesar de payload valido")
	}
	// 500 e esperado por ausencia de banco.
}

// ---------------------------------------------------------------------------
// Decimal sem float (TX-2): conversao via servidor simulado
// ---------------------------------------------------------------------------

func TestCreatePixCharge_ValorDecimalSemFloat(t *testing.T) {
	t.Parallel()

	cases := []struct {
		centavos int64
		wantStr  string
	}{
		{15000, "150.00"},
		{1, "0.01"},
		{100, "1.00"},
		{12345, "123.45"},
		{99999, "999.99"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.wantStr, func(t *testing.T) {
			t.Parallel()

			var capturedBody string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				buf := make([]byte, 2048)
				n, _ := r.Body.Read(buf)
				capturedBody = string(buf[:n])
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"id":"cob_ok","status":"PENDING","pix":{"payload":"qr","encodedImage":""}}`))
			}))
			defer srv.Close()

			p, err := asaas.New(asaas.Config{
				APIKey:       "aact_test",
				WebhookToken: "tok",
				BaseURL:      srv.URL,
			}, nil, nil, nil)
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			// Cria cobranca PIX; ignora erro pois assetLoader e nil (sem validacao).
			_, _ = p.CreatePixChargeRaw(tc.centavos, "tenant-t", "adv-t", "descr")

			if !strings.Contains(capturedBody, tc.wantStr) {
				t.Errorf("centavos=%d: esperado %q no body enviado, got: %s",
					tc.centavos, tc.wantStr, capturedBody)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// PII: campos de pagador descartados pelo decoder
// ---------------------------------------------------------------------------

func TestWebhookHandler_NoPIIParsed(t *testing.T) {
	t.Parallel()
	const token = "tok_pii"
	p := newTestProvider(t, "http://localhost", token)
	// Payload com campos PII que devem ser descartados silenciosamente.
	body := `{
		"event": "PAYMENT_RECEIVED",
		"payment": {
			"id": "cob_pii",
			"status": "RECEIVED",
			"value": "100.00",
			"billingType": "PIX",
			"pixTransaction": {"endToEndIdentifier": "E00000001202606041000000001"},
			"payer": {
				"name": "Maria Fulana",
				"cpfCnpj": "123.456.789-00",
				"email": "maria@example.com"
			}
		}
	}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/asaas", strings.NewReader(body))
	req.Header.Set("asaas-access-token", token)
	w := httptest.NewRecorder()
	p.WebhookHandler("tenant-test").ServeHTTP(w, req)
	// Nao deve rejeitar payload com campos PII extras.
	if w.Code == http.StatusBadRequest {
		t.Error("handler nao deve rejeitar payload com campos PII extras")
	}
}

// ---------------------------------------------------------------------------
// New com configuracao invalida
// ---------------------------------------------------------------------------

func TestNew_RequiresAPIKey(t *testing.T) {
	t.Parallel()
	_, err := asaas.New(asaas.Config{APIKey: "", WebhookToken: "tok"}, nil, nil, nil)
	if err == nil {
		t.Fatal("esperava erro para APIKey ausente")
	}
}

func TestNew_RequiresWebhookToken(t *testing.T) {
	t.Parallel()
	_, err := asaas.New(asaas.Config{APIKey: "aact_test", WebhookToken: ""}, nil, nil, nil)
	if err == nil {
		t.Fatal("esperava erro para WebhookToken ausente")
	}
}

// ---------------------------------------------------------------------------
// TX-2: asaasDecimalToCentavos — casos de erro obrigatorios (LOW cobertura)
// ---------------------------------------------------------------------------

// TestAsaasDecimalToCentavos_FracaoExceedsScale verifica que fracao > 2 digitos
// retorna erro e nao trunca silenciosamente (TX-2 / achado MEDIUM de truncamento).
func TestAsaasDecimalToCentavos_FracaoExceedsScale(t *testing.T) {
	t.Parallel()
	_, err := asaas.AsaasDecimalToCentavos("10.999")
	if err == nil {
		t.Fatal("esperava erro para fracao > 2 digitos (10.999), nao truncamento silencioso")
	}
}

// TestAsaasDecimalToCentavos_ValorNegativo verifica que valor negativo e rejeitado.
// O provedor Asaas nao deve enviar valores negativos; receber um indica anomalia.
func TestAsaasDecimalToCentavos_ValorNegativo(t *testing.T) {
	t.Parallel()
	_, err := asaas.AsaasDecimalToCentavos("-10.00")
	if err == nil {
		t.Fatal("esperava erro para valor negativo (-10.00)")
	}
}

// TestAsaasDecimalToCentavos_ValoresValidos verifica conversoes corretas sem float.
func TestAsaasDecimalToCentavos_ValoresValidos(t *testing.T) {
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
		{"10.", 1000},   // fracao vazia
		{"5.5", 550},    // 1 digito de fracao
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			got, err := asaas.AsaasDecimalToCentavos(tc.input)
			if err != nil {
				t.Fatalf("AsaasDecimalToCentavos(%q): erro inesperado: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("AsaasDecimalToCentavos(%q) = %d, want %d", tc.input, got, tc.want)
			}
		})
	}
}
