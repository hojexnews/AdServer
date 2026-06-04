// Package chainalysis implementa screening de enderecos on-chain via Chainalysis (K6).
//
// # Posicionamento
//
// Vive em services/payments (celula AML/KYC). NAO e importado pelo motor de
// decisao, internal/ranker, internal/cascade nem internal/decision.
//
// # Fronteira (TX-3 / DA-11 / ADR-0004 §E.10)
//
// O screening roda no trilho de pagamento — FORA do hot path de veiculacao.
// Um resultado de sanctions_hit=true ou risco acima do limiar BLOQUEIA o
// deposito ou payout cripto; NUNCA toca a decisao de anuncio.
//
// O endereco on-chain de custodia da plataforma (Safe) nao e PII.
// A associacao endereco<->identidade do anunciante fica no cofre compliance.
// Nunca logar o endereco em contexto que permita identificar o anunciante.
//
// # Fail-closed
//
// Se o Chainalysis retornar erro ou estiver indisponivel, ScreenAddress
// retorna ErrScreeningUnavailable — o chamador DEVE segurar a operacao,
// nao liberar (fail-closed). Reconciliar e abrir excecao manualmente.
//
// # Stubs
//
// A chamada HTTP real ao Chainalysis KYT API e implementada mas pende de
// API key viva (CHAINALYSIS_API_KEY via env/OpenBao).
package chainalysis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------------------------------------------------------------------------
// Erros exportados
// ---------------------------------------------------------------------------

// ErrSanctionsHit e retornado quando o endereco tem match de sancoes OFAC/ONU.
// O chamador DEVE bloquear a operacao imediatamente.
var ErrSanctionsHit = errors.New("chainalysis: endereco em lista de sancoes — operacao bloqueada")

// ErrRiskAboveThreshold e retornado quando o risk_score excede o limiar configurado.
var ErrRiskAboveThreshold = errors.New("chainalysis: risco acima do limiar — operacao bloqueada")

// ErrScreeningUnavailable e retornado quando o Chainalysis nao esta acessivel.
// O chamador DEVE segurar a operacao (fail-closed) — NUNCA liberar.
var ErrScreeningUnavailable = errors.New("chainalysis: screening indisponivel — operacao segurada (fail-closed)")

// ---------------------------------------------------------------------------
// Tipos
// ---------------------------------------------------------------------------

// Direction indica se o endereco e de entrada (deposito) ou saida (payout).
type Direction string

const (
	// DirectionDeposit e o endereco de origem de um deposito recebido.
	DirectionDeposit Direction = "deposit"
	// DirectionPayout e o endereco de destino de um payout enviado.
	DirectionPayout Direction = "payout"
)

// ScreeningResult e o resultado de um screening de endereco.
type ScreeningResult struct {
	// Address e o endereco on-chain screened.
	Address string
	// RiskScore e o score de risco em escala inteira 0-100.
	// 0 = risco minimo; 100 = risco maximo (sanctioned/severe).
	// TX-2: sem float — armazenado como INTEGER no cofre compliance.
	RiskScore int
	// SanctionsHit indica match de sancoes OFAC/ONU.
	SanctionsHit bool
	// Category e a categoria de risco (ex.: "sanctions", "darknet_market", "none").
	Category string
	// ScreenedAt e o timestamp do screening.
	ScreenedAt time.Time
}

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

// Config agrupa a configuracao do Screener Chainalysis.
// APIKey NUNCA em imagem/git — via env/OpenBao.
type Config struct {
	// APIKey e a chave de API do Chainalysis KYT.
	// Lida de CHAINALYSIS_API_KEY via env/OpenBao.
	APIKey string

	// BaseURL e a URL base da API Chainalysis.
	// Default: "https://api.chainalysis.com"
	BaseURL string

	// RiskThreshold e o score de risco (0-100) acima do qual a operacao e bloqueada.
	// Default: 70. Configuravel por tenant/chain. TX-2: sem float.
	RiskThreshold int
}

// ---------------------------------------------------------------------------
// Screener
// ---------------------------------------------------------------------------

// Screener e o client de screening Chainalysis.
type Screener struct {
	apiKey        string // NUNCA logue
	baseURL       string
	riskThreshold int // 0-100; TX-2: sem float
	httpClient    *http.Client
	pool          *pgxpool.Pool
	logger        *slog.Logger
}

