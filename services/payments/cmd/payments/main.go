// Command payments e o servico de pagamentos multi-trilho do AdServer (K4/K5/K6).
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
//   - Cripto K5:   Safe multisig -> Fireblocks (sob AUM) via ChainConnector.
//                  USDC como ramp. Deposito PENDING ate N confirmacoes via webhook.
//                  Reorg -> estorno auditavel (RecordReversal). AEV/BND gated (scale TBD).
//   - Compliance K6: Sumsub (KYC/KYB) + Chainalysis (screening on-chain) + Travel Rule.
//                    Cofre db/compliance (PII/KYC pseudonimo, RLS por tenant).
//                    Screening fail-closed: sancoes/alto risco bloqueiam deposito/payout.
//
// # Invariantes inegociaveis
//
//   - Float PROIBIDO em qualquer valor monetario (TX-2).
//   - Sem conversao automatica entre ativos (DA-10).
//   - Deposito cripto permanece PENDING ate finalidade (webhook do custodiante) (K5).
//   - Chaves NUNCA em imagem/git — apenas via OpenBao/KMS (§2.7 / ADR-0004 §F).
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
//   - SAFE_WEBHOOK_SECRET        — segredo HMAC-SHA256 para webhooks do custodiante Safe (K5).
//                                   Lido de SAFE_WEBHOOK_SECRET via OpenBao/env.
//   - CHAINALYSIS_API_KEY        — chave de API Chainalysis KYT (K6). OpenBao/env.
//   - SUMSUB_API_KEY             — chave de API Sumsub (K6). OpenBao/env.
//   - SUMSUB_WEBHOOK_SECRET      — segredo HMAC para webhooks Sumsub (K6). OpenBao/env.
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
//   - CRYPTO_ENABLED        — "true" habilita trilho cripto Safe/USDC (K5, default: false).
//   - SAFE_RPC_URL          — endpoint JSON-RPC EVM (chain_id=1); OpenBao/env.
//   - SAFE_ADDRESS          — endereco da carteira Safe multisig da plataforma.
//   - SAFE_MIN_CONFIRMATIONS — min confirmacoes para finalidade (default: 12).
//   - USDC_CONTRACT_ADDRESS — contrato USDC (default: mainnet Ethereum).
//   - COMPLIANCE_ENABLED    — "true" habilita compliance K6 (default: false).
//   - CHAINALYSIS_BASE_URL  — URL base Chainalysis (default: https://api.chainalysis.com).
//   - CHAINALYSIS_RISK_THRESHOLD — limiar de risco 0-100 (default: 70).
//   - SUMSUB_BASE_URL       — URL base Sumsub (default: https://api.sumsub.com).
//   - TRAVEL_RULE_THRESHOLD_USDC — limiar Travel Rule em minor-units USDC (default: 0).
//
// # SEAM Fireblocks (K5)
//
// O trilho cripto usa EVMSafe (Safe multisig). Para o upgrade Fireblocks
// (sob AUM — gatilho C ADR-0004), substituir EVMSafe por EVMFireblocks
// no bloco "K5: trilho cripto" abaixo — mesma interface ChainConnector,
// zero mudanca no resto do servico.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hojex/adserver/internal/chainconnector"
	"github.com/hojex/adserver/internal/ledger"
	"github.com/hojex/adserver/services/payments/internal/asaas"
	"github.com/hojex/adserver/services/payments/internal/chainalysis"
	"github.com/hojex/adserver/services/payments/internal/config"
	cryptopkg "github.com/hojex/adserver/services/payments/internal/crypto"
	"github.com/hojex/adserver/services/payments/internal/fiat"
	"github.com/hojex/adserver/services/payments/internal/health"
	"github.com/hojex/adserver/services/payments/internal/kmsenvelope"
	"github.com/hojex/adserver/services/payments/internal/mercadopago"
	"github.com/hojex/adserver/services/payments/internal/secrets"
	stripepkg "github.com/hojex/adserver/services/payments/internal/stripe"
	"github.com/hojex/adserver/services/payments/internal/sumsub"
	"github.com/hojex/adserver/services/payments/internal/travelrule"
	"github.com/jackc/pgx/v5/pgxpool"
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

	// -------------------------------------------------------------------------
	// K6: compliance — Sumsub (KYC/KYB) + Chainalysis (screening) + Travel Rule
	//
	// Inicializado ANTES do K5 para que os adaptadores de compliance possam ser
	// injetados no SafeWebhookHandler no momento de sua construcao.
	//
	// POSICIONAMENTO (ADR-0004 §E.10 / §F):
	//   - Vive na celula AML/KYC (Cilium deny-all, segredos via OpenBao).
	//   - PII/KYC apenas no cofre db/compliance, referenciado por tenant_id pseudonimo (TX-3).
	//   - Ledger e telemetria SEM PII (DA-11).
	//   - Screening fail-closed: sancoes/alto risco bloqueiam deposito/payout.
	//   - Webhook Sumsub verificado por HMAC antes de qualquer efeito.
	//   - Segredos (CHAINALYSIS_API_KEY, SUMSUB_API_KEY, SUMSUB_WEBHOOK_SECRET)
	//     NUNCA em imagem/git — injetados pelo OpenBao/Vault Agent (celula aml-kyc).
	// -------------------------------------------------------------------------

	// Adaptadores de compliance injetados no SafeWebhookHandler (K5).
	// nil = compliance desabilitado (dev/staging); nao-nil em producao.
	// NOTA DE PRODUCAO: COMPLIANCE_ENABLED=false NAO e aceitavel em producao
	// — ambos devem ser nao-nil para que o screening fail-closed opere.
	var cryptoScreener cryptopkg.AddressScreener     // injetado abaixo se COMPLIANCE_ENABLED
	var cryptoTravelRecorder cryptopkg.TravelRuleRecorder // injetado abaixo se COMPLIANCE_ENABLED

	if cfg.ComplianceEnabled {
		// Segredos K6 lidos de env/OpenBao — nunca em imagem/git.
		chainalysisAPIKey := os.Getenv("CHAINALYSIS_API_KEY")
		if chainalysisAPIKey == "" {
			slog.Error("payments: COMPLIANCE_ENABLED=true mas CHAINALYSIS_API_KEY ausente")
			os.Exit(1)
		}
		sumsubAPIKey := os.Getenv("SUMSUB_API_KEY")
		if sumsubAPIKey == "" {
			slog.Error("payments: COMPLIANCE_ENABLED=true mas SUMSUB_API_KEY ausente")
			os.Exit(1)
		}
		sumsubWebhookSecret := os.Getenv("SUMSUB_WEBHOOK_SECRET")
		if sumsubWebhookSecret == "" {
			slog.Error("payments: COMPLIANCE_ENABLED=true mas SUMSUB_WEBHOOK_SECRET ausente")
			os.Exit(1)
		}

		// Screener Chainalysis (screening on-chain).
		// Fail-closed: sancoes ou risco acima do limiar bloqueiam a operacao.
		// A chamada HTTP real pende de CHAINALYSIS_API_KEY viva.
		rawScreener, err := chainalysis.New(chainalysis.Config{
			APIKey:        chainalysisAPIKey, // NUNCA loga
			BaseURL:       cfg.ChainalysisBaseURL,
			RiskThreshold: cfg.ChainalysisRiskThreshold, // int 0-100; TX-2: sem float
		}, pool, logger)
		if err != nil {
			slog.Error("payments: falha ao inicializar Chainalysis screener", "err", err)
			os.Exit(1)
		}
		// Adapta *chainalysis.Screener -> cryptopkg.AddressScreener.
		// O adaptador traduz o ScreenAddressParams do pacote crypto para o do
		// pacote chainalysis — sem ciclo de import, sem PII em logs.
		cryptoScreener = &chainalysisScreenerAdapter{s: rawScreener}

		// Envelope KMS para cifra de PII (originator_name / beneficiary_name / full_name).
		// PII_ENVELOPE_KEY lida de env/OpenBao — nunca em imagem/git (DA-11 / TX-3).
		// Em producao, injetar via OpenBao como PII_ENVELOPE_KEY.
		// Fail-closed: se a chave estiver ausente, o servico nao sobe.
		// Inicializado antes de sumsub.New e travelrule.New pois ambos dependem do envelope.
		piiEnvelope, err := kmsenvelope.NewFromEnv()
		if err != nil {
			slog.Error("payments: PII_ENVELOPE_KEY ausente — nao e possivel cifrar PII de Travel Rule/KYC",
				"err", err,
			)
			os.Exit(1)
		}

		// Client Sumsub (KYC/KYB).
		// Webhook verificado por HMAC-SHA1 antes de qualquer efeito.
		// PII dos documentos fica exclusivamente no Sumsub; cofre local guarda
		// apenas subject_ref pseudonimo + status (TX-3 / DA-11).
		// piiEnvelope: cifra full_name se necessario por regulacao (TX-3).
		sumsubClient, err := sumsub.New(sumsub.Config{
			APIKey:        sumsubAPIKey,        // NUNCA loga
			WebhookSecret: sumsubWebhookSecret, // NUNCA loga
			BaseURL:       cfg.SumsubBaseURL,
		}, pool, piiEnvelope, logger)
		if err != nil {
			slog.Error("payments: falha ao inicializar client Sumsub", "err", err)
			os.Exit(1)
		}

		// Webhook Sumsub: /webhooks/sumsub
		// Verificacao HMAC-SHA1 tempo-constante + anti-replay antes de atualizar status KYC.
		// A celula AML/KYC tem Cilium deny-all; apenas o Sumsub pode acessar este path.
		mux.Handle("/webhooks/sumsub", sumsubClient.WebhookHandler())

		// Recorder de Travel Rule.
		// Registra transferencias acima do limiar em compliance.travel_rule_records.
		// Fail-closed: falha no registro bloqueia o payout (ErrTravelRuleRequired).
		// PII de originador/beneficiario cifrada via envelope KMS antes do INSERT (TX-3/DA-11).
		rawTravelRecorder := travelrule.New(travelrule.Config{
			ThresholdMinorUnits: cfg.TravelRuleThresholdUSDC, // int64; TX-2: sem float
			AssetCode:           "USDC",
		}, pool, nil, piiEnvelope, logger) // transport nil = StubVASPTransport; envelope KMS obrigatorio em producao
		// Adapta *travelrule.Recorder -> cryptopkg.TravelRuleRecorder.
		cryptoTravelRecorder = &travelRuleRecorderAdapter{r: rawTravelRecorder}

		slog.Info("payments: compliance K6 ativo",
			"chainalysis_threshold", cfg.ChainalysisRiskThreshold,
			"travel_rule_threshold_usdc_minor", cfg.TravelRuleThresholdUSDC,
			"sumsub_base_url", cfg.SumsubBaseURL,
			// Nunca loga: API keys, webhook secrets
		)
	}

	// -------------------------------------------------------------------------
	// K5: trilho cripto Safe multisig + USDC (atrás de CRYPTO_ENABLED)
	//
	// SEAM Fireblocks: para o upgrade sob AUM (gatilho C ADR-0004), substituir
	// EVMSafe por EVMFireblocks abaixo — mesma interface ChainConnector, zero
	// mudanca no webhook handler nem no resto do servico.
	//
	// AEV/BND: gated por scale TBD no Asset Registry (enabled=false, CHECK
	// assets_enabled_needs_scale_chk). Qualquer posting em AEV/BND e recusado
	// pelo ledger até scale ser definido (E.2 ADR-0004).
	// -------------------------------------------------------------------------
	if cfg.CryptoEnabled {
		if cfg.SafeRPCURL == "" {
			slog.Error("payments: CRYPTO_ENABLED=true mas SAFE_RPC_URL ausente")
			os.Exit(1)
		}
		safeWebhookSecret := os.Getenv("SAFE_WEBHOOK_SECRET")
		if safeWebhookSecret == "" {
			slog.Error("payments: CRYPTO_ENABLED=true mas SAFE_WEBHOOK_SECRET ausente — necessario para verificar webhooks do custodiante")
			os.Exit(1)
		}

		// Scale do USDC lido do Asset Registry no boot — NUNCA hardcoded (TX-2/DA-10).
		// O Asset Registry e a fonte autoritativa; se USDC estiver disabled ou com
		// scale NULL, LoadAsset falha e o trilho cripto nao sobe (fail-closed).
		usdcAsset, err := assetLoader.LoadAsset(ctx, "USDC")
		if err != nil {
			slog.Error("payments: falha ao carregar scale de USDC do Asset Registry — trilho cripto nao pode subir", "err", err)
			os.Exit(1)
		}
		usdcScale := usdcAsset.Scale

		// Instancia EVMSafe (ChainConnector para Safe multisig).
		evmSafe, err := chainconnector.NewEVMSafe(chainconnector.EVMSafeConfig{
			Chains: map[int64]chainconnector.EVMSafeChainConfig{
				1: {
					ChainID:          1,
					RPCURL:           cfg.SafeRPCURL, // injetado via OpenBao/env
					ContractAddress:  cfg.USDCContractAddress,
					AssetCode:        "USDC",
					AssetScale:       usdcScale, // do Asset Registry, nunca literal
					MinConfirmations: cfg.SafeMinConfirmations,
					SafeAddress:      cfg.SafeAddress,
				},
			},
		})
		if err != nil {
			slog.Error("payments: falha ao inicializar EVMSafe (ChainConnector)", "err", err)
			os.Exit(1)
		}

		// Handler de webhook do custodiante Safe/Defender.
		// Recebe: deposit.detected, deposit.finalized, chain.reorg.
		// Verifica assinatura HMAC-SHA256 antes de qualquer processamento.
		// Screener e TravelRecorder injetados do bloco K6 (nil se COMPLIANCE_ENABLED=false).
		// Em producao, COMPLIANCE_ENABLED deve ser true para screening fail-closed operar.
		safeWebhookHandler, err := cryptopkg.NewSafeWebhookHandler(
			cryptopkg.Config{
				WebhookSecret: safeWebhookSecret, // SAFE_WEBHOOK_SECRET — nunca loga
				ChainConfigs: map[int64]cryptopkg.ChainAccountConfig{
					1: {AssetCode: "USDC", AssetScale: usdcScale}, // do Asset Registry, nunca literal
				},
				Screener:       cryptoScreener,       // nil se COMPLIANCE_ENABLED=false
				TravelRecorder: cryptoTravelRecorder, // nil se COMPLIANCE_ENABLED=false
			},
			pool,
			assetLoader,
			evmSafe,
			logger,
		)
		if err != nil {
			slog.Error("payments: falha ao inicializar SafeWebhookHandler", "err", err)
			os.Exit(1)
		}

		// Webhook do custodiante Safe (celula aml-kyc).
		// Path /webhooks/safe — convencao uniforme com os demais trilhos.
		// A celula AML/KYC tem Cilium deny-all; apenas o Safe/Defender pode acessar.
		mux.Handle("/webhooks/safe", safeWebhookHandler)

		slog.Info("payments: trilho cripto Safe/USDC ativo",
			"chain_id", 1,
			"usdc_contract", cfg.USDCContractAddress,
			"min_confirmations", cfg.SafeMinConfirmations,
			"compliance_screening", cryptoScreener != nil,
			"compliance_travel_rule", cryptoTravelRecorder != nil,
			// Nunca loga: SafeRPCURL (pode conter token), safeWebhookSecret
		)
	}

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

