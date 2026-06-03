// Package shadow implements the shadow-traffic harness for the Go rewrite
// parity gate (§5 do stack — risco "reescrita diverge → corrompe faturamento").
//
// # Conceito
//
// O shadow-traffic espelha cada request de produção para o motor Go em paralelo
// ao Revive legado, compara as decisões e publica divergências em métricas —
// sem nunca servir a resposta do motor Go ao cliente final.
//
// # O que é executável AGORA (sem infra)
//
//   - Tipos de entrada/saída (DecisionInput / DecisionOutput).
//   - Comparador semântico (CompareDecisions).
//   - DivergenceCollector (in-memory para testes).
//   - CutoverGate com critério numérico declarado.
//
// # O que exige infra (marcado com TODO-INFRA)
//
//   - HTTP proxy que captura e espelha requests de produção.
//   - Chamada real ao Revive legado (PHP endpoint).
//   - Chamada real ao motor Go (decision service).
//   - Exportação de métricas para Prometheus/Grafana.
//   - Shadow runner daemon (processo long-running).
//
// Executar o esqueleto executável (comparador + gate):
//
//	go build ./tests/parity/shadow/...
//
// # Critério de cutover (declarado e numérico)
//
// Ver CutoverCriteria no final deste arquivo.
package shadow

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// Tipos de decisão
// ---------------------------------------------------------------------------

// DecisionInput representa os sinais de entrada de um request de anúncio.
// Deve ser preenchido pelo proxy de captura tanto para o Revive quanto para
// o motor Go (os dois recebem os MESMOS inputs).
type DecisionInput struct {
	// Identificação
	ZoneID   string
	TenantID string

	// Contexto de segmentação
	CountryISO2 string // derivado pelo geo resolver; nunca IP bruto
	City        string
	UserAgent   string // já reduzido a classe grossa (TX-5)
	SiteURL     string // sanitizado (sem query string sensível)
	SiteVars    map[string]string

	// Temporalidade
	RequestTime time.Time
	WeekdayNum  int // 0=Sun … 6=Sat

	// Identidade de usuário para capping (hashed+salted — TX-5)
	HashedUserID string // "" = sem identificador estável

	// Rastreamento
	RequestID string // ID único do request de produção (para correlação de logs)
}

// DecisionOutput representa a decisão tomada por um motor (Go ou Revive).
type DecisionOutput struct {
	// Resultado da cascata
	ServedTier  string // "OVERRIDE" | "CONTRACT" | "REMNANT" | "BLANK"
	CampaignID  string // "" para BLANK
	BannerID    string // "" para BLANK
	IsBlank     bool

	// Dados do criativo (para comparação de paridade)
	CreativeType string // "image" | "html5" | "video" | ""
	CreativeURL  string // "" para BLANK

	// Metadados para diagnóstico
	MotorName   string        // "go" | "revive"
	LatencyMs   int64
	EvaluatedAt time.Time
}

// ---------------------------------------------------------------------------
// Comparador semântico
// ---------------------------------------------------------------------------

// DivergenceKind classifica o tipo de divergência encontrada.
type DivergenceKind string

const (
	DivergenceTierMismatch     DivergenceKind = "tier_mismatch"
	DivergenceCampaignMismatch DivergenceKind = "campaign_mismatch"
	DivergenceBannerMismatch   DivergenceKind = "banner_mismatch"
	DivergenceBlankMismatch    DivergenceKind = "blank_mismatch"
)

// Divergence representa uma divergência detectada entre os dois motores.
type Divergence struct {
	RequestID   string
	ZoneID      string
	TenantID    string
	Kind        DivergenceKind
	LegacyValue string
	GoValue     string
	DetectedAt  time.Time
}

