// Package fiat — failover.go
//
// FailoverProvider implementa FiatProvider com roteamento automatico:
// primario -> failover quando o primario falha ou esta indisponivel.
//
// Uso tipico: Asaas (primario) -> Mercado Pago (failover) para o trilho Brasil.
//
// # Logica de failover
//
//  1. Tenta o primario. Se retornar erro, loga e tenta o failover.
//  2. Healthy() checa o primario: se indisponivel, usa o failover diretamente.
//  3. O failover NUNCA e tentado se o primario retornou sucesso.
//  4. Se ambos falham, retorna o erro do failover com contexto do erro primario.
//
// # Invariantes
//
//   - Ambas as implementacoes devem satisfazer FiatProvider.
//   - O roteamento e transparente para o caller: mesma interface.
//   - Logs distinguem qual trilho foi usado (primary vs. failover).
package fiat

import (
	"context"
	"fmt"
	"log/slog"
)

// FailoverProvider implementa FiatProvider com roteamento primario -> failover.
//
// Testavel: injetar stubs de FiatProvider permite testar todos os cenarios
// de failover sem depender de APIs externas.
type FailoverProvider struct {
	primary  FiatProvider
	fallback FiatProvider
	logger   *slog.Logger
}

// NewFailoverProvider cria um FailoverProvider.
//
// primary e o provedor principal (ex.: Asaas PIX).
// fallback e o provedor de failover (ex.: Mercado Pago).
// Se primary estiver saudavel e retornar sucesso, fallback nunca e chamado.
// Se logger for nil, usa slog.Default().
func NewFailoverProvider(primary, fallback FiatProvider, logger *slog.Logger) *FailoverProvider {
	if logger == nil {
		logger = slog.Default()
	}
	return &FailoverProvider{
		primary:  primary,
		fallback: fallback,
		logger:   logger,
	}
}

// RailName retorna "primary/failover" para identificacao em logs.
func (f *FailoverProvider) RailName() string {
	return fmt.Sprintf("%s+%s-failover", f.primary.RailName(), f.fallback.RailName())
}

// Healthy retorna nil apenas se o primario estiver saudavel.
// Se o primario estiver inativo, retorna o erro do primario
// (indica que o failover esta ativo — nao e um erro critico).
func (f *FailoverProvider) Healthy(ctx context.Context) error {
	if err := f.primary.Healthy(ctx); err != nil {
		f.logger.Warn("fiat-failover: primario inativo; failover ativo",
			"primary", f.primary.RailName(),
			"fallback", f.fallback.RailName(),
			"err", err,
		)
		return fmt.Errorf("fiat-failover: primario indisponivel (%s): %w; fallover ativo (%s)",
			f.primary.RailName(), err, f.fallback.RailName())
	}
	return nil
}

// CreatePaymentIntent tenta o primario; em caso de erro, usa o failover.
func (f *FailoverProvider) CreatePaymentIntent(ctx context.Context, req PaymentIntentRequest) (PaymentIntentResult, error) {
	result, err := f.primary.CreatePaymentIntent(ctx, req)
	if err == nil {
		return result, nil
	}

	f.logger.Warn("fiat-failover: primario falhou em CreatePaymentIntent — tentando failover",
		"primary", f.primary.RailName(),
		"fallback", f.fallback.RailName(),
		"primary_err", err,
		"tenant_id", req.TenantID,
		// Sem PII: sem advertiser_id nem outros dados de identidade
	)

	result, errFallback := f.fallback.CreatePaymentIntent(ctx, req)
	if errFallback != nil {
		return PaymentIntentResult{}, fmt.Errorf(
			"fiat-failover: primario=%s (%v); failover=%s (%w)",
			f.primary.RailName(), err, f.fallback.RailName(), errFallback,
		)
	}
	return result, nil
}

// CreatePixCharge tenta o primario (Asaas); em caso de erro, usa o failover (MP).
func (f *FailoverProvider) CreatePixCharge(ctx context.Context, req PixChargeRequest) (PixChargeResult, error) {
	result, err := f.primary.CreatePixCharge(ctx, req)
	if err == nil {
		return result, nil
	}

	f.logger.Warn("fiat-failover: primario falhou em CreatePixCharge — tentando failover",
		"primary", f.primary.RailName(),
		"fallback", f.fallback.RailName(),
		"primary_err", err,
		"tenant_id", req.TenantID,
	)

	result, errFallback := f.fallback.CreatePixCharge(ctx, req)
	if errFallback != nil {
		return PixChargeResult{}, fmt.Errorf(
			"fiat-failover: primario=%s (%v); failover=%s (%w)",
			f.primary.RailName(), err, f.fallback.RailName(), errFallback,
		)
	}
	return result, nil
}
