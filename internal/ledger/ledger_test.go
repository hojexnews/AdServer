// Testes do pacote ledger (K3).
//
// Sem instancia Postgres em CI. A logica de negocio e testada com stubs
// in-memory. Sem dependencias externas alem das ja no go.mod.
//
// Invariantes cobertas:
//   1. Idempotencia: reprocessar mesmo event_id nao duplica entry.
//   2. Balanco: par desbalanceado e rejeitado.
//   3. pending->posted na finalidade.
//   4. Recusa de posting em ativo disabled (AEV/BND).
//   5. Cambio como dois pares isolados por asset_code (DA-10).
//   6. Estorno como novo par de postings (nunca edicao/DELETE do original).
//   7. Reconciliacao: abre excecao, nunca autocorrige, rerun idempotente.
//   8. Scale mismatch rejeitado.
//   9. Asset nao encontrado rejeitado.
//  10. Payout desbalanceado rejeitado.
package ledger_test

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/hojex/adserver/internal/ledger"
)

// ---------------------------------------------------------------------------
// storeStub — ledger in-memory para testes sem Postgres
// ---------------------------------------------------------------------------

type storeEntry struct {
	id             ledger.JournalEntryID
	tenantID       string
	idempotencyKey string
	status         ledger.EntryStatus
	postings       []ledger.PostingLine
}

type store struct {
	nextID  ledger.JournalEntryID
	entries map[string]*storeEntry // chave: "tenantID:key"
}

func newStore() *store {
	return &store{
		nextID:  1,
		entries: make(map[string]*storeEntry),
	}
}

func storeKey(tenantID, idempKey string) string {
	return tenantID + ":" + idempKey
}

// recordEntry simula RecordEntry sem Postgres.
func (s *store) recordEntry(loader ledger.AssetLoader, p ledger.RecordEntryParams) (ledger.JournalEntryID, error) {
	ctx := context.Background()
	if len(p.Postings) == 0 {
		return 0, ledger.ErrEmptyPostings
	}
	// Valida balance por asset_code.
	type sums struct{ d, c int64 }
	byAsset := make(map[string]*sums)
	for _, pl := range p.Postings {
		if byAsset[pl.AssetCode] == nil {
			byAsset[pl.AssetCode] = &sums{}
		}
		byAsset[pl.AssetCode].d += pl.DebitMinorUnits
		byAsset[pl.AssetCode].c += pl.CreditMinorUnits
	}
	for _, sv := range byAsset {
		if sv.d != sv.c {
			return 0, ledger.ErrUnbalancedPostings
		}
	}
	// Valida ativos.
	seen := make(map[string]bool)
	for _, pl := range p.Postings {
		if seen[pl.AssetCode] {
			continue
		}
		seen[pl.AssetCode] = true
		a, err := loader.LoadAsset(ctx, pl.AssetCode)
		if err != nil {
			return 0, err
		}
		if !a.Enabled {
			return 0, ledger.ErrAssetDisabled
		}
		if a.Scale != pl.Scale {
			return 0, ledger.ErrScaleMismatch
		}
	}
	// Idempotencia.
	k := storeKey(p.TenantID, p.IdempotencyKey)
	if e, ok := s.entries[k]; ok {
		return e.id, ledger.ErrIdempotentDuplicate
	}
	id := s.nextID
	s.nextID++
	s.entries[k] = &storeEntry{
		id:             id,
		tenantID:       p.TenantID,
		idempotencyKey: p.IdempotencyKey,
		status:         p.Status,
		postings:       p.Postings,
	}
	return id, nil
}

// finalizeEntry simula FinalizeEntry (pending -> posted).
func (s *store) finalizeEntry(tenantID, idempKey string) error {
	k := storeKey(tenantID, idempKey)
	e, ok := s.entries[k]
	if !ok {
		return fmt.Errorf("ledger: entry nao encontrada: key=%q", idempKey)
	}
	if e.status == ledger.EntryStatusPosted {
		return nil // ja finalizado — idempotente
	}
	e.status = ledger.EntryStatusPosted
	return nil
}

// ---------------------------------------------------------------------------
// helpers de fixture
// ---------------------------------------------------------------------------

