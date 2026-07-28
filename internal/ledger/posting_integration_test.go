//go:build integration

// Package ledger_test — testes de integracao (K3 hardening, achado HIGH
// Bundle A) que chamam ledger.RecordEntry / ledger.FinalizeEntry /
// ledger.RecordPayout / ledger.EnsureAccount REAIS (posting.go / crypto.go /
// account.go) contra um Postgres 16 de verdade — nao o `store` em memoria de
// ledger_test.go, que reimplementa a mesma logica sem tocar banco algum.
//
// Fecham exatamente as 4 lacunas do achado (nenhuma pega pelos TestStub* de
// ledger_test.go, todas comprovadas por mutacao):
//
//	(a) Idempotencia REAL via UNIQUE (tenant_id, idempotency_key) + o
//	    `ON CONFLICT ... DO NOTHING` de insertJournalEntry (posting.go):
//	    reprocessar a mesma idempotency_key duas vezes contra o Postgres
//	    real deve produzir exatamente 1 journal_entry e nao duplicar
//	    postings. Mutacao: remover o ON CONFLICT faz o Postgres devolver um
//	    unique_violation cru (nao ledger.ErrIdempotentDuplicate) na segunda
//	    chamada — este teste vai a vermelho.
//
//	(b) Transicao pending->posted via `UPDATE ... WHERE status='pending'`
//	    (FinalizeEntry, posting.go): a segunda chamada a FinalizeEntry para
//	    a mesma entry (ja 'posted') deve ser idempotente (nil). Mutacao:
//	    remover a guarda `AND status='pending'` faz o UPDATE tentar tocar a
//	    linha ja 'posted'; o trigger de imutabilidade da migration 0004
//	    (journal_entries_immutable_trg) dispara e a segunda chamada retorna
//	    erro em vez de nil — este teste vai a vermelho.
//
//	(c) Ordem de validacao de asset: `validateAsset` (asset_registry.go /
//	    account.go) DEVE rejeitar um ativo disabled/scale divergente ANTES
//	    de qualquer INSERT em journal_entries/postings. Usamos AccountIDs
//	    REAIS (criados via EnsureAccount sob um ativo habilitado) para que a
//	    ausencia da validacao nao seja mascarada por uma FK violation
//	    acidental — se a checagem for removida de dentro de RecordEntry, o
//	    INSERT teria sucesso (nenhuma constraint de banco impede scale
//	    errado ou ativo desabilitado) e a contagem de linhas ficaria > 0.
//
//	(d) Direcao dos postings de RecordPayout (crypto.go): a conta do
//	    anunciante (liability) deve ser DEBITADA e a conta de caixa da
//	    plataforma (asset) deve ser CREDITADA. checkBalance (sum(debit) ==
//	    sum(credit)) NAO pega uma troca Debit<->Credit porque a troca e
//	    simetrica — so uma assercao por CONTA/CAMPO no Postgres real prova a
//	    direcao.
//
// COMO RODAR
//
//	export DATABASE_URL=postgres://user:pass@host:5432/db?sslmode=disable
//	# com as migrations db/ledger/migrations/0001..0004 ja aplicadas
//	go test -tags integration -count=1 -race ./internal/ledger/...
//
// Ou via `make go-test-integration` (deriva os pacotes que tem arquivo
// `//go:build integration` — hoje so internal/ledger — e exige DATABASE_URL
// via t.Fatal deste arquivo, nao um check hardcoded no Makefile). Em CI:
// .github/workflows/db.yml ja sobe um Postgres 16 efemero e aplica as
// migrations do ledger antes de rodar este pacote.
//
// Roda como o role do DATABASE_URL diretamente (superusuario em CI/local, a
// mesma convencao de db/ledger/tests/postings_immutability_test.sql) DE
// PROPOSITO: o alvo aqui e provar a CORRETUDE DA APLICACAO (Go), nao o
// isolamento por tenant (RLS) — ja coberto por
// db/ledger/tests/rls_isolation_test.sql. RLS com FORCE ROW LEVEL SECURITY
// nao se aplica a superusuario de qualquer forma.
//
// Nao usa asset_registry real: RecordEntry/EnsureAccount recebem um
// AssetLoader injetavel — usamos ledger.AssetLoaderStub (mesmo helper
// enabledAssets() de ledger_test.go, mesmo pacote de teste) para manter o
// escopo estritamente em internal/ledger/** (sem depender do schema
// asset_registry).
package ledger_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hojex/adserver/internal/ledger"
)