// ---------------------------------------------------------------------------
// Adaptadores de compliance (K6)
//
// Traduzem os tipos concretos de chainalysis e travelrule para as interfaces
// definidas no pacote crypto, sem ciclo de import.
// ---------------------------------------------------------------------------

// chainalysisScreenerAdapter adapta *chainalysis.Screener para cryptopkg.AddressScreener.
//
// A traducao de ScreenAddressParams (crypto) -> ScreenAddressParams (chainalysis)
// e feita campo-a-campo; os tipos sao identicos em semantica.
// PII: nenhum campo de identidade e adicionado pelo adaptador.
type chainalysisScreenerAdapter struct {
	s *chainalysis.Screener
}

func (a *chainalysisScreenerAdapter) ScreenAddress(ctx context.Context, p cryptopkg.ScreenAddressParams) error {
	return a.s.ScreenAddress(ctx, chainalysis.ScreenAddressParams{
		TenantID:  p.TenantID,
		Address:   p.Address,
		Direction: chainalysis.Direction(p.Direction),
		TxRef:     p.TxRef,
		Network:   p.Network,
	})
}

// travelRuleRecorderAdapter adapta *travelrule.Recorder para cryptopkg.TravelRuleRecorder.
//
// Traduz PayoutTravelRuleParams (crypto) -> RecordParams (travelrule).
// ErrBelowThreshold do travelrule e mapeado para cryptopkg.ErrTravelRuleBelowThreshold
// para que o chamador no pacote crypto possa distinguir sem importar travelrule.
// Qualquer outro erro (ErrTravelRuleRequired) e retornado diretamente — o pacote
// crypto envelopa em ErrTravelRuleBlocked.
type travelRuleRecorderAdapter struct {
	r *travelrule.Recorder
}

func (a *travelRuleRecorderAdapter) RecordPayout(ctx context.Context, p cryptopkg.PayoutTravelRuleParams) error {
	err := a.r.Record(ctx, travelrule.RecordParams{
		TenantID:         p.TenantID,
		TxRef:            p.TxRef,
		OriginatorVASP:   p.OriginatorVASP,
		BeneficiaryVASP:  p.BeneficiaryVASP,
		OriginatorName:   p.OriginatorName,
		BeneficiaryName:  p.BeneficiaryName,
		AmountMinorUnits: p.AmountMinorUnits,
		AssetCode:        p.AssetCode,
		Direction:        travelrule.DirectionOutbound,
	})
	if errors.Is(err, travelrule.ErrBelowThreshold) {
		return cryptopkg.ErrTravelRuleBelowThreshold
	}
	return err
}
