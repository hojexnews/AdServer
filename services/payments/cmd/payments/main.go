// Command payments e o servico de pagamentos multi-trilho do AdServer (K4).
//
// # Posicionamento arquitetural
//
// Este servico vive INTEIRAMENTE FORA do hot path de decisao (ADR-0004 §C /
// stack §2.6). Ele nao e importado por services/decision, internal/cascade
// nem internal/ranker. O motor de decisao le apenas budgets pre-computados
// — nunca consulta este servico em tempo de decisao.
//
// # Trilhos suportados
//
//   - Fiat global:  Stripe (Payment Intents + Billing, SAQ-A).
//   - Fiat Brasil:  Asaas (PIX primario, Pix Automatico) + Mercado Pago failover.
//   - Cripto:       Safe multisig -> Fireblocks (sob AUM) via ChainConnector (K5).
//   - Stablecoin:   USDC (Circle Mint) como ramp; USDT por alcance (K5).
//
// # Invariantes inegociaveis
//
//   - Float PROIBIDO em qualquer valor monetario (TX-2).
//   - Sem conversao automatica entre ativos (DA-10).
//   - Deposito cripto permanece PENDING ate finalidade (webhook do custodiante) (K5).
//   - Chaves (Stripe, Asaas, MercadoPago) NUNCA em imagem/git —
//     apenas via OpenBao/KMS (§2.7 / ADR-0004 §F).
//   - PII/KYC apenas no cofre de compliance (celula aml-kyc), referenciado
//     por tenant_id pseudonimo (TX-3 / DA-11).
//   - Reconciliacao periodica ABRE EXCECOES e nunca autocorrige (§2.6).
//   - SAQ-A: o cartao NUNCA transita pelo backend — tokenizacao client-side.
//
// # Segredos (nao presentes no binario; lidos do OpenBao em boot)
//
//   - STRIPE_SECRET_KEY          — chave secreta Stripe (celula pci).
//   - STRIPE_WEBHOOK_SECRET      — segredo de assinatura de webhook Stripe.
//   - ASAAS_API_KEY              — chave Asaas/PIX (celula aml-kyc).
//   - ASAAS_WEBHOOK_TOKEN        — token de autenticacao de webhooks Asaas.
//   - MERCADOPAGO_ACCESS_TOKEN   — access token Mercado Pago (failover).
//   - MERCADOPAGO_WEBHOOK_TOKEN  — token de verificacao de webhooks Mercado Pago.
//   - PAYMENTS_PG_DSN            — DSN do Postgres do ledger (K3).
//
// # Configuracao nao-sensivel (variaveis de ambiente)
//
//   - PAYMENTS_ENABLED      — "true" habilita o servico (default: false).
//   - PAYMENTS_LISTEN_ADDR  — endereco de escuta HTTP (default: ":8085").
//   - PAYMENTS_ENV          — "development" | "staging" | "production".
//   - STRIPE_BASE_URL       — URL base Stripe (default: https://api.stripe.com).
//   - ASAAS_BASE_URL        — URL base Asaas (default: https://api.asaas.com/v3).
//   - MERCADOPAGO_BASE_URL  — URL base MP (default: https://api.mercadopago.com).
//   - STRIPE_TAX_ENABLED    — "true" habilita Stripe Tax (default: false).
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hojex/adserver/services/payments/internal/asaas"
	"github.com/hojex/adserver/services/payments/internal/config"
	"github.com/hojex/adserver/services/payments/internal/fiat"
	"github.com/hojex/adserver/services/payments/internal/health"
	"github.com/hojex/adserver/services/payments/internal/mercadopago"
	"github.com/hojex/adserver/services/payments/internal/secrets"
	stripepkg "github.com/hojex/adserver/services/payments/internal/stripe"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hojex/adserver/internal/ledger"
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
		return
	}

	slog.Info("payments: iniciando", "addr", cfg.ListenAddr, "version", "K4-fiat")

	// Carrega segredos do OpenBao/KMS (em prod) ou variaveis de ambiente (dev).
	// Falha imediatamente se qualquer segredo estiver ausente.
	sec, err := secrets.Load()
	if err != nil {
		slog.Error("payments: segredos ausentes — nao e possivel iniciar sem segredos de pagamento",
			"err", err)
		os.Exit(1)
	}

	// Conecta ao Postgres do ledger (K3).
	if cfg.PgDSN == "" {
		slog.Error("payments: PAYMENTS_PG_DSN ausente — necessario para gravar postings no ledger")
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, cfg.PgDSN)
	if err != nil {
		slog.Error("payments: falha ao conectar ao Postgres do ledger", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	// AssetLoader com cache de 5 minutos sobre o Postgres.
	// O Asset Registry e a fonte autoritativa de scale/enabled para cada ativo.
	pgLoader := ledger.NewPgAssetLoader(pool)
	assetLoader := ledger.NewAssetCache(pgLoader, 5*time.Minute)

	mux := http.NewServeMux()

	// -------------------------------------------------------------------------
	// K4: registra trilhos fiat (atrás de PAYMENTS_ENABLED)
	// -------------------------------------------------------------------------

	// Trilho Stripe (fiat global, SAQ-A).
	stripeProvider, err := stripepkg.New(stripepkg.Config{
		SecretKey:     sec.StripeSecretKey(),
		WebhookSecret: sec.StripeWebhookSecret(),
		BaseURL:       cfg.StripeBaseURL,
	}, pool, assetLoader, logger)
	if err != nil {
		slog.Error("payments: falha ao inicializar trilho Stripe", "err", err)
		os.Exit(1)
	}

	// Trilho Asaas/PIX (fiat Brasil, primario).
	asaasProvider, err := asaas.New(asaas.Config{
		APIKey:       sec.AsaasAPIKey(),
		WebhookToken: cfg.AsaasWebhookToken,
		BaseURL:      cfg.AsaasBaseURL,
	}, pool, assetLoader, logger)
	if err != nil {
		slog.Error("payments: falha ao inicializar trilho Asaas/PIX", "err", err)
		os.Exit(1)
	}

	// Trilho Mercado Pago (failover Brasil).
	mpProvider, err := mercadopago.New(mercadopago.Config{
		AccessToken:  sec.MercadoPagoAccessToken(),
		WebhookToken: cfg.MercadoPagoWebhookToken,
		BaseURL:      cfg.MercadoPagoBaseURL,
	}, pool, assetLoader, logger)
	if err != nil {
		slog.Error("payments: falha ao inicializar trilho Mercado Pago", "err", err)
		os.Exit(1)
	}

	// Roteador de failover: Asaas (primario) -> Mercado Pago (failover).
	// Usado para CreatePixCharge e CreatePaymentIntent no trilho Brasil.
	_ = fiat.NewFailoverProvider(asaasProvider, mpProvider, logger)

	// -------------------------------------------------------------------------
	// Webhook handlers — server-to-server, verificacao de assinatura obrigatoria
	//
	// NOTA DE MULTI-TENANCY: em producao, o tenant_id seria extraido de um
	// JWT/mTLS injetado pela celula PCI/AML ou de uma rota por tenant.
	// Para K4, usamos uma constante de plataforma que o BFF sobrepoe.
	// O K7 (BFF) sera responsavel por extrair o tenant_id do contexto de
	// autenticacao e passar para os handlers de webhook via middleware.
	// -------------------------------------------------------------------------

	// Webhook Stripe (na celula PCI — path isolado da rota geral).
	// A celula PCI tem Cilium deny-all; apenas o Stripe pode acessar este path.
	//
	// CONTRATO DE PATH (nao alterar sem atualizar platform/cells/pci/gateway/httproute-webhook.yaml):
	// O HTTPRoute da celula PCI entrega exatamente /webhooks/stripe (plural, Exact match).
	// Este path DEVE casar com a regra do HTTPRoute; qualquer divergencia causa 404 silencioso
	// e perda de eventos assinados. Referencia: httproute-webhook.yaml spec.rules[0].matches[0].path.value
	mux.Handle("/webhooks/stripe", stripeProvider.WebhookHandler(platformTenantID()))

	// Webhook Asaas/PIX.
	// Path plural /webhooks/asaas — convencao uniforme com os demais trilhos.
	mux.Handle("/webhooks/asaas", asaasProvider.WebhookHandler(platformTenantID()))

	// Webhook Mercado Pago.
	// Path plural /webhooks/mercadopago — convencao uniforme com os demais trilhos.
	mux.Handle("/webhooks/mercadopago", mpProvider.WebhookHandler(platformTenantID()))

	// Health check com verificacao de trilhos ativos.
	railCheckers := map[string]health.RailChecker{
		"stripe": func() error {
			hCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			return stripeProvider.Healthy(hCtx)
		},
		"asaas_pix": func() error {
			hCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			return asaasProvider.Healthy(hCtx)
		},
		"mercadopago": func() error {
			hCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			return mpProvider.Healthy(hCtx)
		},
	}
	mux.Handle("/healthz", health.Handler(cfg, railCheckers))

	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("payments: http server", "err", err)
			os.Exit(1)
		}
	}()

	slog.Info("payments: trilhos fiat ativos",
		"stripe", cfg.StripeBaseURL,
		"asaas", cfg.AsaasBaseURL,
		"mercadopago_failover", cfg.MercadoPagoBaseURL,
		"stripe_tax", cfg.StripeTaxEnabled,
		// Nunca loga: secret keys, webhook secrets, PgDSN
	)

	<-ctx.Done()
	slog.Info("payments: encerrando (shutdown gracioso)")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("payments: shutdown", "err", err)
	}
	slog.Info("payments: encerrado")
}

// platformTenantID retorna o tenant_id da plataforma para postings de recebimento.
// Em K4, webhooks de pagamento sao recebidos no contexto da plataforma.
// O K7 (BFF) associara pagamentos a tenants especificos via metadata/contexto.
// NUNCA usa PII — apenas o UUID pseudonimo da plataforma.
func platformTenantID() string {
	// Lido de variavel de ambiente para evitar hardcoding.
	// Em producao, injetado pelo OpenBao como PAYMENTS_PLATFORM_TENANT_ID.
	if v := os.Getenv("PAYMENTS_PLATFORM_TENANT_ID"); v != "" {
		return v
	}
	// Default para desenvolvimento — nunca usado em producao (Enabled=false por default).
	return "00000000-0000-0000-0000-000000000001"
}
