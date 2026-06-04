// Package crypto — safe_webhook_test.go
//
// Testes do SafeWebhookHandler sem banco real.
// Usa ledger stubs (AssetLoaderStub) e pool nil para verificar a logica
// de assinatura/parsing/despacho sem dependencia de Postgres.
//
// Casos cobertos:
//   - Rejeita webhook sem assinatura (HTTP 401)
//   - Rejeita webhook com assinatura invalida (HTTP 401)
//   - Aceita webhook com assinatura correta
//   - Anti-replay: rejeita timestamp muito antigo
//   - Tipo desconhecido: retorna 200 (ignora graciosamente)
//   - Deposito detectado: valida logica de parse
//   - Reorg: valida logica de parse
//   - Float PROIBIDO: amount_minor_units chegam como int64 (sem float no JSON)
package crypto

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hojex/adserver/internal/ledger"
)

// testSecret e o segredo de teste (nunca em producao).
const testSecret = "test-secret-do-not-use-in-production"

// signPayload computa a assinatura HMAC-SHA256 para o payload de teste.
func signPayload(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// buildTestHandler cria um SafeWebhookHandler com stubs (sem banco).
// pool=nil: os handlers que precisam de banco retornarao erro (testamos
// apenas a camada de assinatura/parsing para evitar dependencia de Postgres).
func buildTestHandler(t *testing.T) *SafeWebhookHandler {
	t.Helper()
	assetLoader := ledger.NewAssetLoaderStub([]ledger.Asset{
		{Code: "USDC", Scale: 6, Enabled: true},
	})
	h, err := NewSafeWebhookHandler(
		Config{
			WebhookSecret: testSecret,
			ChainConfigs: map[int64]ChainAccountConfig{
				1: {AssetCode: "USDC", AssetScale: 6},
			},
		},
		nil, // pool nil — testes de assinatura/parsing nao chegam ao banco
		assetLoader,
		nil, // connector nil — testes de assinatura/parsing nao chegam ao InjectDeposit
		nil, // logger nil -> slog.Default()
	)
	if err != nil {
		t.Fatalf("NewSafeWebhookHandler: %v", err)
	}
	return h
}

// makePayload cria um payload valido de teste com timestamp atual.
func makePayload(eventType webhookEventType, txHash string, extra map[string]interface{}) []byte {
	base := map[string]interface{}{
		"type":               string(eventType),
		"timestamp":          time.Now().Unix(),
		"tx_hash":            txHash,
		"chain_id":           1,
		"block_number":       12345,
		"confirmations":      3,
		"to_address":         "0xsafe000000000000000000000000000000001",
		"asset_code":         "USDC",
		"amount_minor_units": int64(1_000_000), // 1 USDC; int64, sem float (TX-2)
		"tenant_id":          "00000000-0000-0000-0000-000000000001",
		"advertiser_id":      "adv-001",
	}
	for k, v := range extra {
		base[k] = v
	}
	b, _ := json.Marshal(base)
	return b
}

// ---------------------------------------------------------------------------
// Testes de assinatura
// ---------------------------------------------------------------------------

func TestSafeWebhook_RejectsMissingSignature(t *testing.T) {
	h := buildTestHandler(t)
	body := makePayload(eventTypeDepositDetected, "0x1234", nil)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/safe", bytes.NewReader(body))
	// Sem header X-Safe-Signature
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", rr.Code)
	}
}

func TestSafeWebhook_RejectsInvalidSignature(t *testing.T) {
	h := buildTestHandler(t)
	body := makePayload(eventTypeDepositDetected, "0x1234", nil)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/safe", bytes.NewReader(body))
	req.Header.Set("X-Safe-Signature", "sha256=deaadbeef0000000000000000000000000000000000000000000000000000000")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", rr.Code)
	}
}

func TestSafeWebhook_AcceptsValidSignature_UnknownEventType(t *testing.T) {
	h := buildTestHandler(t)
	body := makePayload("unknown.event", "0x1234", nil)
	sig := signPayload(testSecret, body)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/safe", bytes.NewReader(body))
	req.Header.Set("X-Safe-Signature", sig)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	// Evento desconhecido deve retornar 200 (ignora graciosamente — nao quebra)
	if rr.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", rr.Code)
	}
}