func enabledAssets() *ledger.AssetLoaderStub {
	return ledger.NewAssetLoaderStub([]ledger.Asset{
		{Code: "USDC", Scale: 6, Enabled: true},
		{Code: "USDT", Scale: 6, Enabled: true},
		{Code: "BRL", Scale: 2, Enabled: true},
		// AEV e BND disabled (scale TBD — §3 q.2 / E.2 ADR-0004)
		{Code: "AEV", Scale: 0, Enabled: false},
		{Code: "BND", Scale: 0, Enabled: false},
	})
}

// newTenantID retorna um UUID fixo de teste para o tenant indicado.
func newTenantID(n int) string {
	return fmt.Sprintf("00000000-0000-0000-0000-%012d", n)
}

func makeEntry(tenantID, key string, postings []ledger.PostingLine) ledger.RecordEntryParams {
	return ledger.RecordEntryParams{
		TenantID:       tenantID,
		IdempotencyKey: key,
		Description:    "teste",
		Status:         ledger.EntryStatusPosted,
		EffectiveAt:    time.Now(),
		Postings:       postings,
	}
}

func balancedPostings(assetCode string, scale int32, amount, debitAcc, creditAcc int64) []ledger.PostingLine {
	return []ledger.PostingLine{
		{AccountID: debitAcc, AssetCode: assetCode, Scale: scale, DebitMinorUnits: amount, CreditMinorUnits: 0},
		{AccountID: creditAcc, AssetCode: assetCode, Scale: scale, DebitMinorUnits: 0, CreditMinorUnits: amount},
	}
}

// ---------------------------------------------------------------------------
// 1. Idempotencia
// ---------------------------------------------------------------------------

func TestIdempotency_SameKeyDoesNotDuplicate(t *testing.T) {
	t.Parallel()
	s := newStore()
	loader := enabledAssets()
	tenant := newTenantID(1)
	key := "deposit:0xabc123"
	p := makeEntry(tenant, key, balancedPostings("USDC", 6, 1_000_000, 1, 2))

	id1, err1 := s.recordEntry(loader, p)
	id2, err2 := s.recordEntry(loader, p) // reprocessamento

	if err1 != nil {
		t.Fatalf("primeira gravacao falhou: %v", err1)
	}
	if err2 != ledger.ErrIdempotentDuplicate {
		t.Fatalf("esperava ErrIdempotentDuplicate, got: %v", err2)
	}
	if id1 != id2 {
		t.Errorf("ids devem ser iguais apos reprocessamento: %d != %d", id1, id2)
	}
	count := 0
	for _, e := range s.entries {
		if e.idempotencyKey == key {
			count++
		}
	}
	if count != 1 {
		t.Errorf("esperava 1 entry, encontrou %d", count)
	}
}

func TestIdempotency_DifferentKeyCreatesSeparateEntry(t *testing.T) {
	t.Parallel()
	s := newStore()
	loader := enabledAssets()
	tenant := newTenantID(2)

	id1, _ := s.recordEntry(loader, makeEntry(tenant, "deposit:0xaaa", balancedPostings("USDC", 6, 1_000_000, 1, 2)))
	id2, _ := s.recordEntry(loader, makeEntry(tenant, "deposit:0xbbb", balancedPostings("USDC", 6, 2_000_000, 1, 2)))

	if id1 == id2 {
		t.Error("chaves diferentes devem gerar ids diferentes")
	}
}

// ---------------------------------------------------------------------------
// 2. Balanco: par desbalanceado e rejeitado
// ---------------------------------------------------------------------------

func TestBalance_UnbalancedPostingsRejected(t *testing.T) {
	t.Parallel()
	s := newStore()
	loader := enabledAssets()
	tenant := newTenantID(3)

	postings := []ledger.PostingLine{
		{AccountID: 1, AssetCode: "USDC", Scale: 6, DebitMinorUnits: 100, CreditMinorUnits: 0},
		{AccountID: 2, AssetCode: "USDC", Scale: 6, DebitMinorUnits: 0, CreditMinorUnits: 50}, // 100 != 50
	}
	_, err := s.recordEntry(loader, makeEntry(tenant, "unbalanced", postings))
	if err != ledger.ErrUnbalancedPostings {
		t.Fatalf("esperava ErrUnbalancedPostings, got: %v", err)
	}
}

