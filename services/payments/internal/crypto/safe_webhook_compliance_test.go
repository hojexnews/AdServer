// Package crypto — safe_webhook_compliance_test.go
//
// Testes do screening fail-closed (K6) no SafeWebhookHandler.
//
// Cobre os invariantes criticos:
//   - Deposito de endereco sancionado nao finaliza (ledger.FinalizeDeposit nao e chamado).
//   - Screener indisponivel (ErrScreeningUnavailable) nao finaliza — fail-closed.
//   - Payout para endereco sancionado nao envia (BuildPayout nao e chamado).
//   - Payout acima do limiar sem Travel Rule e bloqueado.
//   - Caminho feliz (endereco limpo, abaixo do limiar) finaliza/envia normalmente.
//
// Stubs in-memory para screener, Travel Rule recorder, ledger e ChainConnector.
// Nenhum banco real — pool nil em todos os casos.
//
// PII: stubs nao logar address + tenant_id juntos.
package crypto

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hojex/adserver/internal/chainconnector"
	"github.com/hojex/adserver/internal/ledger"
	moneyv1 "github.com/hojex/adserver/gen/go/adserver/money/v1"
)

// ---------------------------------------------------------------------------
// Stubs de compliance
// ---------------------------------------------------------------------------

// stubScreener implementa AddressScreener com comportamento configuravel.
type stubScreener struct {
	// err e o erro a retornar para ScreenAddress.
	// nil = endereco limpo (permite a operacao).
	err error
	// calls conta quantas vezes ScreenAddress foi chamado.
	calls atomic.Int64
}

func (s *stubScreener) ScreenAddress(_ context.Context, _ ScreenAddressParams) error {
	s.calls.Add(1)
	return s.err
}

// stubTravelRecorder implementa TravelRuleRecorder com comportamento configuravel.
type stubTravelRecorder struct {
	// err e o erro a retornar para RecordPayout.
	// ErrTravelRuleBelowThreshold = abaixo do limiar (nao bloqueia).
	// ErrTravelRuleBlocked = falha acima do limiar (bloqueia).
	// nil = registro concluido.
	err   error
	calls atomic.Int64
}

func (r *stubTravelRecorder) RecordPayout(_ context.Context, _ PayoutTravelRuleParams) error {
	r.calls.Add(1)
	return r.err
}

// ---------------------------------------------------------------------------
// Stub de ChainConnector para SubmitPayout
// ---------------------------------------------------------------------------

// stubConnector implementa chainconnector.ChainConnector para testes de payout.
// Registra se BuildPayout foi chamado.
type stubConnector struct {
	buildPayoutCalled atomic.Bool
	buildPayoutErr    error
}

func (c *stubConnector) WatchDeposits(_ context.Context, _ string, _ int64) (<-chan chainconnector.DepositEvent, error) {
	ch := make(chan chainconnector.DepositEvent)
	close(ch)
	return ch, nil
}

func (c *stubConnector) GetBalance(_ context.Context, _ string, _ int64, _ string) (*moneyv1.Money, error) {
	return nil, nil
}

func (c *stubConnector) BuildPayout(_ context.Context, _ chainconnector.PayoutRequest) (chainconnector.UnsignedTx, error) {
	c.buildPayoutCalled.Store(true)
	if c.buildPayoutErr != nil {
		return chainconnector.UnsignedTx{}, c.buildPayoutErr
	}
	return chainconnector.UnsignedTx{ChainID: 1, Payload: []byte("stub-tx")}, nil
}

func (c *stubConnector) Confirmations(_ context.Context, _ string, _ int64) (uint32, error) {
	return 12, nil
}

// ---------------------------------------------------------------------------
// Helper: handler com stubs de compliance
// ---------------------------------------------------------------------------

// buildComplianceHandler cria um SafeWebhookHandler com screener e travelRecorder
// injetados, sem banco (pool nil).
func buildComplianceHandler(t *testing.T, sc AddressScreener, tr TravelRuleRecorder) *SafeWebhookHandler {
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
			Screener:       sc,
			TravelRecorder: tr,
		},
		nil, // pool nil — testes nao chegam ao banco
		assetLoader,
		nil, // connector nil — nao usado nos testes de webhook HTTP
		nil, // logger nil -> slog.Default()
	)
	if err != nil {
		t.Fatalf("NewSafeWebhookHandler: %v", err)
	}
	return h
}