// integrationPool abre um pool contra DATABASE_URL. Falha alto e claro (nao
// t.Skip) se a variavel nao estiver definida: um teste tagged `integration`
// que roda sem infra viva e exatamente o "no-op mascarado de validacao real"
// que make/go.mk::go-test-integration existe para evitar.
func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Fatal("DATABASE_URL nao definido — este teste (//go:build integration) exige um " +
			"Postgres 16 real com db/ledger/migrations/0001..0004 aplicadas. " +
			"export DATABASE_URL=postgres://user:pass@host:5432/db?sslmode=disable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New(%q): %v", dsn, err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("pool.Ping: %v (Postgres real acessivel em DATABASE_URL, migrations do ledger aplicadas?)", err)
	}
	return pool
}

// uniqueSuffix gera um sufixo hex curto e aleatorio (minusculo — obrigatorio
// pelo CHECK accounts_code_fmt_chk '^[a-z0-9:_\-]+$') para que reruns locais
// contra um Postgres persistente nao colidam em idempotency_key/account code
// com uma execucao anterior.
func uniqueSuffix(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("crypto/rand: %v", err)
	}
	return hex.EncodeToString(buf)
}

// itEnsureAccount cria (idempotente) uma conta real via ledger.EnsureAccount
// sob um asset_code habilitado no AssetLoaderStub — usado para obter
// AccountIDs REAIS (satisfazem postings_account_fk) sem depender do schema
// asset_registry.
func itEnsureAccount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, loader ledger.AssetLoader, tenant, code string, kind ledger.AccountKind) ledger.Account {
	t.Helper()
	acc, err := ledger.EnsureAccount(ctx, pool, loader, ledger.EnsureAccountParams{
		TenantID:  tenant,
		Code:      code,
		Name:      "integration test: " + code,
		Kind:      kind,
		AssetCode: "USDC",
		Scale:     6,
	})
	if err != nil {
		t.Fatalf("EnsureAccount(%q): %v", code, err)
	}
	return acc
}

// itEntryStatus busca o status real da journal_entry no Postgres.
func itEntryStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenant, key string) string {
	t.Helper()
	var status string
	err := pool.QueryRow(ctx,
		`SELECT status::text FROM ledger.journal_entries WHERE tenant_id=$1::uuid AND idempotency_key=$2`,
		tenant, key,
	).Scan(&status)
	if err != nil {
		t.Fatalf("consultar status da journal_entry (tenant=%s key=%s): %v", tenant, key, err)
	}
	return status
}

// itCountJournalEntries conta quantas journal_entries existem para a chave.
func itCountJournalEntries(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenant, key string) int {
	t.Helper()
	var n int
	err := pool.QueryRow(ctx,
		`SELECT count(*) FROM ledger.journal_entries WHERE tenant_id=$1::uuid AND idempotency_key=$2`,
		tenant, key,
	).Scan(&n)
	if err != nil {
		t.Fatalf("contar journal_entries: %v", err)
	}
	return n
}

// itCountPostings conta quantos postings existem para uma journal_entry.
func itCountPostings(t *testing.T, ctx context.Context, pool *pgxpool.Pool, entryID int64) int {
	t.Helper()
	var n int
	err := pool.QueryRow(ctx,
		`SELECT count(*) FROM ledger.postings WHERE journal_entry_id=$1`, entryID,
	).Scan(&n)
	if err != nil {
		t.Fatalf("contar postings: %v", err)
	}
	return n
}

