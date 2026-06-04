package ledger

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AccountKind espelha ledger.account_kind do Postgres.
type AccountKind string

const (
	AccountKindAsset     AccountKind = "asset"
	AccountKindLiability AccountKind = "liability"
	AccountKindRevenue   AccountKind = "revenue"
	AccountKindExpense   AccountKind = "expense"
	AccountKindEquity    AccountKind = "equity"
)

// Account representa uma conta do plano de contas (ledger.accounts).
// TenantID e tratado como string (UUID em texto) — mesma convencao do resto do projeto.
type Account struct {
	ID        int64
	TenantID  string
	Code      string
	Name      string
	Kind      AccountKind
	AssetCode string
}

// EnsureAccountParams descreve os parametros para provisao idempotente de conta.
type EnsureAccountParams struct {
	TenantID  string // UUID em texto
	Code      string // ex.: "adv:123:USDC", "platform:revenue:USDC"
	Name      string
	Kind      AccountKind
	AssetCode string
	Scale     int32 // scale do ativo (validado contra Asset Registry)
}

// EnsureAccount cria ou retorna a conta existente (idempotente).
//
// O asset_code DEVE estar enabled=true no Asset Registry e o scale DEVE bater
// com o valor do Registry. Se o ativo estiver disabled (ex.: AEV/BND com
// scale TBD), retorna ErrAssetDisabled imediatamente sem tocar o banco.
//
// Uso: provisao de contas cripto no boot ou na primeira operacao por tenant.
func EnsureAccount(ctx context.Context, pool *pgxpool.Pool, loader AssetLoader, p EnsureAccountParams) (Account, error) {
	if err := validateAsset(ctx, loader, p.AssetCode, p.Scale); err != nil {
		return Account{}, fmt.Errorf("EnsureAccount: %w", err)
	}

	// INSERT ... ON CONFLICT DO NOTHING; depois SELECT para obter o id.
	const insertSQL = `
		INSERT INTO ledger.accounts (tenant_id, code, name, kind, asset_code)
		VALUES ($1::uuid, $2, $3, $4, $5)
		ON CONFLICT (tenant_id, code) DO NOTHING
	`
	_, err := pool.Exec(ctx, insertSQL,
		p.TenantID, p.Code, p.Name, string(p.Kind), p.AssetCode)
	if err != nil {
		return Account{}, fmt.Errorf("EnsureAccount: insert: %w", err)
	}

	return fetchAccount(ctx, pool, p.TenantID, p.Code)
}

// fetchAccount busca uma conta pelo (tenant_id, code).
func fetchAccount(ctx context.Context, pool *pgxpool.Pool, tenantID, code string) (Account, error) {
	const selectSQL = `
		SELECT id, tenant_id::text, code, name, kind::text, asset_code
		FROM ledger.accounts
		WHERE tenant_id = $1::uuid AND code = $2
	`
	row := pool.QueryRow(ctx, selectSQL, tenantID, code)
	var a Account
	var kindStr string
	err := row.Scan(&a.ID, &a.TenantID, &a.Code, &a.Name, &kindStr, &a.AssetCode)
	a.Kind = AccountKind(kindStr)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, fmt.Errorf("ledger: conta nao encontrada: tenant=%s code=%s", tenantID, code)
	}
	if err != nil {
		return Account{}, fmt.Errorf("ledger: fetchAccount: %w", err)
	}
	return a, nil

}

// ---------------------------------------------------------------------------
// Plano de contas canonico (BILLING.md §2)
// ---------------------------------------------------------------------------

// AccountCodes retorna os codigos canonicos de contas para um advertiser + asset.
// Os codigos seguem BILLING.md §2:
//
//   - adv:{advertiser_id}:{asset}      — saldo pre-pago do anunciante (liability)
//   - platform:revenue:{asset}         — receita da plataforma (revenue)
//   - platform:cash:{asset}            — caixa recebido (asset)
//   - platform:receivable:{asset}      — contas a receber (asset)
//   - rounding:{asset}                 — restos de arredondamento (expense)
func AccountCodes(advertiserID, assetCode string) AccountCodeSet {
	return AccountCodeSet{
		AdvBalance:         fmt.Sprintf("adv:%s:%s", advertiserID, assetCode),
		PlatformRevenue:    fmt.Sprintf("platform:revenue:%s", assetCode),
		PlatformCash:       fmt.Sprintf("platform:cash:%s", assetCode),
		PlatformReceivable: fmt.Sprintf("platform:receivable:%s", assetCode),
		Rounding:           fmt.Sprintf("rounding:%s", assetCode),
	}
}

// AccountCodeSet agrupa os codigos canonicos de contas para um par (advertiser, asset).
type AccountCodeSet struct {
	AdvBalance         string // "adv:{advertiser_id}:{asset}"
	PlatformRevenue    string // "platform:revenue:{asset}"
	PlatformCash       string // "platform:cash:{asset}"
	PlatformReceivable string // "platform:receivable:{asset}"
	Rounding           string // "rounding:{asset}"
}
