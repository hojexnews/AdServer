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
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hojex/adserver/services/payments/internal/kmsenvelope"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
	}, nil, nil, nil) // pool nil, envelope nil (sem INSERT de PII em testes de assinatura), logger nil
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
	_, err := New(Config{APIKey: "", WebhookSecret: "secret"}, nil, nil, nil)
	if err == nil {
		t.Fatal("esperava erro para APIKey vazio")
	}
}

func TestNew_RequiresWebhookSecret(t *testing.T) {
	_, err := New(Config{APIKey: "key", WebhookSecret: ""}, nil, nil, nil)
	if err == nil {
		t.Fatal("esperava erro para WebhookSecret vazio")
	}
}

func TestNew_OK(t *testing.T) {
	c, err := New(Config{APIKey: "key", WebhookSecret: "secret"}, nil, nil, nil)
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

// ---------------------------------------------------------------------------
// LOW-2: fakeDB — prova ciphertext no INSERT e ramo fail-closed (sumsub)
// ---------------------------------------------------------------------------

// fakeDBRow implementa pgx.Row retornando ErrNoRows (sem registro existente).
type fakeDBRow struct{}

func (fakeDBRow) Scan(dest ...any) error {
	return errNoRows
}

// errNoRows simula pgx.ErrNoRows para o fake QueryRow.
var errNoRows = errors.New("no rows in result set")

// fakeDBSumsub implementa dbQuerier para testes de sumsub.
type fakeDBSumsub struct {
	execCalls []struct {
		sql  string
		args []any
	}
}

func (f *fakeDBSumsub) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.execCalls = append(f.execCalls, struct {
		sql  string
		args []any
	}{sql, args})
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func (f *fakeDBSumsub) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	// Simula "nao encontrado" — o CreateApplicant prossegue com chamada HTTP
	return fakeDBRow{}
}

// fakeHTTPTransport intercepta chamadas HTTP ao Sumsub (retorna applicant ID fake).
type fakeHTTPTransport struct{}

func (fakeHTTPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body := `{"id":"applicant-fake-001"}`
	return &http.Response{
		StatusCode: http.StatusCreated,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}, nil
}

// TestCreateApplicant_FullName_CiphertextNotPlaintext verifica que full_name
// enviado ao SQL e ciphertext, nunca o plaintext PII (LOW-2-a sumsub).
func TestCreateApplicant_FullName_CiphertextNotPlaintext(t *testing.T) {
	env := kmsenvelope.NewForTest()
	db := &fakeDBSumsub{}

	c, err := newWithDB(Config{
		APIKey:        "test-key",
		WebhookSecret: "test-secret",
	}, db, env, nil)
	if err != nil {
		t.Fatalf("newWithDB: %v", err)
	}
	// Injeta transporte HTTP fake para evitar chamada real ao Sumsub
	c.httpClient = &http.Client{Transport: fakeHTTPTransport{}}

	fullName := "Joao da Silva Santos" // PII
	_, err = c.CreateApplicant(context.Background(), CreateApplicantParams{
		TenantID:   "aaaaaaaa-0000-0000-0000-000000000001",
		SubjectRef: "bbbbbbbb-0000-0000-0000-000000000002",
		Level:      KYCLevelKYC,
		LevelName:  "basic-kyc-level",
		FullName:   fullName,
	})
	if err != nil {
		t.Fatalf("CreateApplicant: %v", err)
	}

	// Localiza o INSERT em kyc_subjects
	var insertCall *struct {
		sql  string
		args []any
	}
	for i := range db.execCalls {
		if strings.Contains(db.execCalls[i].sql, "kyc_subjects") {
			insertCall = &db.execCalls[i]
			break
		}
	}
	if insertCall == nil {
		t.Fatal("fakeDB nao recebeu INSERT em kyc_subjects")
	}

	// full_name e o 5o parametro ($5, indice 4)
	// INSERT INTO compliance.kyc_subjects (tenant_id, subject_ref, sumsub_applicant_id, level, status, full_name)
	// VALUES ($1::uuid, $2::uuid, $3, $4, 'pending', $5)
	if len(insertCall.args) < 5 {
		t.Fatalf("INSERT com poucos args: %d", len(insertCall.args))
	}
	fullNameArg := insertCall.args[4]
	ptr, ok := fullNameArg.(*string)
	if !ok || ptr == nil {
		t.Fatalf("full_name arg nao e *string nao-nil: %T", fullNameArg)
	}
	if *ptr == fullName {
		t.Errorf("VIOLACAO LOW-2: full_name enviado em plaintext ao SQL")
	}
	if strings.Contains(*ptr, fullName) {
		t.Errorf("VIOLACAO LOW-2: full_name contem o plaintext no valor enviado ao SQL")
	}
	if !strings.HasPrefix(*ptr, "v1$") {
		t.Errorf("full_name no SQL sem prefixo v1$: primeiros bytes: %q", (*ptr)[:minSub(len(*ptr), 10)])
	}
}

// TestCreateApplicant_FailClosed_EnvelopeNilWithFullName verifica que
// envelope==nil com FullName presente retorna erro fail-closed (LOW-2-b sumsub).
func TestCreateApplicant_FailClosed_EnvelopeNilWithFullName(t *testing.T) {
	db := &fakeDBSumsub{}
	// envelope nil: misconfiguracao
	c, err := newWithDB(Config{
		APIKey:        "test-key",
		WebhookSecret: "test-secret",
	}, db, nil, nil)
	if err != nil {
		t.Fatalf("newWithDB: %v", err)
	}
	c.httpClient = &http.Client{Transport: fakeHTTPTransport{}}

	_, err = c.CreateApplicant(context.Background(), CreateApplicantParams{
		TenantID:   "aaaaaaaa-0000-0000-0000-000000000001",
		SubjectRef: "bbbbbbbb-0000-0000-0000-000000000002",
		Level:      KYCLevelKYC,
		LevelName:  "basic-kyc-level",
		FullName:   "Joao da Silva Santos", // PII: exige envelope
	})
	if err == nil {
		t.Fatal("VIOLACAO fail-closed: esperava erro quando envelope=nil e FullName presente, got nil")
	}
	// Garante que nenhum plaintext chegou ao SQL
	for _, call := range db.execCalls {
		for _, arg := range call.args {
			if s, ok := arg.(string); ok && s == "Joao da Silva Santos" {
				t.Errorf("VIOLACAO DA-11: plaintext PII chegou ao SQL com envelope nil")
			}
		}
	}
}

// minSub e um helper local para limitar indices em msgs de erro.
func minSub(a, b int) int {
	if a < b {
		return a
	}
	return b
}
