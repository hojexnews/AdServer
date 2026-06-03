// Package dual_run especifica e esqueleta o dual-run contábil.
//
// # Mandato
//
// Provar que o faturamento Go e o legado Revive concordam dentro de tolerância
// declarada antes de qualquer cutover. Divergência acima da tolerância BLOQUEIA
// o cutover.
//
// # Fonte de verdade
//
// Faturamento Go  → Iceberg (adserver.billing_hourly via billing_batch_hourly.py)
// Faturamento Rev → MySQL do Revive (tabela `adx_stats_hourly` ou equivalente)
//
// INVARIANTE: nunca comparar contra ClickHouse (streaming) para faturamento.
// O ClickHouse é usado APENAS para reconciliação diagnóstica (CA-6 §5 bullet 4).
// A tabela de billing definitiva é o Iceberg (ADR-0001).
//
// # O que é executável AGORA
//
//   - BillingRecord e seus invariantes (tipo Decimal, nunca float).
//   - ReconcileHour() com lógica de comparação.
//   - DualRunReport com critério numérico.
//
// # O que requer infra (TODO-INFRA)
//
//   - Leitura real do Iceberg (PyIceberg ou Go Iceberg client).
//   - Leitura real do MySQL do Revive.
//   - Execução paralela dos dois billing batches.
//   - Exportação de relatórios para Grafana/PagerDuty.
//
// # Tolerância declarada
//
// Ver BillingTolerancePct no final deste arquivo.
// Alteração exige ADR do tech-lead-architect.
package dual_run

import (
	"errors"
	"fmt"
	"time"
)

// ---------------------------------------------------------------------------
// Tipos básicos — sem float (TX-2/DA-10)
// ---------------------------------------------------------------------------

// BillingAmount representa um valor monetário de faturamento.
// Armazenado como string de número inteiro de minor-units (centavos, satoshis,
// etc.) para evitar qualquer aritmética de ponto flutuante.
//
// REGRA: nunca converter para float64. Compare usando CompareAmounts().
// O scale (número de casas decimais) vem do Asset Registry.
type BillingAmount struct {
	// MinorUnits é o valor em minor-units do ativo (ex: para BRL com scale=2,
	// "1000" = R$ 10,00). É uma string para evitar overflow de int64 com ERC-20.
	MinorUnits string
	AssetCode  string
	Scale      int // casas decimais do Asset Registry; nunca inferido do payload
}

// BillingRecord é um registro de faturamento para um hour_bucket/campaign/banner.
type BillingRecord struct {
	HourBucket    time.Time
	TenantID      string
	CampaignID    string
	BannerID      string
	ZoneID        string
	PricingModel  string // CPM | CPC | CPA | TENANCY
	BillableUnits int64  // número de impressões/cliques/conversões faturáveis
	Amount        BillingAmount
	Source        string // "go" | "revive"
}

// ---------------------------------------------------------------------------
// Comparação de faturamento — sem float
// ---------------------------------------------------------------------------

// ErrAssetMismatch é retornado quando se tenta comparar valores de ativos diferentes.
var ErrAssetMismatch = errors.New("dual_run: cross-currency billing comparison is forbidden (DA-10)")

// ReconcileResult é o resultado da reconciliação de um par de registros.
type ReconcileResult struct {
	HourBucket      time.Time
	TenantID        string
	CampaignID      string
	AssetCode       string
	GoMinorUnits    string // string para evitar float
	ReviveMinorUnits string
	AbsDiffMinorUnits string // |Go - Revive| em minor-units (string)
	DivergencePct   float64  // APENAS para comparação com tolerância; nunca para billing
	WithinTolerance bool
	BlocksCutover   bool
}

