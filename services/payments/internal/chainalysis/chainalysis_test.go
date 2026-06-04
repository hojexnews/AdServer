// Package chainalysis — chainalysis_test.go
//
// Testes unitarios do Screener Chainalysis sem banco real e sem API key viva.
// Usa httptest.Server para simular a API Chainalysis.
//
// Casos cobertos:
//   - Construcao com parametros validos/invalidos
//   - ScreenAddress: sanctions_hit=true -> ErrSanctionsHit (fail-closed)
//   - ScreenAddress: risco acima do limiar -> ErrRiskAboveThreshold (fail-closed)
//   - ScreenAddress: risco abaixo do limiar -> nil (liberado)
//   - ScreenAddress: API indisponivel -> ErrScreeningUnavailable (fail-closed)
//   - ScreenAddress: endereco nao encontrado (404) -> baixo risco (liberado)
//   - mapRiskStringToScore: mapeamento low/medium/high/severe/unknown
//   - Float PROIBIDO: RiskScore e RiskThreshold sao int (TX-2)
//   - PII nunca loga: nao ha campo PII no ScreenAddressParams
//   - Direction invalida: retorna erro
package chainalysis

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---------------------------------------------------------------------------
// Helpers de teste
// ---------------------------------------------------------------------------

// buildTestScreener cria um Screener apontando para o server de teste.
// pool=nil: sem banco (persistResult e no-op).
func buildTestScreener(t *testing.T, srv *httptest.Server, threshold int) *Screener {
	t.Helper()
	s, err := New(Config{
		APIKey:        "test-api-key",
		BaseURL:       srv.URL,
		RiskThreshold: threshold,
	}, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// makeChainalysisServer cria um httptest.Server que retorna a resposta simulada
// do Chainalysis KYT v2.
func makeChainalysisServer(risk, cluster string, statusCode int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(statusCode)
		if statusCode == http.StatusOK {
			resp := map[string]string{
				"address": "0xTEST",
				"risk":    risk,
				"cluster": cluster,
			}
			_ = json.NewEncoder(w).Encode(resp)
		}
	}))
}

// validScreenParams retorna ScreenAddressParams valido para testes.
func validScreenParams(address string) ScreenAddressParams {
	return ScreenAddressParams{
		TenantID:  "aaaaaaaa-0000-0000-0000-000000000001",
		Address:   address,
		Direction: DirectionDeposit,
		TxRef:     "tx_test_001",
		Network:   "ethereum",
	}
}

// ---------------------------------------------------------------------------
// Testes de construcao
// ---------------------------------------------------------------------------

func TestNew_RequiresAPIKey(t *testing.T) {
	_, err := New(Config{APIKey: "", RiskThreshold: 70}, nil, nil)
	if err == nil {
		t.Fatal("esperava erro para APIKey vazio")
	}
}

