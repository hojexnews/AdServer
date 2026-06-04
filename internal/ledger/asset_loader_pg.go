// Package ledger — asset_loader_pg.go
//
// Implementacao Postgres do AssetLoader.
// Le os metadados de ativos de asset_registry.assets via pgxpool.Pool.
//
// Invariantes:
//   - enabled=false -> ErrAssetDisabled (AEV/BND com scale TBD ficam bloqueados).
//   - scale NULL     -> ErrAssetDisabled (CHECK assets_enabled_needs_scale_chk
//     do schema ja impede enabled=true com scale NULL, mas protegemos aqui tambem).
//   - Ativo ausente  -> ErrAssetNotFound.
//   - Float proibido: scale e int32, nenhum float transitado (TX-2).
package ledger

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PgAssetLoader e a implementacao de producao de AssetLoader.
// Le de asset_registry.assets via pgxpool.Pool.
// Thread-safe: pgxpool ja e concorrente.
type PgAssetLoader struct {
	pool *pgxpool.Pool
}

// NewPgAssetLoader cria um PgAssetLoader a partir de um pool de conexoes.
// Combine com NewAssetCache para evitar round-trips ao banco por posting.
//
//	loader := ledger.NewAssetCache(ledger.NewPgAssetLoader(pool), 5*time.Minute)
func NewPgAssetLoader(pool *pgxpool.Pool) *PgAssetLoader {
	return &PgAssetLoader{pool: pool}
}

// LoadAsset implementa AssetLoader buscando no asset_registry.assets.
//
// Retorna ErrAssetNotFound se o code nao existir.
// Retorna ErrAssetDisabled se enabled=false ou scale for NULL.
func (l *PgAssetLoader) LoadAsset(ctx context.Context, code string) (Asset, error) {
	const sql = `
		SELECT code, scale, enabled
		FROM   asset_registry.assets
		WHERE  code = $1
		LIMIT  1
	`
	var a Asset
	var scaleVal *int32 // scale pode ser NULL (AEV/BND pre-spec)
	var enabled bool

	err := l.pool.QueryRow(ctx, sql, code).Scan(&a.Code, &scaleVal, &enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return Asset{}, fmt.Errorf("%w: code=%q", ErrAssetNotFound, code)
	}
	if err != nil {
		return Asset{}, fmt.Errorf("PgAssetLoader.LoadAsset: %w", err)
	}

	// scale NULL ou enabled=false: ativo nao operacional.
	if !enabled || scaleVal == nil {
		return Asset{}, fmt.Errorf("%w: code=%q", ErrAssetDisabled, code)
	}

	a.Scale = *scaleVal
	a.Enabled = true
	return a, nil
}