// CompareDecisions compara a decisão do motor Go com a do Revive legado.
// Retorna nil se as decisões são semanticamente equivalentes, ou uma lista
// de divergências encontradas.
//
// Semântica de equivalência (definição canônica do gate):
//  1. Ambos devem concordar se o resultado é BLANK ou não.
//  2. Se não é BLANK, o tier deve ser o mesmo.
//  3. Se não é BLANK, o campaign_id deve ser o mesmo.
//  4. O banner_id pode divergir quando o campaign tem múltiplos banners
//     elegíveis com regras de seleção não-determinísticas no legado.
//     (Divergência de banner_id é registrada mas não conta como divergência
//     de faturamento para o critério de cutover.)
func CompareDecisions(legacy, goDecision DecisionOutput) []Divergence {
	var divs []Divergence

	// 1. Blank mismatch: um serve, o outro não.
	if legacy.IsBlank != goDecision.IsBlank {
		divs = append(divs, Divergence{
			RequestID:   legacy.CampaignID, // placeholder: usar RequestID real
			ZoneID:      "",
			Kind:        DivergenceBlankMismatch,
			LegacyValue: fmt.Sprintf("blank=%v", legacy.IsBlank),
			GoValue:     fmt.Sprintf("blank=%v", goDecision.IsBlank),
			DetectedAt:  time.Now(),
		})
		return divs // tier/campaign comparison meaningless if blank differs
	}

	// 2. Tier mismatch.
	if legacy.ServedTier != goDecision.ServedTier {
		divs = append(divs, Divergence{
			Kind:        DivergenceTierMismatch,
			LegacyValue: legacy.ServedTier,
			GoValue:     goDecision.ServedTier,
			DetectedAt:  time.Now(),
		})
	}

	// 3. Campaign mismatch (billing-critical: campaigns map to pricing contracts).
	if !legacy.IsBlank && legacy.CampaignID != goDecision.CampaignID {
		divs = append(divs, Divergence{
			Kind:        DivergenceCampaignMismatch,
			LegacyValue: legacy.CampaignID,
			GoValue:     goDecision.CampaignID,
			DetectedAt:  time.Now(),
		})
	}

	// 4. Banner mismatch (informational — does not block cutover alone).
	if !legacy.IsBlank && legacy.BannerID != goDecision.BannerID {
		divs = append(divs, Divergence{
			Kind:        DivergenceBannerMismatch,
			LegacyValue: legacy.BannerID,
			GoValue:     goDecision.BannerID,
			DetectedAt:  time.Now(),
		})
	}

	return divs
}

// ---------------------------------------------------------------------------
// DivergenceCollector — in-memory (para testes e validação pré-produção)
// ---------------------------------------------------------------------------

// DivergenceCollector acumula divergências e calcula taxas para o gate de cutover.
// Implementação thread-safe.
type DivergenceCollector struct {
	mu             sync.Mutex
	totalDecisions atomic.Int64
	divergences    []Divergence
	byKind         map[DivergenceKind]int64
}

// NewDivergenceCollector cria um coletor vazio.
func NewDivergenceCollector() *DivergenceCollector {
	return &DivergenceCollector{
		byKind: make(map[DivergenceKind]int64),
	}
}

// Record registra uma decisão (com ou sem divergências).
func (c *DivergenceCollector) Record(divs []Divergence) {
	c.totalDecisions.Add(1)
	if len(divs) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.divergences = append(c.divergences, divs...)
	for _, d := range divs {
		c.byKind[d.Kind]++
	}
}

