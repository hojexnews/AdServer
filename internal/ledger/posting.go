// Package ledger — posting.go
//
// Gravacao do par de postings idempotente no ledger Postgres double-entry.
//
// Invariantes mantidas aqui:
//   - Float PROIBIDO: valores trafegam como int64 minor-units; NUMERIC(40,18)
//     no Postgres via string (pgx nao usa float para NUMERIC).
//   - sum(debit) = sum(credit) por entry+asset_code e garantido pelo
//     constraint trigger DEFERRABLE INITIALLY DEFERRED do schema (nao duplicar
//     aqui — o banco e a fonte de verdade).
//   - idempotency_key UNIQUE (tenant_id, key): reprocessamento nao duplica.
//   - status 'pending' para deposito cripto; 'posted' para entradas fiat/batch.
//   - Nenhuma captura grava saldo direto.
package ledger

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EntryStatus espelha ledger.entry_status.
type EntryStatus string

const (
	EntryStatusPending EntryStatus = "pending" // deposito cripto aguardando finalidade
	EntryStatusPosted  EntryStatus = "posted"  // confirmado e imutavel
	EntryStatusVoid    EntryStatus = "void"    // estornado (via novo par de postings)
)

// PostingLine descreve um unico debito ou credito.
//
// Exatamente um dos campos DebitMinorUnits / CreditMinorUnits deve ser > 0
// (invariante postings_debit_credit_xor_chk do schema).
//
// DebitMinorUnits e CreditMinorUnits sao int64 para aritmetica Go;
// gravamos como NUMERIC(40,18) string no Postgres (nao float).
type PostingLine struct {
	AccountID        int64
	AssetCode        string
	Scale            int32 // deve bater com Asset Registry (validado em RecordEntry)
	DebitMinorUnits  int64 // apenas um > 0
	CreditMinorUnits int64 // apenas um > 0
}

// RecordEntryParams descreve uma journal_entry + seus postings.
type RecordEntryParams struct {
	TenantID       string      // UUID em texto (mesma convencao do projeto)
	IdempotencyKey string      // formato: BILLING.md §3
	Description    string
	Status         EntryStatus // 'pending' para deposito cripto; 'posted' para outros
	EffectiveAt    time.Time
	RefType        string  // ex.: "tx_hash", "payment_event"
	RefID          *int64  // NULL se nao houver ref numerica
	Metadata       []byte  // JSONB (sem PII — DA-11)
	Postings       []PostingLine
}

// JournalEntryID e o id gerado pelo Postgres para a journal_entry.
type JournalEntryID int64

// ErrIdempotentDuplicate e retornado quando a idempotency_key ja existe.
// O caller pode ignorar com seguranca — a operacao foi bem-sucedida anteriormente.
var ErrIdempotentDuplicate = errors.New("ledger: idempotency_key ja existe (reprocessamento idempotente)")

// ErrUnbalancedPostings e retornado quando os postings nao balanceiam.
var ErrUnbalancedPostings = errors.New("ledger: postings desbalanceados: sum(debit) != sum(credit) por asset_code")

// ErrEmptyPostings e retornado quando o slice de postings esta vazio.
var ErrEmptyPostings = errors.New("ledger: nenhum posting fornecido")

