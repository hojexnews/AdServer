// Package ledger — recon.go
//
// Job de reconciliacao periodica (BILLING.md §7 / ADR-0004 §D).
//
// # Invariantes absolutas
//
//   - Fonte autoritativa: Iceberg (lakehouse). NUNCA streaming/ClickHouse.
//   - Divergencia -> abre excecao em ledger.reconciliation_exceptions.
//   - NUNCA autocorrige. Correcao exige posting de ajuste humano aprovado.
//   - Nenhuma coluna float (TX-2). Valores como *big.Int / string NUMERIC.
//   - PII/KYC proibida no ledger (DA-11).
package ledger

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------------------------------------------------------------------------
// Interface: fonte autoritativa de valores esperados
// ---------------------------------------------------------------------------

// PeriodExpected descreve o valor esperado para um (tenant, asset, periodo).
// TenantID e string (UUID em texto) — mesma convencao do projeto.
type PeriodExpected struct {
	TenantID    string    // UUID em texto
	AssetCode   string
	PeriodStart time.Time
	PeriodEnd   time.Time
	// ExpectedMinorUnits e o valor calculado pela fonte autoritativa (Iceberg).
	// *big.Int: sem float (TX-2).
	ExpectedMinorUnits *big.Int
}

// ExpectedValueSource e a interface para obter valores esperados de uma fonte
// autoritativa (Iceberg/lakehouse).
//
// NUNCA use ClickHouse ou qualquer fonte de streaming como implementacao.
type ExpectedValueSource interface {
	FetchExpected(ctx context.Context, periodStart, periodEnd time.Time) ([]PeriodExpected, error)
}

// ---------------------------------------------------------------------------
// Stub para testes
// ---------------------------------------------------------------------------

// ExpectedValueSourceStub e implementacao deterministica de ExpectedValueSource.
type ExpectedValueSourceStub struct {
	entries []PeriodExpected
}

// NewExpectedValueSourceStub cria um stub com os valores pre-carregados.
func NewExpectedValueSourceStub(entries []PeriodExpected) *ExpectedValueSourceStub {
	return &ExpectedValueSourceStub{entries: entries}
}