// itPostingAmounts busca (debit_amount, credit_amount) de UM posting
// especifico (journal_entry_id, account_id) como *big.Int (minor units
// inteiros — as fixtures deste arquivo nao usam casas decimais fracionarias).
func itPostingAmounts(t *testing.T, ctx context.Context, pool *pgxpool.Pool, entryID, accountID int64) (debit, credit *big.Int) {
	t.Helper()
	var debitStr, creditStr string
	err := pool.QueryRow(ctx,
		`SELECT debit_amount::text, credit_amount::text FROM ledger.postings WHERE journal_entry_id=$1 AND account_id=$2`,
		entryID, accountID,
	).Scan(&debitStr, &creditStr)
	if err != nil {
		t.Fatalf("consultar posting (entry=%d account=%d): %v", entryID, accountID, err)
	}
	return itParseIntegerNumeric(t, debitStr), itParseIntegerNumeric(t, creditStr)
}

// itParseIntegerNumeric converte uma string NUMERIC(40,18) do Postgres (ex.:
// "750000.000000000000000000") para *big.Int, truncando a parte decimal —
// helper de teste isolado, NAO uma chamada ao numericStringToBigInt interno
// de posting.go (inacessivel deste pacote de teste externo).
func itParseIntegerNumeric(t *testing.T, s string) *big.Int {
	t.Helper()
	for i, c := range s {
		if c == '.' {
			s = s[:i]
			break
		}
	}
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		t.Fatalf("valor NUMERIC invalido: %q", s)
	}
	return n
}

// ---------------------------------------------------------------------------
// (a) Idempotencia REAL: ON CONFLICT (tenant_id, idempotency_key) DO NOTHING
// ---------------------------------------------------------------------------

func TestIntegrationRecordEntry_IdempotencyRealConflict(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	loader := enabledAssets()
	tenant := newTenantID(100001)
	suffix := uniqueSuffix(t)
	key := "it:idem:" + suffix

	debitAcc := itEnsureAccount(t, ctx, pool, loader, tenant, "adv:it-idem-"+suffix+":usdc", ledger.AccountKindLiability)
	creditAcc := itEnsureAccount(t, ctx, pool, loader, tenant, "platform:cash:it-idem-"+suffix+":usdc", ledger.AccountKindAsset)

	params := ledger.RecordEntryParams{
		TenantID:       tenant,
		IdempotencyKey: key,
		Description:    "integration: idempotency real conflict",
		Status:         ledger.EntryStatusPosted,
		EffectiveAt:    time.Now(),
		Postings:       balancedPostings("USDC", 6, 1_000_000, debitAcc.ID, creditAcc.ID),
	}

	id1, err1 := ledger.RecordEntry(ctx, pool, loader, params)
	if err1 != nil {
		t.Fatalf("primeira gravacao (RecordEntry real): %v", err1)
	}

	// Reprocessamento: MESMA idempotency_key. Sem o ON CONFLICT DO NOTHING em
	// insertJournalEntry, o Postgres devolveria um unique_violation cru (o
	// UNIQUE (tenant_id, idempotency_key) de journal_entries_idempotency_uq
	// dispara de qualquer forma) e RecordEntry propagaria esse erro bruto em
	// vez de ledger.ErrIdempotentDuplicate — a asserção abaixo vai a
	// vermelho sob essa mutação.
	id2, err2 := ledger.RecordEntry(ctx, pool, loader, params)
	if !errors.Is(err2, ledger.ErrIdempotentDuplicate) {
		t.Fatalf("esperava ledger.ErrIdempotentDuplicate no reprocessamento REAL (Postgres), got: %v", err2)
	}
	if id1 != id2 {
		t.Errorf("ids devem ser iguais apos reprocessamento real: %d != %d", id1, id2)
	}

	if n := itCountJournalEntries(t, ctx, pool, tenant, key); n != 1 {
		t.Fatalf("esperava exatamente 1 journal_entry no Postgres apos reprocessamento, encontrou %d", n)
	}
	if n := itCountPostings(t, ctx, pool, int64(id1)); n != 2 {
		t.Fatalf("esperava exatamente 2 postings (1 par) apos reprocessamento — reprocessamento nao deve duplicar postings, encontrou %d", n)
	}
}

// ---------------------------------------------------------------------------
// (b) pending -> posted via UPDATE guardado por status='pending'
// ---------------------------------------------------------------------------