// ReconcileHour compara os registros de faturamento do motor Go e do Revive
// para uma hora específica. Retorna a lista de reconciliações por
// (tenant, campaign, asset_code).
//
// TODO-INFRA: implementar leitura real do Iceberg e MySQL do Revive.
// Esta função hoje retorna um resultado de exemplo para validação da lógica.
func ReconcileHour(
	hourBucket time.Time,
	goRecords []BillingRecord,
	reviveRecords []BillingRecord,
	tolerancePct float64,
) ([]ReconcileResult, error) {
	if tolerancePct <= 0 || tolerancePct >= 1 {
		return nil, fmt.Errorf("dual_run: tolerancePct must be in (0,1), got %f", tolerancePct)
	}

	// Build lookup: (tenantID, campaignID, assetCode) → total minor-units
	type key struct{ tenantID, campaignID, assetCode string }

	goTotals := make(map[key]int64)
	for _, r := range goRecords {
		if r.Amount.AssetCode == "" {
			continue
		}
		k := key{r.TenantID, r.CampaignID, r.Amount.AssetCode}
		// Parse minor units from string. In production, use decimal.Decimal.
		var units int64
		_, _ = fmt.Sscanf(r.Amount.MinorUnits, "%d", &units)
		goTotals[k] += units
	}

	reviveTotals := make(map[key]int64)
	for _, r := range reviveRecords {
		if r.Amount.AssetCode == "" {
			continue
		}
		k := key{r.TenantID, r.CampaignID, r.Amount.AssetCode}
		var units int64
		_, _ = fmt.Sscanf(r.Amount.MinorUnits, "%d", &units)
		reviveTotals[k] += units
	}

	// Union of all keys.
	allKeys := make(map[key]struct{})
	for k := range goTotals {
		allKeys[k] = struct{}{}
	}
	for k := range reviveTotals {
		allKeys[k] = struct{}{}
	}

	var results []ReconcileResult
	for k := range allKeys {
		goAmt := goTotals[k]
		revAmt := reviveTotals[k]

		var diff int64
		if goAmt > revAmt {
			diff = goAmt - revAmt
		} else {
			diff = revAmt - goAmt
		}

		// Divergence percentage — computed in float only for comparison, never for billing.
		var divPct float64
		if revAmt != 0 {
			divPct = float64(diff) / float64(revAmt) //nolint:forbidigo // only for comparison gate, not billing
		} else if goAmt != 0 {
			divPct = 1.0 // revive is zero but go is not → 100% divergence
		}

		withinTol := divPct <= tolerancePct
		res := ReconcileResult{
			HourBucket:        hourBucket,
			TenantID:          k.tenantID,
			CampaignID:        k.campaignID,
			AssetCode:         k.assetCode,
			GoMinorUnits:      fmt.Sprintf("%d", goAmt),
			ReviveMinorUnits:  fmt.Sprintf("%d", revAmt),
			AbsDiffMinorUnits: fmt.Sprintf("%d", diff),
			DivergencePct:     divPct,
			WithinTolerance:   withinTol,
			BlocksCutover:     !withinTol,
		}
		results = append(results, res)
	}

	return results, nil
}

// ---------------------------------------------------------------------------
// DualRunReport — relatório da reconciliação
// ---------------------------------------------------------------------------

// DualRunReport agrega os resultados de uma ou mais horas de reconciliação.
type DualRunReport struct {
	WindowStart     time.Time
	WindowEnd       time.Time
	TotalHours      int
	HoursReconciled int
	HoursBlocking   int      // horas com divergência acima da tolerância
	MaxDivergencePct float64 // maior divergência observada (float só para relatório)
	CanCutover      bool
	BlockingReasons []string
}

// BuildReport agrega resultados de múltiplas horas em um relatório de cutover.
func BuildReport(hourResults [][]ReconcileResult, windowStart, windowEnd time.Time) DualRunReport {
	report := DualRunReport{
		WindowStart:     windowStart,
		WindowEnd:       windowEnd,
		TotalHours:      len(hourResults),
		HoursReconciled: len(hourResults),
	}

	for _, hourRecs := range hourResults {
		for _, r := range hourRecs {
			if r.DivergencePct > report.MaxDivergencePct {
				report.MaxDivergencePct = r.DivergencePct
			}
			if r.BlocksCutover {
				report.HoursBlocking++
				report.BlockingReasons = append(report.BlockingReasons, fmt.Sprintf(
					"[%s] tenant=%s campaign=%s asset=%s divergencia=%.2f%% (Go=%s Rev=%s)",
					r.HourBucket.Format(time.RFC3339),
					r.TenantID, r.CampaignID, r.AssetCode,
					r.DivergencePct*100,
					r.GoMinorUnits, r.ReviveMinorUnits,
				))
			}
		}
	}

	report.CanCutover = report.HoursBlocking == 0
	return report
}

// ---------------------------------------------------------------------------
// Tolerância declarada
//
// BillingTolerancePct = 0.005 (0.5%)
//
// Justificativa:
//   - Acordos de SLA com anunciantes toleram tipicamente 5% de desvio em
//     volume de entrega. Usamos 10x menos (0.5%) como margem de segurança.
//   - Para campanhas CPM de R$ 1.000/hora, 0.5% = R$ 5,00 de divergência
//     tolerada por hora. Qualquer divergência acima disso é material.
//   - O valor é PARAMÉTRICO (não hard-coded) para permitir ajuste via ADR.
//
// NUNCA aumentar esta tolerância sem ADR do tech-lead-architect.
// NUNCA comparar valores de billing em float.
// ---------------------------------------------------------------------------

// BillingTolerancePct é a tolerância máxima de divergência de faturamento.
// 0.005 = 0.5%. Alteração exige ADR.
const BillingTolerancePct = 0.005
