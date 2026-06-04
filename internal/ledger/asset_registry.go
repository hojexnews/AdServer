package ledger

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Asset representa os metadados de um ativo conforme o Asset Registry
// (asset_registry.assets). Apenas os campos relevantes para o ledger.
type Asset struct {
	Code    string
	Scale   int32 // casas decimais autoritativas (DA-10)
	Enabled bool
}

// ErrAssetDisabled e retornado quando se tenta operar com um ativo
// cujo enabled=false no Asset Registry (ex.: AEV/BND com scale TBD).
// Invariante §3 q.2: nunca aceitar token sem scale definido (enabled implica scale != NULL).
var ErrAssetDisabled = errors.New("ledger: ativo desabilitado no Asset Registry (scale TBD ou enabled=false)")

// ErrAssetNotFound e retornado quando o asset_code nao existe no Registry.
var ErrAssetNotFound = errors.New("ledger: asset_code nao encontrado no Asset Registry")

// ErrScaleMismatch e retornado quando o scale do posting diverge do Registry.
// Este e o dado mais critico (DA-10 / §3 q.2) — divergencia = erro imediato.
var ErrScaleMismatch = errors.New("ledger: scale do posting diverge do Asset Registry")

// AssetLoader e a interface para buscar metadados de ativos.
// A implementacao concreta le do Postgres (asset_registry.assets).
// Em testes, usa-se o AssetLoaderStub.
type AssetLoader interface {
	// LoadAsset retorna os metadados do ativo pelo code.
	// Retorna ErrAssetNotFound se o code nao existir.
	LoadAsset(ctx context.Context, code string) (Asset, error)
}

// ---------------------------------------------------------------------------
// AssetLoaderStub — implementacao deterministica para testes
// ---------------------------------------------------------------------------

// AssetLoaderStub e a implementacao stub de AssetLoader para testes.
// Nao faz acesso a banco de dados.
type AssetLoaderStub struct {
	mu     sync.RWMutex
	assets map[string]Asset
}

// NewAssetLoaderStub cria um stub com os ativos fornecidos.
func NewAssetLoaderStub(assets []Asset) *AssetLoaderStub {
	s := &AssetLoaderStub{assets: make(map[string]Asset, len(assets))}
	for _, a := range assets {
		s.assets[a.Code] = a
	}
	return s
}

// LoadAsset implementa AssetLoader.
func (s *AssetLoaderStub) LoadAsset(_ context.Context, code string) (Asset, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.assets[code]
	if !ok {
		return Asset{}, fmt.Errorf("%w: code=%q", ErrAssetNotFound, code)
	}
	return a, nil
}

// SetAsset substitui ou adiciona um ativo no stub (util para testes).
func (s *AssetLoaderStub) SetAsset(a Asset) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.assets[a.Code] = a
}

// ---------------------------------------------------------------------------
// AssetCache — cache em memoria com TTL sobre qualquer AssetLoader
// ---------------------------------------------------------------------------

// cachedAsset armazena Asset + timestamp de expiracao.
type cachedAsset struct {
	asset   Asset
	expires time.Time
}

// AssetCache e um cache em memoria com TTL sobre qualquer AssetLoader.
// Evita consultas repetidas ao Postgres por asset_code em cada posting.
// Thread-safe.
type AssetCache struct {
	mu      sync.RWMutex
	inner   AssetLoader
	ttl     time.Duration
	entries map[string]cachedAsset
}

// NewAssetCache cria um AssetCache com TTL sobre o loader fornecido.
// ttl define por quanto tempo um asset e considerado valido sem revalidar.
// Um ttl de 0 desativa o cache (sempre revalida).
func NewAssetCache(inner AssetLoader, ttl time.Duration) *AssetCache {
	return &AssetCache{
		inner:   inner,
		ttl:     ttl,
		entries: make(map[string]cachedAsset),
	}
}

// LoadAsset implementa AssetLoader com cache.
func (c *AssetCache) LoadAsset(ctx context.Context, code string) (Asset, error) {
	now := time.Now()
	c.mu.RLock()
	entry, ok := c.entries[code]
	c.mu.RUnlock()
	if ok && (c.ttl == 0 || now.Before(entry.expires)) {
		return entry.asset, nil
	}
	a, err := c.inner.LoadAsset(ctx, code)
	if err != nil {
		return Asset{}, err
	}
	exp := now.Add(c.ttl)
	if c.ttl == 0 {
		exp = now // sem cache
	}
	c.mu.Lock()
	c.entries[code] = cachedAsset{asset: a, expires: exp}
	c.mu.Unlock()
	return a, nil
}

// validateAsset verifica que o ativo esta enabled e que o scale bate.
// Retorna ErrAssetDisabled se enabled=false (AEV/BND com scale TBD).
// Retorna ErrScaleMismatch se postingScale != Registry.Scale.
func validateAsset(ctx context.Context, loader AssetLoader, code string, postingScale int32) error {
	a, err := loader.LoadAsset(ctx, code)
	if err != nil {
		return err
	}
	if !a.Enabled {
		return fmt.Errorf("%w: code=%q", ErrAssetDisabled, code)
	}
	if a.Scale != postingScale {
		return fmt.Errorf("%w: code=%q registry=%d posting=%d",
			ErrScaleMismatch, code, a.Scale, postingScale)
	}
	return nil
}