func TestSafeWebhook_AntiReplay_TooOld(t *testing.T) {
	h := buildTestHandler(t)
	// Timestamp com 11 minutos de atraso (acima da janela de 10 min)
	oldTimestamp := time.Now().Add(-11 * time.Minute).Unix()
	base := map[string]interface{}{
		"type":               string(eventTypeDepositDetected),
		"timestamp":          oldTimestamp,
		"tx_hash":            "0xabc",
		"chain_id":           1,
		"amount_minor_units": int64(1_000_000),
		"tenant_id":          "00000000-0000-0000-0000-000000000001",
	}
	body, _ := json.Marshal(base)
	sig := signPayload(testSecret, body)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/safe", bytes.NewReader(body))
	req.Header.Set("X-Safe-Signature", sig)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401 (anti-replay)", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Testes de parsing de payload
// ---------------------------------------------------------------------------

func TestSafeWebhook_DepositDetected_ValidatesFields(t *testing.T) {
	h := buildTestHandler(t)

	// Payload valido — deve passar pela verificacao de assinatura e parsing,
	// mas falhar no banco (pool nil). O status 500 confirma que chegou ao handler.
	body := makePayload(eventTypeDepositDetected, "0xdeposit001", nil)
	sig := signPayload(testSecret, body)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/safe", bytes.NewReader(body))
	req.Header.Set("X-Safe-Signature", sig)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	// Com pool nil, EnsureAccount retorna erro -> 500 e esperado.
	// O importante e que NAO retornou 401 (assinatura validada) nem 400 (payload valido).
	if rr.Code == http.StatusUnauthorized {
		t.Error("deveria ter passado na verificacao de assinatura")
	}
	if rr.Code == http.StatusBadRequest {
		t.Error("payload valido nao deveria retornar 400")
	}
}

func TestSafeWebhook_Reorg_ValidatesFields(t *testing.T) {
	h := buildTestHandler(t)
	body := makePayload(eventTypeReorg, "0xreorg001", map[string]interface{}{
		"original_tx_hash": "0xoriginal001",
		"reorg_reason":     "chain_reorg",
	})
	sig := signPayload(testSecret, body)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/safe", bytes.NewReader(body))
	req.Header.Set("X-Safe-Signature", sig)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	// Com pool nil, falha no banco -> 500. Assinatura e payload validos.
	if rr.Code == http.StatusUnauthorized {
		t.Error("deveria ter passado na verificacao de assinatura")
	}
	if rr.Code == http.StatusBadRequest {
		t.Error("payload de reorg valido nao deveria retornar 400")
	}
}

func TestSafeWebhook_DepositFinalized_ValidatesFields(t *testing.T) {
	h := buildTestHandler(t)
	body := makePayload(eventTypeDepositFinalized, "0xfinalized001", map[string]interface{}{
		"confirmations": uint32(12),
	})
	sig := signPayload(testSecret, body)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/safe", bytes.NewReader(body))
	req.Header.Set("X-Safe-Signature", sig)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	// Com pool nil, falha no banco -> 500. Assinatura e payload validos.
	if rr.Code == http.StatusUnauthorized {
		t.Error("deveria ter passado na verificacao de assinatura")
	}
}

// ---------------------------------------------------------------------------
// Teste de invariante monetaria: amount chega como int64 (sem float)
// ---------------------------------------------------------------------------

func TestSafeWebhook_AmountIsInt64_NotFloat(t *testing.T) {
	// Verifica que o JSON do payload usa amount_minor_units como inteiro.
	// Se fosse float64, a serializacao/desserializacao perderia precisao.
	// TX-2: float PROIBIDO em valores monetarios.
	payload := safeWebhookPayload{
		Type:             eventTypeDepositDetected,
		Timestamp:        time.Now().Unix(),
		TxHash:           "0xtest",
		ChainID:          1,
		AssetCode:        "USDC",
		AmountMinorUnits: 9_999_999, // 9.999999 USDC — valor que float32 nao representa exato
		TenantID:         "00000000-0000-0000-0000-000000000001",
	}

	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded safeWebhookPayload
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.AmountMinorUnits != 9_999_999 {
		t.Errorf("amount_minor_units perdeu precisao: got %d, want 9_999_999 (TX-2 violada)",
			decoded.AmountMinorUnits)
	}
}

func TestSafeWebhook_NewHandler_RequiresSecret(t *testing.T) {
	_, err := NewSafeWebhookHandler(Config{WebhookSecret: ""}, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("esperava erro para WebhookSecret vazio")
	}
}
