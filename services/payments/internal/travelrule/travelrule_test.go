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
	"strings"
	"testing"

	"github.com/hojex/adserver/services/payments/internal/kmsenvelope"
	"github.com/jackc/pgx/v5/pgconn"
)

// ---------------------------------------------------------------------------
// fakeDB — pool fake para testes de INSERT (LOW-2)
//
// Captura os argumentos passados ao Exec sem banco real.
// Implementa dbExecer (interface interna do pacote).
// ---------------------------------------------------------------------------

type fakeExecCall struct {
	sql  string
	args []any
}

type fakeDB struct {
	calls  []fakeExecCall
	errFor map[string]error // mapa sql-prefixo -> erro a retornar
}

func newFakeDB() *fakeDB {
	return &fakeDB{errFor: make(map[string]error)}
}

func (f *fakeDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.calls = append(f.calls, fakeExecCall{sql: sql, args: args})
	for prefix, err := range f.errFor {
		if strings.Contains(sql, prefix) {
			return pgconn.CommandTag{}, err
		}
	}
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

// ---------------------------------------------------------------------------
// Helpers de teste
// ---------------------------------------------------------------------------

func buildRecorder(threshold int64, transport VASPTransport) *Recorder {
	return New(Config{
		ThresholdMinorUnits: threshold,
		AssetCode:           "USDC",
	}, nil, transport, nil, nil) // pool nil: sem banco; envelope nil: sem INSERT de PII em testes unitarios
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

// ---------------------------------------------------------------------------
// Testes do envelope KMS: cifra de PII em insertRecord
//
// Usa pool=nil (sem banco real), portanto insertRecord retorna nil antes de
// chegar ao SQL — os testes abaixo verificam a logica de validacao do envelope
// e que o campo AmountMinorUnits nao passa pelo caminho de cifra (TX-2).
// ---------------------------------------------------------------------------

// buildRecorderWithEnvelope cria um Recorder com envelope KMS de teste.
func buildRecorderWithEnvelope(threshold int64, transport VASPTransport) *Recorder {
	env := kmsenvelope.NewForTest()
	return New(Config{
		ThresholdMinorUnits: threshold,
		AssetCode:           "USDC",
	}, nil, transport, env, nil)
}

// TestRecord_EnvelopeNil_PoolNil_NoError verifica que envelope=nil com pool=nil
// nao bloqueia (sem banco, o INSERT nunca ocorre — comportamento de teste puro).
func TestRecord_EnvelopeNil_PoolNil_NoError(t *testing.T) {
	rt := &recordingTransport{}
	r := buildRecorder(0, rt) // envelope nil, pool nil
	p := validParams()

	err := r.Record(context.Background(), p)
	if err != nil {
		t.Fatalf("pool nil + envelope nil: esperava nil, got %v", err)
	}
}

// TestRecord_WithEnvelope_AboveThreshold_Succeeds verifica que um Recorder com
// envelope configurado processa normalmente (pool nil => insertRecord retorna nil).
func TestRecord_WithEnvelope_AboveThreshold_Succeeds(t *testing.T) {
	rt := &recordingTransport{}
	r := buildRecorderWithEnvelope(0, rt)
	p := validParams()

	err := r.Record(context.Background(), p)
	if err != nil {
		t.Fatalf("com envelope, acima do limiar: esperava nil, got %v", err)
	}
	if len(rt.calls) != 1 {
		t.Errorf("transport nao foi chamado: calls=%d", len(rt.calls))
	}
}

// TestRecord_AmountMinorUnits_NotEncrypted verifica que AmountMinorUnits
// nao passa pelo envelope (invariante TX-2: cifra somente sobre campos TEXT de nome).
// Como pool=nil, insertRecord retorna nil sem executar SQL — o teste verifica
// que o campo permanece int64 intocado no RecordParams.
func TestRecord_AmountMinorUnits_NotEncrypted(t *testing.T) {
	rt := &recordingTransport{}
	r := buildRecorderWithEnvelope(0, rt)
	p := validParams()
	p.AmountMinorUnits = 9_999_999_999 // valor que float64 nao representa exatamente

	err := r.Record(context.Background(), p)
	if err != nil {
		t.Fatalf("esperava nil, got %v", err)
	}
	// O transport recebe o RecordParams original — AmountMinorUnits deve ser exato.
	if len(rt.calls) != 1 {
		t.Fatalf("transport nao chamado")
	}
	if rt.calls[0].AmountMinorUnits != 9_999_999_999 {
		t.Errorf("TX-2 violado: AmountMinorUnits alterado — quero 9999999999, got %d",
			rt.calls[0].AmountMinorUnits)
	}
}

// ---------------------------------------------------------------------------
// LOW-2: testes com fakeDB — prova ciphertext no INSERT e ramo fail-closed
// ---------------------------------------------------------------------------

// TestInsertRecord_CiphertextNotPlaintext verifica que originator_name e
// beneficiary_name enviados ao SQL sao ciphertext, nunca plaintext PII (LOW-2-a).
func TestInsertRecord_CiphertextNotPlaintext(t *testing.T) {
	env := kmsenvelope.NewForTest()
	db := newFakeDB()
	rt := &recordingTransport{}
	r := newWithDB(Config{ThresholdMinorUnits: 0, AssetCode: "USDC"}, db, rt, env, nil)

	p := validParams()
	p.OriginatorName = "Joao da Silva Santos"  // PII - deve chegar cifrado ao SQL
	p.BeneficiaryName = "Maria Oliveira Costa" // PII - deve chegar cifrado ao SQL

	err := r.Record(context.Background(), p)
	if err != nil {
		t.Fatalf("Record com fakeDB: esperava nil, got %v", err)
	}

	// Localiza a chamada INSERT em travel_rule_records
	var insertCall *fakeExecCall
	for i := range db.calls {
		if strings.Contains(db.calls[i].sql, "travel_rule_records") {
			insertCall = &db.calls[i]
			break
		}
	}
	if insertCall == nil {
		t.Fatal("fakeDB nao recebeu INSERT em travel_rule_records")
	}

	// Posicoes dos args: $1=tenant_id, $2=tx_ref, $3=originator_vasp,
	// $4=beneficiary_vasp, $5=originator_name, $6=beneficiary_name, $7=asset_code, $8=amount
	// (indices 0-based: originator_name=4, beneficiary_name=5)
	if len(insertCall.args) < 6 {
		t.Fatalf("INSERT com poucos argumentos: got %d", len(insertCall.args))
	}

	// originator_name: deve ser *string com ciphertext (nunca plaintext)
	originatorPtr, ok := insertCall.args[4].(*string)
	if !ok || originatorPtr == nil {
		t.Fatalf("originator_name arg nao e *string nao-nil: %T", insertCall.args[4])
	}
	if *originatorPtr == p.OriginatorName {
		t.Errorf("VIOLACAO LOW-2: originator_name enviado em plaintext ao SQL")
	}
	if strings.Contains(*originatorPtr, p.OriginatorName) {
		t.Errorf("VIOLACAO LOW-2: originator_name contem o plaintext no valor enviado ao SQL")
	}
	if !strings.HasPrefix(*originatorPtr, "v1$") {
		t.Errorf("originator_name no SQL sem prefixo v1$: %q", (*originatorPtr)[:min2(len(*originatorPtr), 10)])
	}

	// beneficiary_name: mesma verificacao
	beneficiaryPtr, ok := insertCall.args[5].(*string)
	if !ok || beneficiaryPtr == nil {
		t.Fatalf("beneficiary_name arg nao e *string nao-nil: %T", insertCall.args[5])
	}
	if *beneficiaryPtr == p.BeneficiaryName {
		t.Errorf("VIOLACAO LOW-2: beneficiary_name enviado em plaintext ao SQL")
	}
	if strings.Contains(*beneficiaryPtr, p.BeneficiaryName) {
		t.Errorf("VIOLACAO LOW-2: beneficiary_name contem o plaintext no valor enviado ao SQL")
	}

	// TX-2: amount_minor_units (args[7]) nao deve passar pelo envelope
	if len(insertCall.args) < 8 {
		t.Fatalf("INSERT sem arg de amount: %d args", len(insertCall.args))
	}
	if _, isInt := insertCall.args[7].(int64); !isInt {
		t.Errorf("TX-2: amount_minor_units nao e int64 no SQL - got %T", insertCall.args[7])
	}
}

// TestInsertRecord_FailClosed_EnvelopeNilWithPII verifica que envelope==nil
// com PII presente retorna ErrTravelRuleRequired e bloqueia o INSERT (LOW-2-b).
func TestInsertRecord_FailClosed_EnvelopeNilWithPII(t *testing.T) {
	db := newFakeDB()
	rt := &recordingTransport{}
	// envelope nil: misconfiguracao de producao
	r := newWithDB(Config{ThresholdMinorUnits: 0, AssetCode: "USDC"}, db, rt, nil, nil)

	p := validParams()
	p.OriginatorName = "Joao da Silva Santos" // PII: exige envelope

	err := r.Record(context.Background(), p)
	if err == nil {
		t.Fatal("VIOLACAO fail-closed: esperava erro quando envelope=nil e PII presente, got nil")
	}
	if !errors.Is(err, ErrTravelRuleRequired) {
		t.Errorf("esperava ErrTravelRuleRequired, got: %v", err)
	}
	// Garante que nenhum plaintext chegou ao SQL
	for _, call := range db.calls {
		for _, arg := range call.args {
			if s, ok := arg.(string); ok && s == p.OriginatorName {
				t.Errorf("VIOLACAO DA-11: plaintext PII chegou ao SQL com envelope nil")
			}
		}
	}
}

// TestInsertRecord_AmountNotEncrypted_FakeDB e um teste TX-2 explicitamente
// via fakeDB: amount_minor_units nunca passa pelo envelope KMS.
func TestInsertRecord_AmountNotEncrypted_FakeDB(t *testing.T) {
	env := kmsenvelope.NewForTest()
	db := newFakeDB()
	rt := &recordingTransport{}
	r := newWithDB(Config{ThresholdMinorUnits: 0, AssetCode: "USDC"}, db, rt, env, nil)

	p := validParams()
	p.AmountMinorUnits = 9_876_543_210 // valor que float64 nao representa exatamente

	if err := r.Record(context.Background(), p); err != nil {
		t.Fatalf("Record: %v", err)
	}

	var insertCall *fakeExecCall
	for i := range db.calls {
		if strings.Contains(db.calls[i].sql, "travel_rule_records") {
			insertCall = &db.calls[i]
			break
		}
	}
	if insertCall == nil {
		t.Fatal("fakeDB nao recebeu INSERT")
	}
	if len(insertCall.args) < 8 {
		t.Fatalf("poucos args: %d", len(insertCall.args))
	}
	amount, ok := insertCall.args[7].(int64)
	if !ok {
		t.Fatalf("TX-2: amount_minor_units nao e int64 no SQL - got %T", insertCall.args[7])
	}
	if amount != 9_876_543_210 {
		t.Errorf("TX-2: amount_minor_units alterado - quero 9876543210, got %d", amount)
	}
}

// min2 e um helper local para limitar indices em msgs de erro.
func min2(a, b int) int {
	if a < b {
		return a
	}
	return b
}