func TestNew_OK(t *testing.T) {
	srv := makeChainalysisServer("low", "none", http.StatusOK)
	defer srv.Close()

	s, err := New(Config{APIKey: "key", BaseURL: srv.URL, RiskThreshold: 70}, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if s == nil {
		t.Fatal("screener nao deve ser nil")
	}
}

func TestNew_DefaultThreshold(t *testing.T) {
	srv := makeChainalysisServer("low", "none", http.StatusOK)
	defer srv.Close()

	// RiskThreshold 0 deve usar default 70
	s, err := New(Config{APIKey: "key", BaseURL: srv.URL, RiskThreshold: 0}, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if s.riskThreshold != 70 {
		t.Errorf("threshold default: got %d, want 70", s.riskThreshold)
	}
}

// ---------------------------------------------------------------------------
// Testes de ScreenAddress
// ---------------------------------------------------------------------------

func TestScreenAddress_LowRisk_Libera(t *testing.T) {
	srv := makeChainalysisServer("low", "none", http.StatusOK)
	defer srv.Close()
	s := buildTestScreener(t, srv, 70)

	err := s.ScreenAddress(context.Background(), validScreenParams("0xLOW"))
	if err != nil {
		t.Errorf("baixo risco: esperava nil, got %v", err)
	}
}

func TestScreenAddress_HighRisk_Bloqueia(t *testing.T) {
	// high = score 80; limiar = 70 -> bloqueia
	srv := makeChainalysisServer("high", "darknet_market", http.StatusOK)
	defer srv.Close()
	s := buildTestScreener(t, srv, 70)

	err := s.ScreenAddress(context.Background(), validScreenParams("0xHIGH"))
	if !errors.Is(err, ErrRiskAboveThreshold) {
		t.Errorf("alto risco: esperava ErrRiskAboveThreshold, got %v", err)
	}
}

func TestScreenAddress_Sanctions_Bloqueia(t *testing.T) {
	// Categoria "sanctions" -> ErrSanctionsHit independente do score
	srv := makeChainalysisServer("severe", "sanctions", http.StatusOK)
	defer srv.Close()
	s := buildTestScreener(t, srv, 70)

	err := s.ScreenAddress(context.Background(), validScreenParams("0xSANCTIONED"))
	if !errors.Is(err, ErrSanctionsHit) {
		t.Errorf("sancoes: esperava ErrSanctionsHit, got %v", err)
	}
}

func TestScreenAddress_Unavailable_FailClosed(t *testing.T) {
	// API retorna 500 -> fail-closed: ErrScreeningUnavailable
	srv := makeChainalysisServer("", "", http.StatusInternalServerError)
	defer srv.Close()
	s := buildTestScreener(t, srv, 70)

	err := s.ScreenAddress(context.Background(), validScreenParams("0xUNAVAIL"))
	if !errors.Is(err, ErrScreeningUnavailable) {
		t.Errorf("indisponivel: esperava ErrScreeningUnavailable, got %v", err)
	}
}

func TestScreenAddress_NotFound_BaixoRisco(t *testing.T) {
	// 404 -> endereco sem historico = baixo risco = liberado
	srv := makeChainalysisServer("", "", http.StatusNotFound)
	defer srv.Close()
	s := buildTestScreener(t, srv, 70)

	err := s.ScreenAddress(context.Background(), validScreenParams("0xNEW"))
	if err != nil {
		t.Errorf("nao encontrado (404): esperava nil (baixo risco), got %v", err)
	}
}

func TestScreenAddress_EmptyAddress(t *testing.T) {
	srv := makeChainalysisServer("low", "none", http.StatusOK)
	defer srv.Close()
	s := buildTestScreener(t, srv, 70)

	p := validScreenParams("")
	err := s.ScreenAddress(context.Background(), p)
	if err == nil {
		t.Fatal("esperava erro para Address vazio")
	}
}

func TestScreenAddress_EmptyTenantID(t *testing.T) {
	srv := makeChainalysisServer("low", "none", http.StatusOK)
	defer srv.Close()
	s := buildTestScreener(t, srv, 70)

	p := validScreenParams("0xTEST")
	p.TenantID = ""
	err := s.ScreenAddress(context.Background(), p)
	if err == nil {
		t.Fatal("esperava erro para TenantID vazio")
	}
}

func TestScreenAddress_DirectionInvalida(t *testing.T) {
	srv := makeChainalysisServer("low", "none", http.StatusOK)
	defer srv.Close()
	s := buildTestScreener(t, srv, 70)

	p := validScreenParams("0xTEST")
	p.Direction = "invalid_direction"
	err := s.ScreenAddress(context.Background(), p)
	if err == nil {
		t.Fatal("esperava erro para Direction invalida")
	}
}

// ---------------------------------------------------------------------------
// Testes de mapeamento de score
// ---------------------------------------------------------------------------

func TestMapRiskStringToScore(t *testing.T) {
	// TX-2: todos os valores retornados sao int (sem float)
	cases := []struct {
		risk string
		want int
	}{
		{"low", 10},
		{"LOW", 10},     // case-insensitive
		{"medium", 50},
		{"high", 80},
		{"severe", 100},
		{"unknown", 0},
		{"", 0},
	}
	for _, tc := range cases {
		got := mapRiskStringToScore(tc.risk)
		if got != tc.want {
			t.Errorf("mapRiskStringToScore(%q) = %d, want %d", tc.risk, got, tc.want)
		}
		// Verifica que o tipo e int (TX-2: sem float)
		var _ int = got
	}
}

// ---------------------------------------------------------------------------
// Invariante TX-2: RiskScore e RiskThreshold sao int (sem float)
// ---------------------------------------------------------------------------

func TestRiskScore_IsInt_NoFloat(t *testing.T) {
	// Verifica estaticamente que ScreeningResult.RiskScore e int.
	result := ScreeningResult{RiskScore: 80}
	var _ int = result.RiskScore // compilacao falha se float

	cfg := Config{APIKey: "k", RiskThreshold: 70}
	var _ int = cfg.RiskThreshold // compilacao falha se float
}

// ---------------------------------------------------------------------------
// Teste: Payout direction funciona alem de Deposit
// ---------------------------------------------------------------------------

func TestScreenAddress_PayoutDirection(t *testing.T) {
	srv := makeChainalysisServer("low", "none", http.StatusOK)
	defer srv.Close()
	s := buildTestScreener(t, srv, 70)

	p := validScreenParams("0xDEST")
	p.Direction = DirectionPayout
	err := s.ScreenAddress(context.Background(), p)
	if err != nil {
		t.Errorf("payout direction: esperava nil, got %v", err)
	}
}