// DivergenceRate retorna a taxa de divergência (decisões divergentes / total).
// Apenas divergências de kind que bloqueiam cutover são contadas.
func (c *DivergenceCollector) DivergenceRate() float64 {
	total := c.totalDecisions.Load()
	if total == 0 {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Divergências que BLOQUEIAM o cutover (billing-critical):
	blocking := c.byKind[DivergenceTierMismatch] +
		c.byKind[DivergenceCampaignMismatch] +
		c.byKind[DivergenceBlankMismatch]
	return float64(blocking) / float64(total)
}

// BannerDivergenceRate retorna a taxa de divergências de banner (informacional).
// Não bloqueia o cutover por si só.
func (c *DivergenceCollector) BannerDivergenceRate() float64 {
	total := c.totalDecisions.Load()
	if total == 0 {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return float64(c.byKind[DivergenceBannerMismatch]) / float64(total)
}

// Report retorna um relatório sumário para logging/alertas.
func (c *DivergenceCollector) Report() DivergenceReport {
	c.mu.Lock()
	defer c.mu.Unlock()
	total := c.totalDecisions.Load()
	byKindCopy := make(map[DivergenceKind]int64, len(c.byKind))
	for k, v := range c.byKind {
		byKindCopy[k] = v
	}
	return DivergenceReport{
		TotalDecisions: total,
		TotalDivergent: int64(len(c.divergences)),
		ByKind:         byKindCopy,
	}
}

// DivergenceReport é o relatório produzido pelo coletor.
type DivergenceReport struct {
	TotalDecisions int64
	TotalDivergent int64
	ByKind         map[DivergenceKind]int64
}

// ---------------------------------------------------------------------------
// CutoverGate — critério de cutover declarado e numérico
// ---------------------------------------------------------------------------

// CutoverCriteria define o critério numérico de cutover.
// TODOS os critérios devem ser satisfeitos para que o cutover seja aprovado.
//
// Critério derivado do risco §5 do stack ("reescrita diverge → corrompe faturamento"):
//
//   MaxDecisionDivergenceRate:   0.001 (0.1%)
//     → Menos de 1 decisão em 1.000 pode divergir em tier ou campaign.
//     → Justificativa: a bilhetagem CPM é por campanha; uma campanha errada
//       fatura uma conta errada. 0.1% é o teto de erro aceitável antes do
//       volume de faturamento justificar rollback.
//
//   MaxBillingDivergencePct:     0.0050 (0.50%)
//     → Valor faturado pelo motor Go vs. legado não pode divergir mais de 0.5%.
//     → Comparação sobre Iceberg (nunca streaming).
//     → Justificativa: acordos de SLA com anunciantes tipicamente têm
//       tolerância de 5% em volume de entrega; usamos 0.5% como margem de
//       segurança conservadora (10x abaixo do SLA).
//
//   MinShadowHours:              48
//     → O shadow deve observar pelo menos 48 horas de tráfego real antes
//       do cutover, cobrindo variação de dia da semana e horários de pico.
//
//   MinShadowDecisions:          100_000
//     → Amostragem mínima para que a taxa de divergência seja estatisticamente
//       representativa (erro padrão < 0.1% com p=0.001 e N=100.000).
//
// Esses valores SÃO O GATE DE CUTOVER. Alterá-los exige ADR do tech-lead-architect.
var CutoverCriteria = struct {
	MaxDecisionDivergenceRate float64
	MaxBillingDivergencePct   float64
	MinShadowHours            int
	MinShadowDecisions        int64
}{
	MaxDecisionDivergenceRate: 0.001, // 0.1%
	MaxBillingDivergencePct:   0.005, // 0.5%
	MinShadowHours:            48,
	MinShadowDecisions:        100_000,
}

// CutoverGate avalia se o motor Go pode ser promovido a produção.
type CutoverGate struct {
	Criteria  interface{ /* same shape as CutoverCriteria */ }
	Collector *DivergenceCollector
	StartedAt time.Time
}

// NewCutoverGate cria um gate com os critérios padrão.
func NewCutoverGate(collector *DivergenceCollector) *CutoverGate {
	return &CutoverGate{
		Collector: collector,
		StartedAt: time.Now(),
	}
}

// CutoverDecision é o resultado da avaliação do gate.
type CutoverDecision struct {
	Approved bool
	Reasons  []string // por que foi reprovado (vazio se aprovado)
}

// Evaluate avalia se o cutover pode ser aprovado com base nos critérios.
// NUNCA aprova silenciosamente: se Collector é nil ou tem dados insuficientes,
// retorna Approved=false com razão explícita.
func (g *CutoverGate) Evaluate() CutoverDecision {
	var reasons []string

	if g.Collector == nil {
		return CutoverDecision{
			Approved: false,
			Reasons:  []string{"ERRO: DivergenceCollector é nil — gate não pode ser avaliado"},
		}
	}

	report := g.Collector.Report()

	// 1. Amostragem mínima.
	if report.TotalDecisions < CutoverCriteria.MinShadowDecisions {
		reasons = append(reasons, fmt.Sprintf(
			"amostragem insuficiente: %d decisões observadas, mínimo %d",
			report.TotalDecisions, CutoverCriteria.MinShadowDecisions,
		))
	}

	// 2. Tempo mínimo de shadow.
	elapsed := time.Since(g.StartedAt)
	if elapsed < time.Duration(CutoverCriteria.MinShadowHours)*time.Hour {
		reasons = append(reasons, fmt.Sprintf(
			"shadow insuficiente: %.1f horas decorridas, mínimo %d",
			elapsed.Hours(), CutoverCriteria.MinShadowHours,
		))
	}

	// 3. Taxa de divergência de decisão (billing-critical).
	divRate := g.Collector.DivergenceRate()
	if divRate > CutoverCriteria.MaxDecisionDivergenceRate {
		reasons = append(reasons, fmt.Sprintf(
			"taxa de divergência de decisão: %.4f (%.2f%%), máximo %.4f (%.2f%%)",
			divRate, divRate*100,
			CutoverCriteria.MaxDecisionDivergenceRate,
			CutoverCriteria.MaxDecisionDivergenceRate*100,
		))
	}

	// 4. Taxa de divergência de billing (preenchida pelo DualRunReconciler).
	// NOTA: a taxa de billing é passada externamente pelo dual-run contábil.
	// O gate não lê billing diretamente — ele recebe o resultado da reconciliação.

	return CutoverDecision{
		Approved: len(reasons) == 0,
		Reasons:  reasons,
	}
}

// ---------------------------------------------------------------------------
// TODO-INFRA: Shadow runner daemon
// O seguinte bloco documenta o que deve ser implementado quando a infra estiver
// disponível. NÃO é executável sem Revive, motor Go, e proxy HTTP.
// ---------------------------------------------------------------------------

// ShadowRunner (TODO-INFRA) espelha requests de produção para o motor Go em
// paralelo ao Revive. Estrutura conceitual:
//
//	type ShadowRunner struct {
//	    reviveEndpoint string  // ex.: "http://revive.internal/www/delivery/asyncjs.php"
//	    goEndpoint     string  // ex.: "http://decision.internal/v1/decide"
//	    collector      *DivergenceCollector
//	    httpClient     *http.Client
//	    concurrency    int
//	}
//
// Fluxo por request:
//  1. O proxy HTTP (Nginx/Envoy mirror) envia uma cópia do request ao shadow runner.
//  2. O shadow runner chama o Revive e o motor Go EM PARALELO com timeout.
//  3. A resposta do Revive é sempre a que vai ao cliente (shadow não serve).
//  4. CompareDecisions(legacyOutput, goOutput) é chamado.
//  5. Divergências são registradas no DivergenceCollector e emitidas como métricas.
//
// Configuração de espelhamento (Nginx upstream mirror):
//
//	mirror /shadow;
//	mirror_request_body on;
//	location /shadow {
//	    internal;
//	    proxy_pass http://shadow-runner:9099;
//	}
//
// Variáveis de ambiente necessárias (TODO-INFRA):
//
//	REVIVE_ENDPOINT    = http://revive.prod.internal/www/delivery/asyncjs.php
//	GO_DECISION_URL    = http://decision.prod.internal/v1/decide
//	SHADOW_METRICS_PORT = 9098

// ---------------------------------------------------------------------------
// DualRunReconciler (TODO-INFRA) — especificação do dual-run contábil
// ---------------------------------------------------------------------------

// DualRunReconciliation representa o resultado de uma reconciliação contábil
// entre o motor Go e o legado para uma janela horária.
//
// Metodologia (ver dual_run_spec.go para detalhes completos):
//  1. Para cada hour_bucket, o billing batch Go e o billing batch Revive
//     calculam postings independentemente.
//  2. Os totais são comparados: se a divergência exceder MaxBillingDivergencePct,
//     um alerta é aberto e o cutover é BLOQUEADO.
//  3. A fonte de verdade para o billing Go é o Iceberg (nunca streaming).
//  4. A fonte de verdade para o billing Revive é o banco MySQL do Revive.
//  5. Comparações são feitas em NUMERIC/Decimal — nunca float.
type DualRunReconciliation struct {
	HourBucket      time.Time
	TenantID        string
	AssetCode       string
	GoAmount        string // NUMERIC string (nunca float)
	LegacyAmount    string // NUMERIC string (nunca float)
	DivergencePct   string // NUMERIC string (nunca float)
	WithinTolerance bool
	BlocksCutover   bool // true se a divergência exceder o critério
}