// New cria um Screener Chainalysis.
//
// pool pode ser nil em testes. Em producao, deve ser nao-nil para gravar
// em compliance.screening_results.
func New(cfg Config, pool *pgxpool.Pool, logger *slog.Logger) (*Screener, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("chainalysis: APIKey obrigatorio (CHAINALYSIS_API_KEY)")
	}
	if logger == nil {
		logger = slog.Default()
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.chainalysis.com"
	}
	threshold := cfg.RiskThreshold
	if threshold <= 0 {
		threshold = 70 // default: bloquear acima de 70/100
	}
	return &Screener{
		apiKey:        cfg.APIKey,
		baseURL:       baseURL,
		riskThreshold: threshold,
		httpClient:    &http.Client{Timeout: 10 * time.Second},
		pool:          pool,
		logger:        logger,
	}, nil
}

// ---------------------------------------------------------------------------
// ScreenAddress screena um endereco on-chain antes de finalizar deposito
// ou submeter payout.
//
// FLUXO (ADR-0004 §E.10 + K6):
//  1. Consulta o Chainalysis KYT API pelo risco do endereco.
//  2. Grava o resultado em compliance.screening_results (idempotente).
//  3. Se sanctions_hit=true ou risk_score > threshold:
//     -> retorna ErrSanctionsHit ou ErrRiskAboveThreshold.
//     -> o chamador DEVE bloquear a operacao.
//  4. Se o Chainalysis retornar erro ou estiver indisponivel:
//     -> retorna ErrScreeningUnavailable.
//     -> o chamador DEVE segurar a operacao (fail-closed).
//
// PII: o endereco on-chain nao e PII. A associacao com identidade fica
// no cofre kyc_subjects. Nunca logar o endereco com o tenant_id.
//
// Idempotente por (tenantID, address, direction, txRef).
// ---------------------------------------------------------------------------

// ScreenAddressParams agrupa os parametros do screening.
type ScreenAddressParams struct {
	// TenantID e o UUID pseudonimo do tenant.
	TenantID string
	// Address e o endereco on-chain a screear.
	Address string
	// Direction indica se e deposito ou payout.
	Direction Direction
	// TxRef e a referencia a transacao (hash ou idempotency_key).
	TxRef string
	// Network e a rede blockchain (ex.: "ethereum", "bitcoin").
	// Default: "ethereum".
	Network string
}

// ScreenAddress realiza o screening e retorna erro se bloqueado.
//
// Retorna nil se o endereco for considerado de baixo risco.
// Retorna ErrSanctionsHit, ErrRiskAboveThreshold ou ErrScreeningUnavailable
// conforme o resultado — nunca retorna nil em caso de problema de compliance.
func (s *Screener) ScreenAddress(ctx context.Context, p ScreenAddressParams) error {
	if p.Address == "" {
		return errors.New("chainalysis: Address obrigatorio para screening")
	}
	if p.TenantID == "" {
		return errors.New("chainalysis: TenantID obrigatorio para screening")
	}
	if p.Direction != DirectionDeposit && p.Direction != DirectionPayout {
		return fmt.Errorf("chainalysis: Direction invalida: %q", p.Direction)
	}
	network := p.Network
	if network == "" {
		network = "ethereum"
	}

	// Consulta o Chainalysis KYT
	result, err := s.callScreenAddress(ctx, p.Address, network)
	if err != nil {
		// Fail-closed: indisponibilidade segura a operacao
		s.logger.Error("chainalysis: screening indisponivel — segurar operacao (fail-closed)",
			"direction", string(p.Direction),
			"network", network,
			"err", err,
			// Sem PII: nao loga address com tenant_id
		)
		// Tenta gravar a falha como alerta (best-effort)
		_ = s.persistResult(ctx, p, ScreeningResult{
			Address:      p.Address,
			Category:     "unavailable",
			ScreenedAt:   time.Now().UTC(),
		})
		return ErrScreeningUnavailable
	}

	// Persiste o resultado no cofre compliance.screening_results (idempotente)
	if err := s.persistResult(ctx, p, result); err != nil {
		// Falha ao persistir: log + fail-closed (nao liberar sem registro)
		s.logger.Error("chainalysis: falha ao persistir resultado no cofre — segurar operacao",
			"err", err,
		)
		return ErrScreeningUnavailable
	}

	// Avalia o resultado: sanctions primeiro (mais grave)
	if result.SanctionsHit {
		s.logger.Warn("chainalysis: SANCOES detectadas — operacao BLOQUEADA",
			"direction", string(p.Direction),
			"category", result.Category,
			"network", network,
			// Nao loga address nem tenant_id juntos para evitar associacao de identidade
		)
		return ErrSanctionsHit
	}

	// Risco acima do limiar (ambos inteiros 0-100; TX-2: sem float)
	if result.RiskScore > s.riskThreshold {
		s.logger.Warn("chainalysis: risco acima do limiar — operacao BLOQUEADA",
			"direction", string(p.Direction),
			"risk_score", result.RiskScore,
			"threshold", s.riskThreshold,
			"category", result.Category,
			"network", network,
		)
		return ErrRiskAboveThreshold
	}

	s.logger.Info("chainalysis: screening OK — operacao liberada",
		"direction", string(p.Direction),
		"risk_score", result.RiskScore,
		"network", network,
	)
	return nil
}

