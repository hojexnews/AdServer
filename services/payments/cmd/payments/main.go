// Command payments e o servico de pagamentos multi-trilho do AdServer (K0).
//
// # Posicionamento arquitetural
//
// Este servico vive INTEIRAMENTE FORA do hot path de decisao (ADR-0004 §C /
// stack §2.6). Ele nao e importado por services/decision, internal/cascade
// nem internal/ranker. O motor de decisao le apenas budgets pre-computados
// — nunca consulta este servico em tempo de decisao.
//
// # Trilhos suportados (K0: scaffolding; trilhos vivos em K4/K5)
//
//   - Fiat global:  Stripe (Payment Intents + Billing + Tax, SAQ-A).
//   - Fiat Brasil:  Asaas (PIX primario, Pix Automatico) + Mercado Pago failover.
//   - Cripto:       Safe multisig -> Fireblocks (sob AUM) via ChainConnector.
//   - Stablecoin:   USDC (Circle Mint) como ramp; USDT por alcance.
//
// # Invariantes inegociaveis
//
//   - Float PROIBIDO em qualquer valor monetario (TX-2).
//   - Sem conversao automatica entre ativos (DA-10).
//   - Deposito cripto permanece PENDING ate finalidade (webhook do custodiante).
//   - Chaves (Stripe, Asaas, Safe/Fireblocks) NUNCA em imagem/git —
//     apenas via OpenBao/KMS (§2.7 / ADR-0004 §F).
//   - PII/KYC apenas no cofre de compliance (celula aml-kyc), referenciado
//     por tenant_id pseudonimo (TX-3 / DA-11).
//   - Reconciliacao periodica ABRE EXCECOES e nunca autocorrige (§2.6).
//
// # Segredos (nao presentes no binario; lidos do OpenBao em boot)
//
//   - STRIPE_SECRET_KEY    — chave secreta Stripe (celula pci).
//   - ASAAS_API_KEY        — chave Asaas/PIX (celula aml-kyc).
//   - SAFE_RPC_URL         — RPC EVM para Safe multisig.
//   - PAYMENTS_PG_DSN      — DSN do Postgres do ledger (K3).
//
// K0 entrega apenas o esqueleto: health check + config + flag de enable.
// As integracoes vivas chegam em K4 (fiat) e K5 (cripto).
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hojex/adserver/services/payments/internal/config"
	"github.com/hojex/adserver/services/payments/internal/health"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config: falha ao carregar", "err", err)
		os.Exit(1)
	}

	if !cfg.Enabled {
		slog.Info("payments: servico desabilitado por flag (PAYMENTS_ENABLED=false); encerrando")
		// Flag default-off em K0: o servico nao roda em producao ate K4/K5.
		return
	}

	slog.Info("payments: iniciando", "addr", cfg.ListenAddr, "version", "K0-scaffold")

	mux := http.NewServeMux()
	mux.Handle("/healthz", health.Handler(cfg))

	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Shutdown gracioso ao receber SIGTERM/SIGINT.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("payments: http server", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("payments: encerrando (shutdown gracioso)")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("payments: shutdown", "err", err)
	}
	slog.Info("payments: encerrado")
}
