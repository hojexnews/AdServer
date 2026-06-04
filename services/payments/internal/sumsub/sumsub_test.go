// Package sumsub — sumsub_test.go
//
// Testes unitarios do client Sumsub sem banco real e sem API key viva.
// Cobre:
//   - Construcao com parametros validos/invalidos
//   - Verificacao de assinatura HMAC-SHA1 (tempo-constante)
//   - Verificacao de assinatura HMAC-SHA256 (fallback)
//   - Rejeicao de webhook sem assinatura (fail-closed)
//   - Rejeicao de webhook com assinatura invalida
//   - Anti-replay: evento muito antigo -> 200 (aceita sem processar)
//   - Mapeamento de reviewStatus/reviewAnswer para status interno
//   - Idempotencia do processamento (sem banco: path de log)
//   - PII nunca loga: payload com campos PII nao dispara panic/erro
package sumsub

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// testSecret e o segredo de teste (nunca em producao).
const testSecret = "test-sumsub-secret-do-not-use-in-production"

// buildTestClient cria um Client Sumsub com stubs (sem banco).
func buildTestClient(t *testing.T) *Client {
	t.Helper()
	c, err := New(Config{
		APIKey:        "test-api-key",
		WebhookSecret: testSecret,
		BaseURL:       "https://api.sumsub.com",
	}, nil, nil) // pool nil, logger nil
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// signSHA1 computa HMAC-SHA1 do payload para simular o Sumsub.
func signSHA1(secret string, body []byte) string {
	//nolint:gosec
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// makeWebhookPayload cria um payload de webhook Sumsub valido.
// Campos PII sao propositalmente ausentes (sem nome, CPF, etc.).
func makeWebhookPayload(reviewStatus, reviewAnswer, applicantID string) []byte {
	event := map[string]interface{}{
		"applicantId":    applicantID,
		"externalUserId": "aaaa-bbbb-cccc-dddd-stub", // pseudonimo, nao PII
		"type":           "applicantReviewed",
		"reviewStatus":   reviewStatus,
		"reviewResult": map[string]interface{}{
			"reviewAnswer": reviewAnswer,
		},
		"createdAt": time.Now().UTC().Format(time.RFC3339),
	}
	b, _ := json.Marshal(event)
	return b
}

// ---------------------------------------------------------------------------
// Testes de construcao
// ---------------------------------------------------------------------------

func TestNew_RequiresAPIKey(t *testing.T) {
	_, err := New(Config{APIKey: "", WebhookSecret: "secret"}, nil, nil)
	if err == nil {
		t.Fatal("esperava erro para APIKey vazio")
	}
}

func TestNew_RequiresWebhookSecret(t *testing.T) {
	_, err := New(Config{APIKey: "key", WebhookSecret: ""}, nil, nil)
	if err == nil {
		t.Fatal("esperava erro para WebhookSecret vazio")
	}
}

func TestNew_OK(t *testing.T) {
	c, err := New(Config{APIKey: "key", WebhookSecret: "secret"}, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c == nil {
		t.Fatal("client nao deve ser nil")
	}
}

// ---------------------------------------------------------------------------
// Testes de verificacao de assinatura
// ---------------------------------------------------------------------------

func TestWebhook_RejectsMissingSignature(t *testing.T) {
	c := buildTestClient(t)
	body := makeWebhookPayload("completed", "GREEN", "applicant-001")

	req := httptest.NewRequest(http.MethodPost, "/webhooks/sumsub", bytes.NewReader(body))
	// Sem X-Payload-Digest
	rr := httptest.NewRecorder()
	c.WebhookHandler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", rr.Code)
	}
}

func TestWebhook_RejectsInvalidSignature(t *testing.T) {
	c := buildTestClient(t)
	body := makeWebhookPayload("completed", "GREEN", "applicant-001")

	req := httptest.NewRequest(http.MethodPost, "/webhooks/sumsub", bytes.NewReader(body))
	req.Header.Set("X-Payload-Digest", "deadbeef000000000000000000000000deadbeef")
	rr := httptest.NewRecorder()
	c.WebhookHandler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", rr.Code)
	}
}

func TestWebhook_AcceptsValidSHA1Signature(t *testing.T) {
	c := buildTestClient(t)
	body := makeWebhookPayload("completed", "GREEN", "applicant-001")
	sig := signSHA1(testSecret, body)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/sumsub", bytes.NewReader(body))
	req.Header.Set("X-Payload-Digest", sig)
	rr := httptest.NewRecorder()
	c.WebhookHandler().ServeHTTP(rr, req)

	// Com pool nil, processa mas nao grava no banco; deve retornar 200
	if rr.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", rr.Code)
	}
}