// RecordEntry grava uma journal_entry + seus postings em UMA transacao atomica.
//
// Idempotencia: se a idempotency_key ja existir, retorna ErrIdempotentDuplicate
// com o id original. O caller pode tratar esse erro como sucesso (at-least-once).
func RecordEntry(ctx context.Context, pool *pgxpool.Pool, loader AssetLoader, p RecordEntryParams) (JournalEntryID, error) {
	if len(p.Postings) == 0 {
		return 0, ErrEmptyPostings
	}
	// Validacao antecipada de balance por asset_code.
	if err := checkBalance(p.Postings); err != nil {
		return 0, err
	}
	// Validacao de ativos: todos devem estar enabled com scale correto.
	seen := make(map[string]bool)
	for _, pl := range p.Postings {
		if seen[pl.AssetCode] {
			continue
		}
		seen[pl.AssetCode] = true
		if err := validateAsset(ctx, loader, pl.AssetCode, pl.Scale); err != nil {
			return 0, fmt.Errorf("RecordEntry posting[%s]: %w", pl.AssetCode, err)
		}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("RecordEntry: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	entryID, err := insertJournalEntry(ctx, tx, p)
	if err != nil {
		// Reprocesso at-least-once: a entry ja existe e seus postings ja foram
		// gravados na captura original. Propaga o id REAL (para tracing) com o
		// sentinel, sem reinserir postings. A tx faz rollback (no-op).
		if errors.Is(err, ErrIdempotentDuplicate) {
			return JournalEntryID(entryID), ErrIdempotentDuplicate
		}
		return 0, err
	}

	if err := insertPostings(ctx, tx, entryID, p.Postings); err != nil {
		return 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("RecordEntry: commit: %w", err)
	}
	return JournalEntryID(entryID), nil
}

// FinalizeEntryParams parametriza a transicao pending -> posted.
type FinalizeEntryParams struct {
	TenantID       string // UUID em texto
	IdempotencyKey string // chave da entry a finalizar
}

// FinalizeEntry transiciona uma journal_entry de 'pending' para 'posted'.
//
// Usada ao receber DepositFinalizedEvent (N confirmacoes on-chain).
// Idempotente: se a entry ja estiver 'posted', retorna sem erro.
func FinalizeEntry(ctx context.Context, pool *pgxpool.Pool, p FinalizeEntryParams) error {
	const sql = `
		UPDATE ledger.journal_entries
		SET    status = 'posted'
		WHERE  tenant_id       = $1::uuid
		  AND  idempotency_key = $2
		  AND  status          = 'pending'
	`
	tag, err := pool.Exec(ctx, sql, p.TenantID, p.IdempotencyKey)
	if err != nil {
		return fmt.Errorf("FinalizeEntry: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var status string
		err2 := pool.QueryRow(ctx,
			`SELECT status::text FROM ledger.journal_entries WHERE tenant_id=$1::uuid AND idempotency_key=$2`,
			p.TenantID, p.IdempotencyKey,
		).Scan(&status)
		if errors.Is(err2, pgx.ErrNoRows) {
			return fmt.Errorf("FinalizeEntry: entry nao encontrada: key=%q", p.IdempotencyKey)
		}
		if err2 != nil {
			return fmt.Errorf("FinalizeEntry: verificar status: %w", err2)
		}
		if status == string(EntryStatusPosted) {
			return nil // ja finalizado — idempotente
		}
		return fmt.Errorf("FinalizeEntry: status inesperado=%q para key=%q", status, p.IdempotencyKey)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Balance — saldo derivado de postings (nunca coluna armazenada)
// ---------------------------------------------------------------------------

// Balance retorna o saldo corrente de uma conta em minor-units como *big.Int.
//
// Derivado de SUM(debit_amount - credit_amount). NUNCA e coluna armazenada.
// Retorna 0 se a conta nao tiver postings.
func Balance(ctx context.Context, pool *pgxpool.Pool, tenantID, accountCode string) (*big.Int, error) {
	const sql = `
		SELECT COALESCE(SUM(p.debit_amount - p.credit_amount), 0)::text
		FROM   ledger.accounts a
		LEFT   JOIN ledger.postings p ON p.account_id = a.id
		WHERE  a.tenant_id = $1::uuid AND a.code = $2
		GROUP  BY a.id
	`
	var balStr string
	err := pool.QueryRow(ctx, sql, tenantID, accountCode).Scan(&balStr)
	if errors.Is(err, pgx.ErrNoRows) {
		return big.NewInt(0), nil
	}
	if err != nil {
		return nil, fmt.Errorf("Balance: %w", err)
	}
	return numericStringToBigInt(balStr), nil
}

// ---------------------------------------------------------------------------
// helpers internos
// ---------------------------------------------------------------------------

// checkBalance valida sum(debit)=sum(credit) por asset_code sem acesso ao banco.
func checkBalance(postings []PostingLine) error {
	type sums struct{ debit, credit int64 }
	byAsset := make(map[string]*sums)
	for _, pl := range postings {
		s := byAsset[pl.AssetCode]
		if s == nil {
			s = &sums{}
			byAsset[pl.AssetCode] = s
		}
		s.debit += pl.DebitMinorUnits
		s.credit += pl.CreditMinorUnits
	}
	for code, s := range byAsset {
		if s.debit != s.credit {
			return fmt.Errorf("%w: asset=%q debit=%d credit=%d",
				ErrUnbalancedPostings, code, s.debit, s.credit)
		}
	}
	return nil
}

// CheckBalanceForTest expoe a funcao de producao checkBalance para testes de
// balanco. checkBalance e unexported e o pacote de teste externo (ledger_test)
// nao a alcanca; este wrapper FINO delega diretamente, sem logica propria, para
// que TestCheckBalance_* exercite o caminho REAL (posting.go) — nao uma
// reimplementacao. checkBalance recebe []PostingLine puro, sem Postgres, entao
// o teste roda em memoria. Nao usar em codigo de producao.
func CheckBalanceForTest(postings []PostingLine) error {
	return checkBalance(postings)
}

// insertJournalEntry insere o cabecalho da entry e retorna o id gerado.
func insertJournalEntry(ctx context.Context, tx pgx.Tx, p RecordEntryParams) (int64, error) {
	const insertSQL = `
		INSERT INTO ledger.journal_entries
			(tenant_id, idempotency_key, description, status, effective_at,
			 ref_type, ref_id, metadata)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (tenant_id, idempotency_key) DO NOTHING
		RETURNING id
	`
	var id int64
	err := tx.QueryRow(ctx, insertSQL,
		p.TenantID,
		p.IdempotencyKey,
		p.Description,
		string(p.Status),
		p.EffectiveAt,
		nullableString(p.RefType),
		p.RefID,
		p.Metadata,
	).Scan(&id)

	if errors.Is(err, pgx.ErrNoRows) {
		// ON CONFLICT DO NOTHING nao retorna linha; busca o id existente.
		err2 := tx.QueryRow(ctx,
			`SELECT id FROM ledger.journal_entries WHERE tenant_id=$1::uuid AND idempotency_key=$2`,
			p.TenantID, p.IdempotencyKey,
		).Scan(&id)
		if err2 != nil {
			return 0, fmt.Errorf("insertJournalEntry: buscar existente: %w", err2)
		}
		return id, ErrIdempotentDuplicate
	}
	if err != nil {
		return 0, fmt.Errorf("insertJournalEntry: %w", err)
	}
	return id, nil
}

// insertPostings insere os postings de uma entry.
// Usa NUMERIC como string para evitar float (TX-2).
func insertPostings(ctx context.Context, tx pgx.Tx, entryID int64, postings []PostingLine) error {
	for _, pl := range postings {
		const sql = `
			INSERT INTO ledger.postings
				(journal_entry_id, tenant_id, account_id, asset_code, scale,
				 debit_amount, credit_amount, posted_at)
			SELECT
				$1,
				je.tenant_id,
				$3,
				$4,
				$5,
				$6::numeric,
				$7::numeric,
				now()
			FROM ledger.journal_entries je
			WHERE je.id = $1
			  AND je.tenant_id = (SELECT tenant_id FROM ledger.accounts WHERE id = $3)
		`
		debit := int64ToNumericStr(pl.DebitMinorUnits)
		credit := int64ToNumericStr(pl.CreditMinorUnits)

		tag, err := tx.Exec(ctx, sql,
			entryID,
			entryID,
			pl.AccountID,
			pl.AssetCode,
			pl.Scale,
			debit,
			credit,
		)
		if err != nil {
			return fmt.Errorf("insertPostings: account=%d asset=%s: %w", pl.AccountID, pl.AssetCode, err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("insertPostings: posting nao inserido (tenant mismatch?): account=%d", pl.AccountID)
		}
	}
	return nil
}

// int64ToNumericStr converte int64 para string NUMERIC exata (sem float).
func int64ToNumericStr(v int64) string {
	return big.NewInt(v).String()
}

// numericStringToBigInt converte string NUMERIC do Postgres para *big.Int.
// Remove a parte decimal se presente (assumindo minor-units inteiros).
func numericStringToBigInt(s string) *big.Int {
	for i, c := range s {
		if c == '.' {
			s = s[:i]
			break
		}
	}
	n := new(big.Int)
	n.SetString(s, 10)
	return n
}

// nullableString retorna nil se s for vazio, ou s caso contrario.
func nullableString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