// FetchExpected implementa ExpectedValueSource.
func (s *ExpectedValueSourceStub) FetchExpected(_ context.Context, start, end time.Time) ([]PeriodExpected, error) {
	var out []PeriodExpected
	for _, e := range s.entries {
		if !e.PeriodEnd.Before(start) && !e.PeriodStart.After(end) {
			out = append(out, e)
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// OpenExceptionParamsExported — parametros exportados para a interface ReconStore
// Exportado para que stubs de teste (package ledger_test) implementem ReconStore.
// ---------------------------------------------------------------------------

// OpenExceptionParamsExported parametriza a abertura de uma excecao de reconciliacao.
// Exportado porque ReconStore e uma interface exportada.
type OpenExceptionParamsExported struct {
	TenantID           string // UUID em texto
	AssetCode          string
	PeriodStart        time.Time
	PeriodEnd          time.Time
	ExpectedMinorUnits *big.Int
	LedgerMinorUnits   *big.Int
}

// ---------------------------------------------------------------------------
// ReconStore: interface exportada de acesso ao banco para o Reconciler
// ---------------------------------------------------------------------------

// ReconStore e a interface de acesso ao banco que o Reconciler precisa.
// Exportada para permitir implementacoes de stub em testes externos.
//
// A implementacao de producao e pgReconStore (usa pgxpool.Pool).
// Em testes, use um struct que satisfaca esta interface.
type ReconStore interface {
	// SumLedgerPostings retorna SUM(debit_amount) como string NUMERIC para
	// o periodo. Retorna "0" se nao houver postings.
	SumLedgerPostings(ctx context.Context, tenantID, assetCode string, start, end time.Time) (string, error)

	// OpenException insere uma linha em ledger.reconciliation_exceptions.
	// Retorna ErrExceptionAlreadyOpen se ja existir (idempotente).
	OpenException(ctx context.Context, p OpenExceptionParamsExported) error
}

// ---------------------------------------------------------------------------
// Implementacao de producao: pgReconStore
// ---------------------------------------------------------------------------

type pgReconStore struct {
	pool *pgxpool.Pool
}

func (s *pgReconStore) SumLedgerPostings(ctx context.Context, tenantID, assetCode string, start, end time.Time) (string, error) {
	const sql = `
		SELECT COALESCE(SUM(p.debit_amount), 0)::text
		FROM   ledger.postings    p
		JOIN   ledger.journal_entries je ON je.id = p.journal_entry_id
		WHERE  je.tenant_id  = $1::uuid
		  AND  p.asset_code  = $2
		  AND  p.posted_at  >= $3
		  AND  p.posted_at   < $4
		  AND  je.status     = 'posted'
	`
	var sumStr string
	if err := s.pool.QueryRow(ctx, sql, tenantID, assetCode, start, end).Scan(&sumStr); err != nil {
		return "0", fmt.Errorf("SumLedgerPostings: %w", err)
	}
	return sumStr, nil
}

func (s *pgReconStore) OpenException(ctx context.Context, p OpenExceptionParamsExported) error {
	const sql = `
		INSERT INTO ledger.reconciliation_exceptions
			(tenant_id, asset_code, period_start, period_end,
			 expected_minor_units, ledger_minor_units, source, status)
		VALUES ($1::uuid, $2, $3, $4, $5::numeric, $6::numeric, 'iceberg', 'open')
		ON CONFLICT (tenant_id, asset_code, period_start, period_end) DO NOTHING
	`
	tag, err := s.pool.Exec(ctx, sql,
		p.TenantID,
		p.AssetCode,
		p.PeriodStart,
		p.PeriodEnd,
		p.ExpectedMinorUnits.String(),
		p.LedgerMinorUnits.String(),
	)
	if err != nil {
		return fmt.Errorf("OpenException: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrExceptionAlreadyOpen
	}
	return nil
}

// ---------------------------------------------------------------------------
// Reconciler
// ---------------------------------------------------------------------------

// Reconciler executa a reconciliacao periodica entre Iceberg e o ledger.
type Reconciler struct {
	db     ReconStore
	source ExpectedValueSource
}

// NewReconciler cria um Reconciler de producao com pgxpool.Pool.
// source: fonte autoritativa (Iceberg). NUNCA ClickHouse/streaming.
func NewReconciler(pool *pgxpool.Pool, source ExpectedValueSource) *Reconciler {
	return &Reconciler{
		db:     &pgReconStore{pool: pool},
		source: source,
	}
}

// NewReconcilerForTest cria um Reconciler com ReconStore injetavel para testes.
// Nao deve ser usado em producao — use NewReconciler.
func NewReconcilerForTest(db ReconStore, source ExpectedValueSource) *Reconciler {
	return &Reconciler{db: db, source: source}
}

// ReconcileParams parametriza uma execucao de reconciliacao.
type ReconcileParams struct {
	PeriodStart time.Time
	PeriodEnd   time.Time
}

// ReconcileResult descreve o resultado de uma reconciliacao.
type ReconcileResult struct {
	Checked    int // numero de (tenant, asset, periodo) verificados
	Diverged   int // novas divergencias abertas nesta execucao
	Exceptions []ExceptionOpened
}

// ExceptionOpened descreve uma excecao aberta nesta execucao.
type ExceptionOpened struct {
	TenantID             string // UUID em texto
	AssetCode            string
	PeriodStart          time.Time
	PeriodEnd            time.Time
	ExpectedMinorUnits   *big.Int
	LedgerMinorUnits     *big.Int
	DivergenceMinorUnits *big.Int // expected - ledger
}

// ErrExceptionAlreadyOpen e retornado quando a excecao para o mesmo periodo ja existe.
var ErrExceptionAlreadyOpen = errors.New("ledger: excecao ja aberta para este periodo (rerun idempotente)")

// Run executa a reconciliacao para o periodo indicado.
//
// Para cada (tenant, asset, periodo) da fonte Iceberg:
//  1. Calcula SUM(debit_amount) dos postings do ledger.
//  2. Compara com o valor esperado (Iceberg).
//  3. Se divergir, abre excecao. NUNCA grava postings de correcao.
func (r *Reconciler) Run(ctx context.Context, p ReconcileParams) (ReconcileResult, error) {
	expected, err := r.source.FetchExpected(ctx, p.PeriodStart, p.PeriodEnd)
	if err != nil {
		return ReconcileResult{}, fmt.Errorf("Reconciler.Run: fetch expected: %w", err)
	}

	res := ReconcileResult{Checked: len(expected)}
	for _, exp := range expected {
		sumStr, err := r.db.SumLedgerPostings(ctx, exp.TenantID, exp.AssetCode, exp.PeriodStart, exp.PeriodEnd)
		if err != nil {
			return res, fmt.Errorf("Reconciler.Run: sum ledger: %w", err)
		}
		ledgerAmt := numericStringToBigInt(sumStr)

		if exp.ExpectedMinorUnits.Cmp(ledgerAmt) == 0 {
			continue // em equilibrio
		}

		// Divergencia detectada: abre excecao. NUNCA autocorrige.
		divergence := new(big.Int).Sub(exp.ExpectedMinorUnits, ledgerAmt)
		openErr := r.db.OpenException(ctx, OpenExceptionParamsExported{
			TenantID:           exp.TenantID,
			AssetCode:          exp.AssetCode,
			PeriodStart:        exp.PeriodStart,
			PeriodEnd:          exp.PeriodEnd,
			ExpectedMinorUnits: exp.ExpectedMinorUnits,
			LedgerMinorUnits:   ledgerAmt,
		})
		if openErr != nil && !errors.Is(openErr, ErrExceptionAlreadyOpen) {
			return res, fmt.Errorf("Reconciler.Run: open exception: %w", openErr)
		}
		if openErr == nil {
			// Nova excecao aberta nesta execucao.
			res.Diverged++
			res.Exceptions = append(res.Exceptions, ExceptionOpened{
				TenantID:             exp.TenantID,
				AssetCode:            exp.AssetCode,
				PeriodStart:          exp.PeriodStart,
				PeriodEnd:            exp.PeriodEnd,
				ExpectedMinorUnits:   exp.ExpectedMinorUnits,
				LedgerMinorUnits:     ledgerAmt,
				DivergenceMinorUnits: divergence,
			})
		}
		// ErrExceptionAlreadyOpen: rerun idempotente, nao incrementa Diverged.
	}
	return res, nil
}