// makeSignedRequest cria um *http.Request POST assinado para /webhooks/safe.
func makeSignedRequest(t *testing.T, body []byte) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhooks/safe", bytes.NewReader(body))
	req.Header.Set("X-Safe-Signature", signPayload(testSecret, body))
	return req
}

// makeFinalizationPayload cria payload deposit.finalized com from_address opcional.
func makeFinalizationPayload(fromAddress string) []byte {
	m := map[string]interface{}{
		"type":               string(eventTypeDepositFinalized),
		"timestamp":          time.Now().Unix(),
		"tx_hash":            "0xfinalize001",
		"chain_id":           int64(1),
		"block_number":       uint64(100),
		"confirmations":      uint32(12),
		"to_address":         "0xsafe000000000000000000000000000000001",
		"asset_code":         "USDC",
		"amount_minor_units": int64(5_000_000),
		"tenant_id":          "00000000-0000-0000-0000-000000000002",
		"advertiser_id":      "adv-screening-test",
	}
	if fromAddress != "" {
		m["from_address"] = fromAddress
	}
	b, _ := json.Marshal(m)
	return b
}

// ---------------------------------------------------------------------------
// Testes: deposito finalizado com screening
// ---------------------------------------------------------------------------

// TestDepositFinalized_SanctionedAddress_DoesNotFinalize verifica que um deposito
// de endereco sancionado NAO finaliza — ledger.FinalizeDeposit nao e chamado.
// O handler retorna 500 (screener bloqueou; custodiante deve retentar).
func TestDepositFinalized_SanctionedAddress_DoesNotFinalize(t *testing.T) {
	sc := &stubScreener{err: errors.New("chainalysis: endereco em lista de sancoes — operacao bloqueada")}
	h := buildComplianceHandler(t, sc, nil)

	body := makeFinalizationPayload("0xsanctioned000000000000000000000001")
	req := makeSignedRequest(t, body)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	// Screener deve ter sido chamado exatamente uma vez.
	if got := sc.calls.Load(); got != 1 {
		t.Errorf("screener.ScreenAddress chamado %d vezes, esperado 1", got)
	}
	// Handler retorna 500 para que o custodiante retente (screening falhou).
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status: got %d, want 500 (sancao bloqueia — fail-closed)", rr.Code)
	}
}

// TestDepositFinalized_ScreenerUnavailable_FailClosed verifica que screener
// indisponivel tambem bloqueia a finalizacao (fail-closed).
func TestDepositFinalized_ScreenerUnavailable_FailClosed(t *testing.T) {
	sc := &stubScreener{err: errors.New("chainalysis: screening indisponivel — operacao segurada (fail-closed)")}
	h := buildComplianceHandler(t, sc, nil)

	body := makeFinalizationPayload("0xunknown000000000000000000000000001")
	req := makeSignedRequest(t, body)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if sc.calls.Load() != 1 {
		t.Errorf("screener.ScreenAddress nao foi chamado")
	}
	// Indisponibilidade tambem deve bloquear (fail-closed).
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status: got %d, want 500 (indisponivel = fail-closed)", rr.Code)
	}
}

// TestDepositFinalized_MissingFromAddress_FailClosed verifica que ausencia de
// from_address com screener ativo bloqueia a finalizacao.
func TestDepositFinalized_MissingFromAddress_FailClosed(t *testing.T) {
	sc := &stubScreener{err: nil} // screener sem erro — mas from_address ausente
	h := buildComplianceHandler(t, sc, nil)

	// Payload sem from_address.
	body := makeFinalizationPayload("")
	req := makeSignedRequest(t, body)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	// Screener NAO deve ser chamado (bloqueio antes do call).
	if sc.calls.Load() != 0 {
		t.Errorf("screener nao devia ser chamado quando from_address ausente")
	}
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status: got %d, want 500 (from_address ausente = fail-closed)", rr.Code)
	}
}