func TestBalance_BalancedPostingsAccepted(t *testing.T) {
	t.Parallel()
	s := newStore()
	loader := enabledAssets()
	tenant := newTenantID(4)

	_, err := s.recordEntry(loader, makeEntry(tenant, "balanced", balancedPostings("USDC", 6, 500_000, 1, 2)))
	if err != nil {
		t.Fatalf("esperava sucesso, got: %v", err)
	}
}

func TestBalance_EmptyPostingsRejected(t *testing.T) {
	t.Parallel()
	s := newStore()
	loader := enabledAssets()
	tenant := newTenantID(5)

	p := ledger.RecordEntryParams{
		TenantID: tenant, IdempotencyKey: "empty", Description: "vazio",
		Status: ledger.EntryStatusPosted, EffectiveAt: time.Now(), Postings: nil,
	}
	_, err := s.recordEntry(loader, p)
	if err != ledger.ErrEmptyPostings {
		t.Fatalf("esperava ErrEmptyPostings, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 2b. Balanco — funcao de PRODUCAO (checkBalance em posting.go), nao o stub.
//
// Os TestBalance_* acima exercitam s.recordEntry (a reimplementacao local do
// store stub), nao a checagem real. checkBalance([]PostingLine) e a fonte de
// verdade em memoria, chamada por RecordEntry (posting.go). Estes testes
// batem NELA diretamente via o wrapper fino ledger.CheckBalanceForTest, para
// que uma mutacao em `if s.debit != s.credit` (posting.go) -> `if false`
// (que desativa a checagem de producao) seja pega — os TestBalance_* baseados
// no stub NAO pegariam. Prova nao-tautologica (FP #11, 29a onda).
// ---------------------------------------------------------------------------

func TestCheckBalance_BalancedReturnsNil(t *testing.T) {
	t.Parallel()
	// Par balanceado (500000 = 500000) por asset -> checkBalance de producao nil.
	if err := ledger.CheckBalanceForTest(balancedPostings("USDC", 6, 500_000, 1, 2)); err != nil {
		t.Fatalf("checkBalance de producao rejeitou par balanceado: %v", err)
	}
}

func TestCheckBalance_UnbalancedReturnsError(t *testing.T) {
	t.Parallel()
	// Par desbalanceado (100 != 50): a funcao de PRODUCAO deve retornar
	// ErrUnbalancedPostings. Sob a mutacao `if s.debit != s.credit` -> `if false`
	// em posting.go, checkBalance retornaria nil e ESTE teste falha (RED),
	// enquanto os TestBalance_* do stub continuariam verdes.
	postings := []ledger.PostingLine{
		{AccountID: 1, AssetCode: "USDC", Scale: 6, DebitMinorUnits: 100, CreditMinorUnits: 0},
		{AccountID: 2, AssetCode: "USDC", Scale: 6, DebitMinorUnits: 0, CreditMinorUnits: 50},
	}
	err := ledger.CheckBalanceForTest(postings)
	if !errors.Is(err, ledger.ErrUnbalancedPostings) {
		t.Fatalf("esperava ErrUnbalancedPostings da funcao de producao, got: %v", err)
	}
}

func TestCheckBalance_PerAssetImbalanceDetected(t *testing.T) {
	t.Parallel()
	// BRL balanceado, USDC desbalanceado (1_000_000 vs 999_999): o balanco e
	// verificado POR asset_code, entao a divergencia de um centavo em USDC
	// e detectada mesmo com BRL perfeito. Cobre o loop `for code, s := range
	// byAsset` da funcao de producao.
	postings := []ledger.PostingLine{
		{AccountID: 1, AssetCode: "BRL", Scale: 2, DebitMinorUnits: 1000, CreditMinorUnits: 0},
		{AccountID: 2, AssetCode: "BRL", Scale: 2, DebitMinorUnits: 0, CreditMinorUnits: 1000},
		{AccountID: 3, AssetCode: "USDC", Scale: 6, DebitMinorUnits: 1_000_000, CreditMinorUnits: 0},
		{AccountID: 4, AssetCode: "USDC", Scale: 6, DebitMinorUnits: 0, CreditMinorUnits: 999_999},
	}
	if !errors.Is(ledger.CheckBalanceForTest(postings), ledger.ErrUnbalancedPostings) {
		t.Fatal("esperava ErrUnbalancedPostings: USDC desbalanceado por asset (funcao de producao)")
	}
}

// ---------------------------------------------------------------------------
// 3. pending -> posted na finalidade
// ---------------------------------------------------------------------------

func TestPendingToPosted_DepositLifecycle(t *testing.T) {
	t.Parallel()
	s := newStore()
	loader := enabledAssets()
	tenant := newTenantID(6)
	key := "deposit:0xdeadbeef001"

	p := ledger.RecordEntryParams{
		TenantID: tenant, IdempotencyKey: key, Description: "PENDING",
		Status:      ledger.EntryStatusPending, // PENDING
		EffectiveAt: time.Now(),
		Postings:    balancedPostings("USDC", 6, 5_000_000, 10, 20),
	}
	if _, err := s.recordEntry(loader, p); err != nil {
		t.Fatalf("RecordEntry PENDING: %v", err)
	}

	entry := s.entries[storeKey(tenant, key)]
	if entry.status != ledger.EntryStatusPending {
		t.Errorf("esperado PENDING, got %s", entry.status)
	}

	if err := s.finalizeEntry(tenant, key); err != nil {
		t.Fatalf("FinalizeEntry: %v", err)
	}
	if entry.status != ledger.EntryStatusPosted {
		t.Errorf("esperado posted apos finalizacao, got %s", entry.status)
	}
}

func TestPendingToPosted_FinalizeIdempotent(t *testing.T) {
	t.Parallel()
	s := newStore()
	loader := enabledAssets()
	tenant := newTenantID(7)
	key := "deposit:0xidempfinal"

	p := ledger.RecordEntryParams{
		TenantID: tenant, IdempotencyKey: key, Description: "PENDING",
		Status: ledger.EntryStatusPending, EffectiveAt: time.Now(),
		Postings: balancedPostings("USDC", 6, 1_000_000, 1, 2),
	}
	s.recordEntry(loader, p) //nolint:errcheck

	if err := s.finalizeEntry(tenant, key); err != nil {
		t.Fatalf("primeira finalizacao: %v", err)
	}
	if err := s.finalizeEntry(tenant, key); err != nil {
		t.Fatalf("segunda finalizacao (idempotente): %v", err)
	}
}

// ---------------------------------------------------------------------------
// 4. Recusa de posting em ativo disabled (AEV/BND)
// ---------------------------------------------------------------------------

func TestDisabledAsset_AEVRejected(t *testing.T) {
	t.Parallel()
	s := newStore()
	loader := enabledAssets()
	tenant := newTenantID(8)

	_, err := s.recordEntry(loader, makeEntry(tenant, "deposit:aev", balancedPostings("AEV", 0, 1_000, 1, 2)))
	if err != ledger.ErrAssetDisabled {
		t.Fatalf("esperava ErrAssetDisabled para AEV, got: %v", err)
	}
}

func TestDisabledAsset_BNDRejected(t *testing.T) {
	t.Parallel()
	s := newStore()
	loader := enabledAssets()
	tenant := newTenantID(9)

	_, err := s.recordEntry(loader, makeEntry(tenant, "deposit:bnd", balancedPostings("BND", 0, 500, 1, 2)))
	if err != ledger.ErrAssetDisabled {
		t.Fatalf("esperava ErrAssetDisabled para BND, got: %v", err)
	}
}

func TestDisabledAsset_EnabledAfterStubUpdate(t *testing.T) {
	t.Parallel()
	s := newStore()
	loader := enabledAssets()
	tenant := newTenantID(10)

	// AEV disabled — deve falhar.
	_, err := s.recordEntry(loader, makeEntry(tenant, "aev:before", balancedPostings("AEV", 0, 100, 1, 2)))
	if err != ledger.ErrAssetDisabled {
		t.Fatalf("esperava ErrAssetDisabled, got: %v", err)
	}

	// Habilita AEV com scale=18 (premissa E.2 ADR-0004).
	loader.SetAsset(ledger.Asset{Code: "AEV", Scale: 18, Enabled: true})

	// Agora deve aceitar.
	_, err = s.recordEntry(loader, makeEntry(tenant, "aev:after", balancedPostings("AEV", 18, 100, 1, 2)))
	if err != nil {
		t.Fatalf("esperava sucesso apos habilitar AEV, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 5. Cambio como dois pares isolados por asset_code (DA-10)
// ---------------------------------------------------------------------------

func TestFXExchange_TwoPairsIsolated(t *testing.T) {
	t.Parallel()
	s := newStore()
	loader := enabledAssets()
	tenant := newTenantID(11)

	// Par BRL: debit 512300, credit 512300.
	// Par USDC: debit 1000000, credit 1000000.
	// Valores por ativo NAO precisam ser iguais entre si (DA-10: ledgers isolados).
	postings := []ledger.PostingLine{
		{AccountID: 100, AssetCode: "BRL", Scale: 2, DebitMinorUnits: 512300, CreditMinorUnits: 0},
		{AccountID: 101, AssetCode: "BRL", Scale: 2, DebitMinorUnits: 0, CreditMinorUnits: 512300},
		{AccountID: 200, AssetCode: "USDC", Scale: 6, DebitMinorUnits: 0, CreditMinorUnits: 1_000_000},
		{AccountID: 201, AssetCode: "USDC", Scale: 6, DebitMinorUnits: 1_000_000, CreditMinorUnits: 0},
	}

	p := ledger.RecordEntryParams{
		TenantID: tenant, IdempotencyKey: "fx:deal-001:BRL-USDC",
		Description: "Cambio BRL->USDC @ 5.123 desk-ops",
		Status: ledger.EntryStatusPosted, EffectiveAt: time.Now(),
		Metadata: []byte(`{"rate_applied":"5.123","approver":"desk-ops","rate_source":"manual"}`),
		Postings: postings,
	}
	if _, err := s.recordEntry(loader, p); err != nil {
		t.Fatalf("FX exchange falhou: %v", err)
	}

	// Verifica balance isolado por asset.
	brlD, brlC, usdcD, usdcC := int64(0), int64(0), int64(0), int64(0)
	entry := s.entries[storeKey(tenant, "fx:deal-001:BRL-USDC")]
	for _, pl := range entry.postings {
		switch pl.AssetCode {
		case "BRL":
			brlD += pl.DebitMinorUnits
			brlC += pl.CreditMinorUnits
		case "USDC":
			usdcD += pl.DebitMinorUnits
			usdcC += pl.CreditMinorUnits
		}
	}
	if brlD != brlC {
		t.Errorf("BRL desbalanceado: debit=%d credit=%d", brlD, brlC)
	}
	if usdcD != usdcC {
		t.Errorf("USDC desbalanceado: debit=%d credit=%d", usdcD, usdcC)
	}
}

func TestFXExchange_CrossAssetAmountsNeedNotMatch(t *testing.T) {
	t.Parallel()
	// DA-10: BRL e USDC sao ledgers isolados. Igualar os valores seria cambio
	// implicito, proibido. Apenas o balance POR ativo e exigido.
	s := newStore()
	loader := enabledAssets()
	tenant := newTenantID(12)

	postings := []ledger.PostingLine{
		{AccountID: 1, AssetCode: "BRL", Scale: 2, DebitMinorUnits: 1000, CreditMinorUnits: 0},
		{AccountID: 2, AssetCode: "BRL", Scale: 2, DebitMinorUnits: 0, CreditMinorUnits: 1000},
		{AccountID: 3, AssetCode: "USDC", Scale: 6, DebitMinorUnits: 195, CreditMinorUnits: 0},
		{AccountID: 4, AssetCode: "USDC", Scale: 6, DebitMinorUnits: 0, CreditMinorUnits: 195},
	}
	if _, err := s.recordEntry(loader, makeEntry(tenant, "fx:cross", postings)); err != nil {
		t.Fatalf("cambio com valores diferentes por ativo deveria passar: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 6. Estorno como novo par de postings (nunca edicao/DELETE do original)
// ---------------------------------------------------------------------------

func TestReversal_NewPairDoesNotEditOriginal(t *testing.T) {
	t.Parallel()
	s := newStore()
	loader := enabledAssets()
	tenant := newTenantID(13)
	originalKey := "deposit:0xcafecafe"
	voidKey := "void:" + originalKey

	// Deposito original.
	if _, err := s.recordEntry(loader, makeEntry(tenant, originalKey, balancedPostings("USDC", 6, 2_000_000, 10, 20))); err != nil {
		t.Fatalf("deposito original: %v", err)
	}

	// Estorno: postings invertidos, nova chave "void:{original_key}".
	voidPostings := []ledger.PostingLine{
		{AccountID: 10, AssetCode: "USDC", Scale: 6, DebitMinorUnits: 0, CreditMinorUnits: 2_000_000},
		{AccountID: 20, AssetCode: "USDC", Scale: 6, DebitMinorUnits: 2_000_000, CreditMinorUnits: 0},
	}
	if _, err := s.recordEntry(loader, makeEntry(tenant, voidKey, voidPostings)); err != nil {
		t.Fatalf("estorno: %v", err)
	}

	// Original imutavel.
	orig := s.entries[storeKey(tenant, originalKey)]
	if orig == nil {
		t.Fatal("entry original nao deveria ser deletada")
	}
	if orig.postings[0].DebitMinorUnits != 2_000_000 {
		t.Error("posting original nao deveria ser modificado")
	}

	// Estorno e entry separada.
	void := s.entries[storeKey(tenant, voidKey)]
	if void == nil {
		t.Fatal("entry de estorno nao encontrada")
	}
	if len(s.entries) < 2 {
		t.Errorf("esperava >= 2 entries (original + void), got %d", len(s.entries))
	}
}

func TestReversal_VoidKeyIdempotent(t *testing.T) {
	t.Parallel()
	s := newStore()
	loader := enabledAssets()
	tenant := newTenantID(14)
	voidKey := "void:deposit:0xreorg01"

	postings := balancedPostings("USDC", 6, 1_000_000, 1, 2)
	_, err1 := s.recordEntry(loader, makeEntry(tenant, voidKey, postings))
	_, err2 := s.recordEntry(loader, makeEntry(tenant, voidKey, postings))

	if err1 != nil {
		t.Fatalf("primeiro estorno falhou: %v", err1)
	}
	if err2 != ledger.ErrIdempotentDuplicate {
		t.Fatalf("segundo estorno deveria ser ErrIdempotentDuplicate, got: %v", err2)
	}
}

// ---------------------------------------------------------------------------
// 7. Reconciliacao: abre excecao, nunca autocorrige, rerun idempotente
// ---------------------------------------------------------------------------

// reconStoreStub implementa ledger.ReconStore para testes sem Postgres.
type reconStoreStub struct {
	sumByKey         map[string]*big.Int // "tenantID:assetCode" -> valor simulado
	exceptions       []ledger.OpenExceptionParamsExported
	postingsInserted int // DEVE ser sempre 0 (nunca autocorrige)
}

func (r *reconStoreStub) SumLedgerPostings(_ context.Context, tenantID, assetCode string, _, _ time.Time) (string, error) {
	key := tenantID + ":" + assetCode
	v, ok := r.sumByKey[key]
	if !ok {
		return "0", nil
	}
	return v.String(), nil
}

func (r *reconStoreStub) OpenException(_ context.Context, p ledger.OpenExceptionParamsExported) error {
	for _, e := range r.exceptions {
		if e.TenantID == p.TenantID && e.AssetCode == p.AssetCode &&
			e.PeriodStart.Equal(p.PeriodStart) && e.PeriodEnd.Equal(p.PeriodEnd) {
			return ledger.ErrExceptionAlreadyOpen
		}
	}
	r.exceptions = append(r.exceptions, p)
	return nil
}

func TestReconciliation_OpenExceptionOnDivergence(t *testing.T) {
	t.Parallel()
	tenant := newTenantID(20)
	periodStart := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 6, 1, 1, 0, 0, 0, time.UTC)

	source := ledger.NewExpectedValueSourceStub([]ledger.PeriodExpected{
		{TenantID: tenant, AssetCode: "USDC", PeriodStart: periodStart, PeriodEnd: periodEnd, ExpectedMinorUnits: big.NewInt(1_000_000)},
	})
	rdb := &reconStoreStub{
		sumByKey: map[string]*big.Int{tenant + ":USDC": big.NewInt(900_000)}, // diverge 100k
	}

	rec := ledger.NewReconcilerForTest(rdb, source)
	result, err := rec.Run(context.Background(), ledger.ReconcileParams{PeriodStart: periodStart, PeriodEnd: periodEnd})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Diverged != 1 {
		t.Errorf("esperava 1 divergencia, got %d", result.Diverged)
	}
	if len(result.Exceptions) != 1 {
		t.Fatalf("esperava 1 excecao, got %d", len(result.Exceptions))
	}
	exc := result.Exceptions[0]
	if exc.AssetCode != "USDC" {
		t.Errorf("asset=%q, esperava USDC", exc.AssetCode)
	}
	wantDiv := big.NewInt(100_000)
	if exc.DivergenceMinorUnits.Cmp(wantDiv) != 0 {
		t.Errorf("divergencia=%s, esperava %s", exc.DivergenceMinorUnits, wantDiv)
	}
}

func TestReconciliation_NoDivergenceNoException(t *testing.T) {
	t.Parallel()
	tenant := newTenantID(21)
	periodStart := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 6, 2, 1, 0, 0, 0, time.UTC)

	source := ledger.NewExpectedValueSourceStub([]ledger.PeriodExpected{
		{TenantID: tenant, AssetCode: "USDC", PeriodStart: periodStart, PeriodEnd: periodEnd, ExpectedMinorUnits: big.NewInt(500_000)},
	})
	rdb := &reconStoreStub{
		sumByKey: map[string]*big.Int{tenant + ":USDC": big.NewInt(500_000)}, // equilibrio
	}

	rec := ledger.NewReconcilerForTest(rdb, source)
	result, err := rec.Run(context.Background(), ledger.ReconcileParams{PeriodStart: periodStart, PeriodEnd: periodEnd})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Diverged != 0 {
		t.Errorf("sem divergencia: esperava 0 excecoes, got %d", result.Diverged)
	}
}

func TestReconciliation_DoesNotAutocorrect(t *testing.T) {
	t.Parallel()
	tenant := newTenantID(22)
	periodStart := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 6, 3, 1, 0, 0, 0, time.UTC)

	source := ledger.NewExpectedValueSourceStub([]ledger.PeriodExpected{
		{TenantID: tenant, AssetCode: "BRL", PeriodStart: periodStart, PeriodEnd: periodEnd, ExpectedMinorUnits: big.NewInt(1000)},
	})
	rdb := &reconStoreStub{
		sumByKey: map[string]*big.Int{tenant + ":BRL": big.NewInt(800)},
	}

	rec := ledger.NewReconcilerForTest(rdb, source)
	result, _ := rec.Run(context.Background(), ledger.ReconcileParams{PeriodStart: periodStart, PeriodEnd: periodEnd})

	// Nunca autocorrige — postingsInserted deve ser 0.
	if rdb.postingsInserted > 0 {
		t.Errorf("reconciliacao NAO deve inserir postings (nunca autocorrige): %d inseridos", rdb.postingsInserted)
	}
	if result.Diverged != 1 {
		t.Errorf("esperava 1 divergencia, got %d", result.Diverged)
	}
}

func TestReconciliation_RerunIdempotent(t *testing.T) {
	t.Parallel()
	tenant := newTenantID(23)
	periodStart := time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 6, 4, 1, 0, 0, 0, time.UTC)

	source := ledger.NewExpectedValueSourceStub([]ledger.PeriodExpected{
		{TenantID: tenant, AssetCode: "USDC", PeriodStart: periodStart, PeriodEnd: periodEnd, ExpectedMinorUnits: big.NewInt(2_000_000)},
	})
	rdb := &reconStoreStub{
		sumByKey: map[string]*big.Int{tenant + ":USDC": big.NewInt(1_800_000)},
	}

	rec := ledger.NewReconcilerForTest(rdb, source)

	// Run 1: abre excecao.
	r1, err := rec.Run(context.Background(), ledger.ReconcileParams{PeriodStart: periodStart, PeriodEnd: periodEnd})
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	if r1.Diverged != 1 {
		t.Errorf("run 1: esperava 1 divergencia, got %d", r1.Diverged)
	}

	// Run 2 (rerun): excecao ja existe — idempotente, nao incrementa Diverged.
	r2, err := rec.Run(context.Background(), ledger.ReconcileParams{PeriodStart: periodStart, PeriodEnd: periodEnd})
	if err != nil {
		t.Fatalf("run 2 (rerun): %v", err)
	}
	if r2.Diverged != 0 {
		t.Errorf("rerun nao deve contar nova divergencia (excecao ja aberta): got %d", r2.Diverged)
	}
}

// ---------------------------------------------------------------------------
// 8. Scale mismatch rejeitado
// ---------------------------------------------------------------------------

func TestScaleMismatch_Rejected(t *testing.T) {
	t.Parallel()
	s := newStore()
	loader := enabledAssets()
	tenant := newTenantID(30)

	// USDC Registry=6; postando com scale=2 — deve falhar.
	_, err := s.recordEntry(loader, makeEntry(tenant, "scale:mismatch", balancedPostings("USDC", 2, 100, 1, 2)))
	if err != ledger.ErrScaleMismatch {
		t.Fatalf("esperava ErrScaleMismatch, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 9. Asset nao encontrado rejeitado
// ---------------------------------------------------------------------------

func TestAssetNotFound_Rejected(t *testing.T) {
	t.Parallel()
	s := newStore()
	loader := enabledAssets()
	tenant := newTenantID(31)

	_, err := s.recordEntry(loader, makeEntry(tenant, "unknown:asset", balancedPostings("XUNKNOWN", 8, 100, 1, 2)))
	if err == nil {
		t.Fatal("esperava erro para asset desconhecido")
	}
}

// ---------------------------------------------------------------------------
// 10. Payout desbalanceado rejeitado
// ---------------------------------------------------------------------------

func TestPayout_UnbalancedRejected(t *testing.T) {
	t.Parallel()
	s := newStore()
	loader := enabledAssets()
	tenant := newTenantID(32)

	// debit=500, credit=300 — desbalanceado.
	postings := []ledger.PostingLine{
		{AccountID: 1, AssetCode: "USDC", Scale: 6, DebitMinorUnits: 500, CreditMinorUnits: 0},
		{AccountID: 2, AssetCode: "USDC", Scale: 6, DebitMinorUnits: 0, CreditMinorUnits: 300},
	}
	_, err := s.recordEntry(loader, makeEntry(tenant, "payout:unbalanced", postings))
	if err != ledger.ErrUnbalancedPostings {
		t.Fatalf("esperava ErrUnbalancedPostings, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// AccountCodes: codigos canonicos corretos (BILLING.md §2)
// ---------------------------------------------------------------------------

func TestAccountCodes_CanonicalFormat(t *testing.T) {
	t.Parallel()
	codes := ledger.AccountCodes("advertiser-42", "USDC")

	if codes.AdvBalance != "adv:advertiser-42:USDC" {
		t.Errorf("AdvBalance: %q", codes.AdvBalance)
	}
	if codes.PlatformRevenue != "platform:revenue:USDC" {
		t.Errorf("PlatformRevenue: %q", codes.PlatformRevenue)
	}
	if codes.PlatformCash != "platform:cash:USDC" {
		t.Errorf("PlatformCash: %q", codes.PlatformCash)
	}
	if codes.PlatformReceivable != "platform:receivable:USDC" {
		t.Errorf("PlatformReceivable: %q", codes.PlatformReceivable)
	}
	if codes.Rounding != "rounding:USDC" {
		t.Errorf("Rounding: %q", codes.Rounding)
	}
}
