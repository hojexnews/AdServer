// Package travelrule — travelrule_test.go
//
// Testes unitarios do Recorder de Travel Rule sem banco real e sem VASP real.
//
// Casos cobertos:
//   - Abaixo do limiar: retorna ErrBelowThreshold (nao bloqueante)
//   - Acima do limiar: chama transport e grava no cofre (stub pool=nil)
//   - Falha no transport: retorna ErrTravelRuleRequired (bloqueante)
//   - Limiar zero: sempre acima (comportamento conservador)
//   - TenantID vazio: retorna erro
//   - TxRef vazio: retorna erro
//   - AmountMinorUnits negativo: retorna erro
//   - Float PROIBIDO: todos os valores sao int64/int (TX-2)
//   - PII nunca loga: RecordParams com nomes nao causa panic
//   - StubVASPTransport retorna 'sent' sem envio real
//   - AboveThreshold: logica de limiar correta
package travelrule

import (
	"context"
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// Helpers de teste
// ---------------------------------------------------------------------------

func buildRecorder(threshold int64, transport VASPTransport) *Recorder {
	return New(Config{
		ThresholdMinorUnits: threshold,
		AssetCode:           "USDC",
	}, nil, transport, nil) // pool nil: sem banco em testes unitarios
}

// errorTransport e um VASPTransport que sempre falha.
type errorTransport struct{ err error }

func (e errorTransport) Send(_ context.Context, _ RecordParams) (string, error) {
	return "", e.err
}

// recordingTransport registra as chamadas para verificacao.
type recordingTransport struct {
	calls []RecordParams
}

func (rt *recordingTransport) Send(_ context.Context, p RecordParams) (string, error) {
	rt.calls = append(rt.calls, p)
	return "sent", nil
}

func validParams() RecordParams {
	return RecordParams{
		TenantID:         "aaaaaaaa-0000-0000-0000-000000000001",
		TxRef:            "payout_001",
		OriginatorVASP:   "SAFE_PLATFORM",
		BeneficiaryVASP:  "EXCHANGE_ABC",
		OriginatorName:   "Joao Silva",   // PII — cifrar em producao; aceito em testes
		BeneficiaryName:  "Jane Doe",     // PII — cifrar em producao; aceito em testes
		AmountMinorUnits: 1_000_000_000, // 1000 USDC (scale=6) em minor-units — int64, sem float
		AssetCode:        "USDC",
		Direction:        DirectionOutbound,
	}
}

// ---------------------------------------------------------------------------
// Testes de validacao de parametros
// ---------------------------------------------------------------------------

func TestRecord_EmptyTenantID(t *testing.T) {
	r := buildRecorder(0, nil)
	p := validParams()
	p.TenantID = ""
	err := r.Record(context.Background(), p)
	if err == nil {
		t.Fatal("esperava erro para TenantID vazio")
	}
}

func TestRecord_EmptyTxRef(t *testing.T) {
	r := buildRecorder(0, nil)
	p := validParams()
	p.TxRef = ""
	err := r.Record(context.Background(), p)
	if err == nil {
		t.Fatal("esperava erro para TxRef vazio")
	}
}

func TestRecord_NegativeAmount(t *testing.T) {
	r := buildRecorder(0, nil)
	p := validParams()
	p.AmountMinorUnits = -1
	err := r.Record(context.Background(), p)
	if err == nil {
		t.Fatal("esperava erro para AmountMinorUnits negativo")
	}
}

// ---------------------------------------------------------------------------
// Testes de limiar
// ---------------------------------------------------------------------------

func TestRecord_BelowThreshold(t *testing.T) {
	// Limiar de 2_000_000_000 (2000 USDC); valor enviado = 500_000_000 (500 USDC)
	// Ambos int64 — TX-2: sem float.
	r := buildRecorder(2_000_000_000, nil)
	p := validParams()
	p.AmountMinorUnits = 500_000_000

	err := r.Record(context.Background(), p)
	if !errors.Is(err, ErrBelowThreshold) {
		t.Errorf("abaixo do limiar: esperava ErrBelowThreshold, got %v", err)
	}
}

func TestRecord_AboveThreshold_StubTransport(t *testing.T) {
	// Acima do limiar: deve chamar o transport (stub) e retornar nil (sucesso).
	rt := &recordingTransport{}
	r := buildRecorder(500_000_000, rt) // limiar = 500 USDC
	p := validParams()
	p.AmountMinorUnits = 1_000_000_000 // 1000 USDC — acima do limiar

	err := r.Record(context.Background(), p)
	if err != nil {
		t.Fatalf("acima do limiar com stub: esperava nil, got %v", err)
	}
	if len(rt.calls) != 1 {
		t.Errorf("transport nao foi chamado: calls=%d", len(rt.calls))
	}
}

func TestRecord_ZeroThreshold_AlwaysAbove(t *testing.T) {
	// Limiar zero: todas as transferencias devem ser registradas (comportamento conservador).
	rt := &recordingTransport{}
	r := buildRecorder(0, rt)
	p := validParams()
	p.AmountMinorUnits = 1 // minimo possivel — ainda deve registrar

	err := r.Record(context.Background(), p)
	if err != nil {
		t.Fatalf("limiar zero: esperava nil, got %v", err)
	}
	if len(rt.calls) != 1 {
		t.Errorf("transport nao foi chamado com limiar zero: calls=%d", len(rt.calls))
	}
}

func TestRecord_TransportError_Fail_Closed(t *testing.T) {
	// Falha no transport: deve retornar ErrTravelRuleRequired (bloqueante).
	sendErr := errors.New("vasp: connection refused")
	r := buildRecorder(0, errorTransport{err: sendErr})
	p := validParams()

	err := r.Record(context.Background(), p)
	if !errors.Is(err, ErrTravelRuleRequired) {
		t.Errorf("falha no transport: esperava ErrTravelRuleRequired, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Testes de AboveThreshold
// ---------------------------------------------------------------------------

func TestAboveThreshold_Logic(t *testing.T) {
	cases := []struct {
		threshold int64
		amount    int64
		want      bool
	}{
		{0, 1, true},           // limiar zero = sempre acima
		{0, 0, true},           // limiar zero = sempre acima (mesmo zero)
		{1000, 999, false},     // abaixo
		{1000, 1000, true},     // igual = acima (>= limiar)
		{1000, 1001, true},     // acima
		{1_000_000, 999_999, false},
		{1_000_000, 1_000_001, true},
	}
	for _, tc := range cases {
		r := buildRecorder(tc.threshold, nil)
		got := r.AboveThreshold(tc.amount)
		if got != tc.want {
			t.Errorf("AboveThreshold(threshold=%d, amount=%d): got %v, want %v",
				tc.threshold, tc.amount, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Teste: PII nunca causa panic (nomes sao aceitos mas nao logados)
// ---------------------------------------------------------------------------

func TestRecord_PIIFieldsAccepted_NoPanic(t *testing.T) {
	// Nomes PII podem ser passados em params — nao devem causar panic.
	// Em producao, seriam cifrados pela camada de aplicacao antes do Insert.
	rt := &recordingTransport{}
	r := buildRecorder(0, rt)
	p := validParams()
	p.OriginatorName = "Joao da Silva Santos"  // PII — cifrar em producao
	p.BeneficiaryName = "Maria Oliveira Costa" // PII — cifrar em producao

	// Nao deve panic
	err := r.Record(context.Background(), p)
	if err != nil {
		t.Fatalf("PII aceita sem panic: esperava nil, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Teste: StubVASPTransport retorna 'sent'
// ---------------------------------------------------------------------------

func TestStubVASPTransport(t *testing.T) {
	var stub VASPTransport = StubVASPTransport{}
	status, err := stub.Send(context.Background(), validParams())
	if err != nil {
		t.Fatalf("StubVASPTransport.Send: %v", err)
	}
	if status != "sent" {
		t.Errorf("StubVASPTransport.Send: status=%q, want 'sent'", status)
	}
}

// ---------------------------------------------------------------------------
// Invariante TX-2: AmountMinorUnits e int64 (sem float)
// ---------------------------------------------------------------------------

func TestRecord_AmountIsInt64_NoFloat(t *testing.T) {
	// Verifica que o tipo de AmountMinorUnits e int64 — nenhum float permitido.
	// Este teste documenta a invariante TX-2 no nivel de tipo Go.
	p := validParams()
	var _ int64 = p.AmountMinorUnits // compilacao falha se float

	// Valor extremo: 1 satoshi ou 1 minor-unit de USDC
	p.AmountMinorUnits = 1
	var _ int64 = p.AmountMinorUnits

	// Valor maximo razoavel: 10^15 minor-units (1 bilhao de USDC com scale=6)
	p.AmountMinorUnits = 1_000_000_000_000_000
	var _ int64 = p.AmountMinorUnits
}