func TestIntegrationFinalizeEntry_PendingToPostedRealGuard(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	loader := enabledAssets()
	tenant := newTenantID(100002)
	suffix := uniqueSuffix(t)
	key := "it:finalize:" + suffix

	debitAcc := itEnsureAccount(t, ctx, pool, loader, tenant, "adv:it-fin-"+suffix+":usdc", ledger.AccountKindLiability)
	creditAcc := itEnsureAccount(t, ctx, pool, loader, tenant, "platform:cash:it-fin-"+suffix+":usdc", ledger.AccountKindAsset)

	if _, err := ledger.RecordEntry(ctx, pool, loader, ledger.RecordEntryParams{
		TenantID:       tenant,
		IdempotencyKey: key,
		Description:    "integration: pending deposit",
		Status:         ledger.EntryStatusPending,
		EffectiveAt:    time.Now(),
		Postings:       balancedPostings("USDC", 6, 500_000, debitAcc.ID, creditAcc.ID),
	}); err != nil {
		t.Fatalf("RecordEntry PENDING real: %v", err)
	}

	if got := itEntryStatus(t, ctx, pool, tenant, key); got != "pending" {
		t.Fatalf("status apos RecordEntry deveria ser 'pending' no Postgres, got %q", got)
	}

	if err := ledger.FinalizeEntry(ctx, pool, ledger.FinalizeEntryParams{TenantID: tenant, IdempotencyKey: key}); err != nil {
		t.Fatalf("primeira finalizacao real: %v", err)
	}
	if got := itEntryStatus(t, ctx, pool, tenant, key); got != "posted" {
		t.Fatalf("status apos FinalizeEntry deveria ser 'posted' no Postgres, got %q", got)
	}

	// Segunda finalizacao: DEVE ser idempotente (nil). A UPDATE de
	// FinalizeEntry e guardada por "AND status='pending'" — sem essa guarda,
	// o UPDATE tentaria tocar a linha ja 'posted' e o trigger de
	// imutabilidade (migration 0004, journal_entries_immutable_trg) dispara
	// "ja esta status=posted (imutavel)" — a chamada retornaria erro em vez
	// de nil, e esta asserção vai a vermelho.
	if err := ledger.FinalizeEntry(ctx, pool, ledger.FinalizeEntryParams{TenantID: tenant, IdempotencyKey: key}); err != nil {
		t.Fatalf("segunda finalizacao (idempotente) deveria retornar nil, got: %v — "+
			"se a guarda WHERE status='pending' for removida de FinalizeEntry, o UPDATE tenta "+
			"tocar uma entry ja 'posted' e o trigger de imutabilidade (migration 0004) rejeita", err)
	}
	if got := itEntryStatus(t, ctx, pool, tenant, key); got != "posted" {
		t.Fatalf("status apos segunda finalizacao deveria continuar 'posted', got %q", got)
	}
}

// ---------------------------------------------------------------------------
// (c) Validacao de asset REJEITA ANTES de qualquer INSERT
// ---------------------------------------------------------------------------