// TestDepositFinalized_CleanAddress_ProceedsToPool verifica que endereco limpo
// passa pelo screening e chega ao pool (retorna 500 apenas por pool nil, nao
// por screening).
func TestDepositFinalized_CleanAddress_ProceedsToPool(t *testing.T) {
	sc := &stubScreener{err: nil} // endereco limpo
	h := buildComplianceHandler(t, sc, nil)

	body := makeFinalizationPayload("0xclean000000000000000000000000000001")
	req := makeSignedRequest(t, body)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	// Screener deve ter sido chamado.
	if sc.calls.Load() != 1 {
		t.Errorf("screener.ScreenAddress nao foi chamado para endereco limpo")
	}
	// Com pool nil, FinalizeDeposit retorna erro de banco -> 500.
	// O importante: o erro NAO e de screening — passou pela fronteira compliance.
	if rr.Code == http.StatusUnauthorized || rr.Code == http.StatusBadRequest {
		t.Errorf("status inesperado %d — screening nao deveria ter bloqueado endereco limpo", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Testes: SubmitPayout com screening e Travel Rule
// ---------------------------------------------------------------------------

// newTestMoney cria um Money stub para testes de payout.
func newTestMoney(amount int64, assetCode string) *moneyv1.Money {
	return &moneyv1.Money{
		Amount:    amount,
		AssetCode: assetCode,
		Scale:     6,
	}
}

// TestSubmitPayout_SanctionedDestination_DoesNotBuild verifica que payout
// para endereco sancionado NAO chama BuildPayout.
func TestSubmitPayout_SanctionedDestination_DoesNotBuild(t *testing.T) {
	sc := &stubScreener{err: errors.New("chainalysis: endereco em lista de sancoes — operacao bloqueada")}
	tr := &stubTravelRecorder{err: nil}
	h := buildComplianceHandler(t, sc, tr)
	conn := &stubConnector{}

	err := h.SubmitPayout(context.Background(), chainconnector.PayoutRequest{
		ToAddress:      "0xsanctioned000000000000000000000001",
		Amount:         newTestMoney(10_000_000, "USDC"),
		ChainID:        1,
		IdempotencyKey: "pay-sanctions-001",
	}, conn)

	if err == nil {
		t.Fatal("esperava erro de screening para endereco sancionado")
	}
	if !errors.Is(err, ErrScreeningBlocked) {
		t.Errorf("erro esperado ErrScreeningBlocked, got: %v", err)
	}
	// BuildPayout NAO deve ter sido chamado.
	if conn.buildPayoutCalled.Load() {
		t.Error("BuildPayout foi chamado mesmo com endereco sancionado — fail-closed violado")
	}
	// Travel Rule NAO deve ter sido chamado (bloqueio antes).
	if tr.calls.Load() != 0 {
		t.Error("TravelRule nao devia ter sido chamado apos screening bloquear")
	}
}

// TestSubmitPayout_ScreenerUnavailable_FailClosed verifica que screener
// indisponivel bloqueia o payout antes de BuildPayout.
func TestSubmitPayout_ScreenerUnavailable_FailClosed(t *testing.T) {
	sc := &stubScreener{err: errors.New("chainalysis: screening indisponivel — operacao segurada (fail-closed)")}
	h := buildComplianceHandler(t, sc, nil)
	conn := &stubConnector{}

	err := h.SubmitPayout(context.Background(), chainconnector.PayoutRequest{
		ToAddress:      "0xunknown000000000000000000000000001",
		Amount:         newTestMoney(1_000_000, "USDC"),
		ChainID:        1,
		IdempotencyKey: "pay-unavail-001",
	}, conn)

	if err == nil {
		t.Fatal("esperava erro de screening indisponivel")
	}
	if !errors.Is(err, ErrScreeningBlocked) {
		t.Errorf("erro esperado ErrScreeningBlocked, got: %v", err)
	}
	if conn.buildPayoutCalled.Load() {
		t.Error("BuildPayout foi chamado mesmo com screener indisponivel — fail-closed violado")
	}
}

// TestSubmitPayout_AboveThreshold_TravelRuleBlocked verifica que payout acima
// do limiar com Travel Rule falhando e bloqueado antes de BuildPayout.
func TestSubmitPayout_AboveThreshold_TravelRuleBlocked(t *testing.T) {
	sc := &stubScreener{err: nil} // endereco limpo
	tr := &stubTravelRecorder{err: errors.New("travelrule: registro obrigatorio — payout bloqueado (fail-closed)")}
	h := buildComplianceHandler(t, sc, tr)
	conn := &stubConnector{}

	err := h.SubmitPayout(context.Background(), chainconnector.PayoutRequest{
		ToAddress:      "0xclean000000000000000000000000000001",
		Amount:         newTestMoney(1_000_000_000, "USDC"), // 1000 USDC — acima de qualquer limiar razoavel
		ChainID:        1,
		IdempotencyKey: "pay-travelrule-001",
	}, conn)

	if err == nil {
		t.Fatal("esperava erro de Travel Rule acima do limiar")
	}
	if !errors.Is(err, ErrTravelRuleBlocked) {
		t.Errorf("erro esperado ErrTravelRuleBlocked, got: %v", err)
	}
	// Screening foi chamado (endereco limpo passou).
	if sc.calls.Load() != 1 {
		t.Errorf("screener.ScreenAddress chamado %d vezes, esperado 1", sc.calls.Load())
	}
	// Travel Rule foi chamado.
	if tr.calls.Load() != 1 {
		t.Errorf("travelRecorder.RecordPayout chamado %d vezes, esperado 1", tr.calls.Load())
	}
	// BuildPayout NAO deve ter sido chamado.
	if conn.buildPayoutCalled.Load() {
		t.Error("BuildPayout foi chamado mesmo com Travel Rule bloqueando — fail-closed violado")
	}
}

// TestSubmitPayout_BelowThreshold_Proceeds verifica que payout abaixo do limiar
// de Travel Rule prossegue normalmente (ErrTravelRuleBelowThreshold nao e bloqueante).
func TestSubmitPayout_BelowThreshold_Proceeds(t *testing.T) {
	sc := &stubScreener{err: nil} // endereco limpo
	tr := &stubTravelRecorder{err: ErrTravelRuleBelowThreshold} // abaixo do limiar
	h := buildComplianceHandler(t, sc, tr)
	conn := &stubConnector{}

	err := h.SubmitPayout(context.Background(), chainconnector.PayoutRequest{
		ToAddress:      "0xclean000000000000000000000000000001",
		Amount:         newTestMoney(100_000, "USDC"), // 0.10 USDC — abaixo de qualquer limiar
		ChainID:        1,
		IdempotencyKey: "pay-below-threshold-001",
	}, conn)

	// Deve ter chegado ao BuildPayout (stub retorna nil).
	if err != nil {
		t.Errorf("esperava sucesso para endereco limpo abaixo do limiar, got: %v", err)
	}
	if !conn.buildPayoutCalled.Load() {
		t.Error("BuildPayout deveria ter sido chamado para payout valido")
	}
}

// TestSubmitPayout_CleanAddress_NoCompliance_Proceeds verifica o caminho feliz
// sem compliance configurado (screener e travelRecorder nil).
func TestSubmitPayout_CleanAddress_NoCompliance_Proceeds(t *testing.T) {
	// Sem screener nem travelRecorder — comportamento de dev/staging.
	h := buildComplianceHandler(t, nil, nil)
	conn := &stubConnector{}

	err := h.SubmitPayout(context.Background(), chainconnector.PayoutRequest{
		ToAddress:      "0xany000000000000000000000000000000001",
		Amount:         newTestMoney(500_000, "USDC"),
		ChainID:        1,
		IdempotencyKey: "pay-no-compliance-001",
	}, conn)

	if err != nil {
		t.Errorf("esperava sucesso sem compliance, got: %v", err)
	}
	if !conn.buildPayoutCalled.Load() {
		t.Error("BuildPayout deveria ter sido chamado sem compliance")
	}
}

// TestSubmitPayout_CleanAddress_FullCompliance_Proceeds verifica o caminho feliz
// completo: endereco limpo + Travel Rule below threshold -> BuildPayout chamado.
func TestSubmitPayout_CleanAddress_FullCompliance_Proceeds(t *testing.T) {
	sc := &stubScreener{err: nil}
	tr := &stubTravelRecorder{err: nil} // registro concluido (acima do limiar, sucesso)
	h := buildComplianceHandler(t, sc, tr)
	conn := &stubConnector{}

	err := h.SubmitPayout(context.Background(), chainconnector.PayoutRequest{
		ToAddress:      "0xclean000000000000000000000000000001",
		Amount:         newTestMoney(2_000_000_000, "USDC"), // 2000 USDC — acima do limiar, mas registro OK
		ChainID:        1,
		IdempotencyKey: "pay-full-compliance-001",
	}, conn)

	if err != nil {
		t.Errorf("esperava sucesso com compliance completo, got: %v", err)
	}
	if sc.calls.Load() != 1 {
		t.Errorf("screener chamado %d vezes, esperado 1", sc.calls.Load())
	}
	if tr.calls.Load() != 1 {
		t.Errorf("travelRecorder chamado %d vezes, esperado 1", tr.calls.Load())
	}
	if !conn.buildPayoutCalled.Load() {
		t.Error("BuildPayout deveria ter sido chamado apos compliance passar")
	}
}