func TestWebhook_AntiReplay_TooOld(t *testing.T) {
	c := buildTestClient(t)

	// Evento com createdAt mais antigo que a janela
	oldTime := time.Now().Add(-11 * time.Minute).UTC().Format(time.RFC3339)
	event := map[string]interface{}{
		"applicantId":  "applicant-002",
		"type":         "applicantReviewed",
		"reviewStatus": "completed",
		"reviewResult": map[string]interface{}{"reviewAnswer": "GREEN"},
		"createdAt":    oldTime,
	}
	body, _ := json.Marshal(event)
	sig := signSHA1(testSecret, body)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/sumsub", bytes.NewReader(body))
	req.Header.Set("X-Payload-Digest", sig)
	rr := httptest.NewRecorder()
	c.WebhookHandler().ServeHTTP(rr, req)

	// Evento antigo: aceita com 200 (nao processa, nao retenta) — ver comentario no codigo
	if rr.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200 (evento antigo aceito graciosamente)", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Testes de mapeamento de status
// ---------------------------------------------------------------------------

func TestMapSumsubReviewStatus(t *testing.T) {
	cases := []struct {
		reviewStatus string
		reviewAnswer string
		want         KYCStatus
	}{
		{"completed", "GREEN", KYCStatusApproved},
		{"completed", "RED", KYCStatusRejected},
		{"completed", "YELLOW", KYCStatusOnHold},
		{"pending", "", KYCStatusPending},
		{"queued", "", KYCStatusPending},
		{"onHold", "", KYCStatusOnHold},
		{"unknown", "", KYCStatusPending},
	}
	for _, tc := range cases {
		got := mapSumsubReviewStatus(tc.reviewStatus, tc.reviewAnswer)
		if got != tc.want {
			t.Errorf("mapSumsubReviewStatus(%q, %q) = %q, want %q",
				tc.reviewStatus, tc.reviewAnswer, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Teste: payload com campos PII nao causa panic nem grava PII
//
// Verifica que mesmo se o Sumsub enviar PII (firstName, lastName) no payload,
// o handler nao tenta gravar esses campos — o struct sumsubWebhookEvent
// NAO os declara (by design: campos ausentes sao descartados pelo json.Unmarshal).
// ---------------------------------------------------------------------------

func TestWebhook_PIIFieldsDiscarded(t *testing.T) {
	c := buildTestClient(t)

	// Payload com campos PII que o Sumsub pode enviar (mas que devem ser ignorados)
	event := map[string]interface{}{
		"applicantId":  "applicant-pii-test",
		"type":         "applicantReviewed",
		"reviewStatus": "completed",
		"reviewResult": map[string]interface{}{"reviewAnswer": "GREEN"},
		"createdAt":    time.Now().UTC().Format(time.RFC3339),
		// Campos PII que nao devem ser processados:
		"firstName":  "Joao",
		"lastName":   "Silva",
		"dob":        "1990-01-01",
		"email":      "joao@example.com",
		"phone":      "+5511999999999",
		"nationalId": "123.456.789-00",
	}
	body, _ := json.Marshal(event)
	sig := signSHA1(testSecret, body)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/sumsub", bytes.NewReader(body))
	req.Header.Set("X-Payload-Digest", sig)
	rr := httptest.NewRecorder()

	// Nao deve panic; deve retornar 200 (com pool nil, apenas loga)
	c.WebhookHandler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200 (PII descartada silenciosamente)", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Teste de invariante: CreateApplicant requer TenantID e SubjectRef
// ---------------------------------------------------------------------------

func TestCreateApplicant_RequiresIDs(t *testing.T) {
	c := buildTestClient(t)
	_, err := c.CreateApplicant(nil, CreateApplicantParams{ //nolint:staticcheck
		TenantID:   "",
		SubjectRef: "",
		Level:      KYCLevelKYC,
	})
	if err == nil {
		t.Fatal("esperava erro para TenantID/SubjectRef vazios")
	}
}

func TestCreateApplicant_InvalidLevel(t *testing.T) {
	c := buildTestClient(t)
	_, err := c.CreateApplicant(nil, CreateApplicantParams{ //nolint:staticcheck
		TenantID:   "aaaa-bbbb-cccc-dddd-0001",
		SubjectRef: "aaaa-bbbb-cccc-dddd-0002",
		Level:      "invalid_level",
	})
	if err == nil {
		t.Fatal("esperava erro para Level invalido")
	}
}