func TestIntegrationRecordEntry_AssetValidationRejectsBeforeInsert(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	loader := enabledAssets() // AEV/BND disabled; USDC enabled scale=6
	tenant := newTenantID(100003)
	suffix := uniqueSuffix(t)

	// Contas REAIS (nao IDs fantasmas): se a validacao de asset for removida
	// de RecordEntry, o INSERT em ledger.postings teria sucesso (nenhuma
	// constraint do banco verifica enabled/scale contra o Asset Registry —
	// so a checagem em Go faz isso), entao a contagem de linhas abaixo
	// pegaria a regressao. Contas fantasmas (IDs inexistentes) mascarariam a
	// mutacao atras de uma FK violation acidental de insertPostings.
	debitAcc := itEnsureAccount(t, ctx, pool, loader, tenant, "adv:it-assetval-"+suffix+":usdc", ledger.AccountKindLiability)
	creditAcc := itEnsureAccount(t, ctx, pool, loader, tenant, "platform:cash:it-assetval-"+suffix+":usdc", ledger.AccountKindAsset)

	cases := []struct {
		name      string
		assetCode string
		scale     int32
		wantErr   error
	}{
		// AEV esta enabled=false no AssetLoaderStub (scale TBD, §3 q.2).
		{name: "disabled_asset_AEV", assetCode: "AEV", scale: 0, wantErr: ledger.ErrAssetDisabled},
		// USDC esta enabled=true com Scale=6 no Registry; postar com scale=2 diverge.
		{name: "scale_mismatch_USDC", assetCode: "USDC", scale: 2, wantErr: ledger.ErrScaleMismatch},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := "it:assetval:" + tc.name + ":" + suffix

			_, err := ledger.RecordEntry(ctx, pool, loader, ledger.RecordEntryParams{
				TenantID:       tenant,
				IdempotencyKey: key,
				Description:    "integration: asset validation order",
				Status:         ledger.EntryStatusPosted,
				EffectiveAt:    time.Now(),
				Postings:       balancedPostings(tc.assetCode, tc.scale, 1_000, debitAcc.ID, creditAcc.ID),
			})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("esperava %v do RecordEntry REAL, got: %v", tc.wantErr, err)
			}

			// A prova da ORDEM: nenhuma linha deve existir no Postgres — a
			// validacao de asset roda ANTES de qualquer tx.Begin()/INSERT.
			if n := itCountJournalEntries(t, ctx, pool, tenant, key); n != 0 {
				t.Fatalf("RecordEntry NAO deveria ter inserido journal_entry para %s "+
					"(validacao de asset deve rejeitar ANTES do INSERT); encontrou %d linha(s)", tc.name, n)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// (d) Direcao dos postings de RecordPayout (debito/credito)
// ---------------------------------------------------------------------------

func TestIntegrationRecordPayout_PostingDirectionMatchesAccountKind(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	loader := enabledAssets()
	tenant := newTenantID(100004)
	suffix := uniqueSuffix(t)

	advAcc := itEnsureAccount(t, ctx, pool, loader, tenant, "adv:it-payout-"+suffix+":usdc", ledger.AccountKindLiability)
	cashAcc := itEnsureAccount(t, ctx, pool, loader, tenant, "platform:cash:it-payout-"+suffix+":usdc", ledger.AccountKindAsset)

	amount := int64(750_000)
	id, err := ledger.RecordPayout(ctx, pool, loader, ledger.PayoutParams{
		TenantID:              tenant,
		PaymentEventID:        "it-payout-" + suffix,
		AssetCode:             "USDC",
		Scale:                 6,
		AmountMinor:           amount,
		EffectiveAt:           time.Now(),
		AdvAccountID:          advAcc.ID,
		PlatformCashAccountID: cashAcc.ID,
	})
	if err != nil {
		t.Fatalf("RecordPayout real: %v", err)
	}

	wantAmount := big.NewInt(amount)

	// Conta do anunciante (liability, "adv:{id}:{asset}"): payout REDUZ o
	// saldo devido ao anunciante -> DEBITO. checkBalance (sum(debit) ==
	// sum(credit)) nao pega uma troca Debit<->Credit porque a soma continua
	// batendo simetricamente — so a asserção por CONTA abaixo prova a direcao.
	advDebit, advCredit := itPostingAmounts(t, ctx, pool, int64(id), advAcc.ID)
	if advDebit.Cmp(wantAmount) != 0 || advCredit.Sign() != 0 {
		t.Fatalf("conta do anunciante (liability) deveria ser DEBITADA em %s (reduz saldo devido): debit=%s credit=%s",
			wantAmount, advDebit, advCredit)
	}

	// Conta de caixa da plataforma (asset, "platform:cash:{asset}"): payout
	// REDUZ o caixa da plataforma -> CREDITO.
	cashDebit, cashCredit := itPostingAmounts(t, ctx, pool, int64(id), cashAcc.ID)
	if cashCredit.Cmp(wantAmount) != 0 || cashDebit.Sign() != 0 {
		t.Fatalf("conta de caixa da plataforma (asset) deveria ser CREDITADA em %s (reduz caixa): debit=%s credit=%s",
			wantAmount, cashDebit, cashCredit)
	}

	// Controle positivo: o par ainda deve estar balanceado (nao e o
	// bug que estamos caçando, mas confirma que a fixture em si é valida).
	if advDebit.Cmp(cashCredit) != 0 {
		t.Fatalf("par de payout desbalanceado: adv.debit=%s platform.credit=%s", advDebit, cashCredit)
	}
}