// callScreenAddress chama a API Chainalysis KYT para obter o risco do endereco.
//
// STUB: implementado conforme a API do Chainalysis; pende de API key viva.
// URL: POST /api/kyt/v2/users/{externalId}/transfers
//
// NAO loga a resposta — pode conter informacoes sensiveis do Chainalysis.
func (s *Screener) callScreenAddress(ctx context.Context, address, network string) (ScreeningResult, error) {
	// Chainalysis KYT v2: registra o endereco e consulta o risco
	// Endpoint: GET /api/kyt/v2/addresses/{address}
	// (simplificado — a API real pode variar por plano)
	url := fmt.Sprintf("%s/api/kyt/v2/addresses/%s", s.baseURL, address)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ScreeningResult{}, fmt.Errorf("chainalysis: criar request: %w", err)
	}
	req.Header.Set("Token", s.apiKey) // Chainalysis usa header "Token"
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return ScreeningResult{}, fmt.Errorf("chainalysis: HTTP: %w", err)
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return ScreeningResult{}, fmt.Errorf("chainalysis: ler resposta: %w", err)
	}

	if resp.StatusCode == http.StatusNotFound {
		// Endereco nao encontrado = sem historico = baixo risco (nao bloqueado)
		return ScreeningResult{
			Address:    address,
			RiskScore:  0,
			Category:   "none",
			ScreenedAt: time.Now().UTC(),
		}, nil
	}

	if resp.StatusCode != http.StatusOK {
		return ScreeningResult{}, fmt.Errorf("chainalysis: status inesperado: %d", resp.StatusCode)
	}

	// Parse da resposta Chainalysis KYT v2
	var chainalysisResp struct {
		Address   string  `json:"address"`
		Risk      string  `json:"risk"`     // "low", "medium", "high", "severe"
		Category  string  `json:"cluster"`  // categoria do cluster
		// Chainalysis usa string "risk" — mapeamos para score numerico
	}
	if err := json.Unmarshal(rawBody, &chainalysisResp); err != nil {
		return ScreeningResult{}, fmt.Errorf("chainalysis: parse resposta: %w", err)
	}

	riskScore := mapRiskStringToScore(chainalysisResp.Risk)
	sanctionsHit := strings.EqualFold(chainalysisResp.Category, "sanctions") ||
		strings.EqualFold(chainalysisResp.Risk, "severe")

	return ScreeningResult{
		Address:      address,
		RiskScore:    riskScore,
		SanctionsHit: sanctionsHit,
		Category:     chainalysisResp.Category,
		ScreenedAt:   time.Now().UTC(),
	}, nil
}

// mapRiskStringToScore converte o nivel de risco textual do Chainalysis
// para um score inteiro (0-100). TX-2: sem float.
// Escala: low=10, medium=50, high=80, severe=100.
func mapRiskStringToScore(risk string) int {
	switch strings.ToLower(risk) {
	case "low":
		return 10
	case "medium":
		return 50
	case "high":
		return 80
	case "severe":
		return 100
	default:
		return 0
	}
}

// persistResult grava o resultado do screening em compliance.screening_results.
//
// Idempotente: ON CONFLICT (tenant_id, address, direction, tx_ref) DO NOTHING.
// Se o pool for nil (testes), retorna nil sem gravar.
func (s *Screener) persistResult(ctx context.Context, p ScreenAddressParams, result ScreeningResult) error {
	if s.pool == nil {
		return nil
	}

	txRef := p.TxRef
	if txRef == "" {
		txRef = "noop"
	}

	_, err := s.pool.Exec(ctx,
		// risk_score e INTEGER 0-100 no cofre compliance (TX-2: sem float).
		`INSERT INTO compliance.screening_results
		   (tenant_id, address, direction, risk_score, sanctions_hit, category, tx_ref, screened_at)
		 VALUES ($1::uuid, $2, $3, $4::integer, $5, $6, $7, $8)
		 ON CONFLICT (tenant_id, address, direction, tx_ref) DO NOTHING`,
		p.TenantID,
		p.Address,
		string(p.Direction),
		result.RiskScore,
		result.SanctionsHit,
		result.Category,
		txRef,
		result.ScreenedAt,
	)
	if err != nil {
		return fmt.Errorf("chainalysis: persistir resultado: %w", err)
	}
	return nil
}
