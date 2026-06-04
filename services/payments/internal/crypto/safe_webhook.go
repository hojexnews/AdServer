// Package crypto implementa o webhook handler de custódia Safe multisig (K5)
// e as interfaces de compliance de screening/Travel Rule (K6).
//
// # Posicionamento
//
// Este pacote recebe notificacoes do custodiante (Safe/OpenZeppelin Defender/
// Gelato) sobre eventos on-chain: deposito detectado, finalidade atingida e
// reorg. Vive inteiramente em services/payments — NUNCA importado pelo motor
// de decisao, internal/ranker ou internal/cascade.
//
// # Fluxo de deposito (E.8 ADR-0004)
//
//  1. Custodiante detecta Transfer on-chain -> envia POST /webhooks/safe.
//  2. Handler verifica assinatura HMAC-SHA256 (tempo-constante, anti-replay).
//  3. Deposito detectado -> ledger.RecordDeposit (status pending; idempotente).
//  4. Finalidade (N confirmacoes) -> SCREENING do endereco originador (K6)
//     fail-closed: sancoes/risco alto/indisponivel bloqueia FinalizeDeposit.
//     Se screening OK -> ledger.FinalizeDeposit (pending->posted).
//  5. Reorg -> ledger.RecordReversal (novo par invertido; posting original intocado).
//
// # Fluxo de payout (SubmitPayout)
//
//  1. SCREENING do endereco de destino (K6) fail-closed — bloqueia se sancionado,
//     risco alto ou screener indisponivel.
//  2. Para transferencias acima do limiar: TravelRule.Record (K6) — bloqueia se
//     ErrTravelRuleRequired.
//  3. BuildPayout via ChainConnector -> submissao ao custodiante Safe.
//
// # Interfaces de compliance (K6)
//
// AddressScreener e TravelRuleRecorder sao definidas neste pacote para evitar
// dependencia circular. A implementacao concreta (chainalysis.Screener,
// travelrule.Recorder) e injetada pelo main.go.
//
//   - AddressScreener: ScreenAddress(ctx, params) error — fail-closed.
//   - TravelRuleRecorder: RecordPayout(ctx, params) error — fail-closed acima do limiar.
//
// Ambos sao OPCIONAIS (nil = sem screening, comportamento de desenvolvimento).
// Em producao com COMPLIANCE_ENABLED, ambos DEVEM ser nao-nil.
//
// # Invariantes inegociaveis
//
//   - Float PROIBIDO: valores monetarios chegam como string decimal e sao
//     convertidos para int64 minor-units com parseMajorToMinor (sem float).
//   - Saldo permanece pending ate FinalizeDeposit. O conector NUNCA credita.
//   - Reorg = estorno auditavel. NUNCA edita posting original.
//   - Idempotencia: deposit:{tx_hash} / void:deposit:{tx_hash} — reprocessamento
//     nao duplica postings.
//   - Segredo (SAFE_WEBHOOK_SECRET) lido de env/OpenBao, NUNCA hardcoded.
//   - PII: enderecos on-chain da plataforma nao sao PII. Associacao
//     anunciante<->endereco fica no cofre de compliance (K6 — TX-3/DA-11).
//   - Screening fail-closed: QUALQUER erro (sancao, risco, indisponivel) BLOQUEIA.
//     Logs de bloqueio NAO combinam endereco + tenant_id (DA-11).
//
// # Seguranca do webhook
//
// Verificacao identica ao padrao dos webhooks fiat (K4):
//   - HMAC-SHA256 do payload com segredo compartilhado (SAFE_WEBHOOK_SECRET).
//   - Header: X-Safe-Signature: sha256=<hex>
//   - Rejeita com HTTP 401 se assinatura invalida ou ausente.
//   - Anti-replay: timestamp no payload; janela de 10 minutos.
//   - Limite de tamanho do payload: 512 KB.
package crypto

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/hojex/adserver/internal/chainconnector"
	"github.com/hojex/adserver/internal/ledger"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------------------------------------------------------------------------
// Interfaces de compliance (K6)
// ---------------------------------------------------------------------------

// ScreenAddressParams agrupa os parametros de screening passados ao AddressScreener.
//
// Definido aqui para desacoplar o pacote crypto do pacote chainalysis
// e permitir stubs de teste sem dependencia de implementacao concreta.
type ScreenAddressParams struct {
	// TenantID e o UUID pseudonimo do tenant (nunca PII real).
	TenantID string
	// Address e o endereco on-chain a screear (nao e PII de anunciante).
	Address string
	// ChainID identifica a chain (ex.: 1=Ethereum mainnet).
	ChainID int64
	// Direction indica o sentido: "deposit" (originador) ou "payout" (destino).
	Direction string
	// TxRef e a referencia a transacao (hash ou idempotency_key).
	TxRef string
	// Network e a rede blockchain (ex.: "ethereum"). Default derivado de ChainID.
	Network string
}

// AddressScreener define o contrato de screening de enderecos on-chain (K6).
//
// A implementacao concreta e chainalysis.Screener. Definida aqui para
// desacoplar o pacote crypto (evitar ciclo de import) e facilitar stubs.
//
// Fail-closed: qualquer erro retornado (ErrSanctionsHit, ErrRiskAboveThreshold,
// ErrScreeningUnavailable) DEVE bloquear a operacao. Nunca retorna nil em erro.
//
// PII: implementacoes nao devem logar Address e TenantID juntos.
type AddressScreener interface {
	ScreenAddress(ctx context.Context, p ScreenAddressParams) error
}

// PayoutTravelRuleParams agrupa os parametros do registro de Travel Rule para payout.
//
// Definido aqui para desacoplar crypto de travelrule e permitir stubs de teste.
// PII (OriginatorName, BeneficiaryName) devem ser cifrados pela camada de
// aplicacao antes de chegar aqui — a interface nao garante cifragem.
type PayoutTravelRuleParams struct {
	// TenantID e o UUID pseudonimo do tenant.
	TenantID string
	// TxRef e a referencia da transacao (idempotency_key do payout).
	TxRef string
	// OriginatorVASP e o identificador do VASP de origem.
	OriginatorVASP string
	// BeneficiaryVASP e o identificador do VASP receptor.
	BeneficiaryVASP string
	// OriginatorName e o nome do originador (PII — cifrar antes de INSERT).
	OriginatorName string
	// BeneficiaryName e o nome do beneficiario (PII — cifrar antes de INSERT).
	BeneficiaryName string
	// AmountMinorUnits e o valor em minor-units (int64, TX-2: sem float).
	AmountMinorUnits int64
	// AssetCode e o codigo do ativo (ex.: "USDC").
	AssetCode string
}

// TravelRuleRecorder define o contrato de registro de Travel Rule para payouts (K6).
//
// A implementacao concreta e travelrule.Recorder. Definida aqui para
// desacoplar o pacote crypto e facilitar stubs de teste.
//
// Semantica de retorno:
//   - nil: registro concluido (acima do limiar, enviado com sucesso).
//   - ErrTravelRuleBelowThreshold: abaixo do limiar — nao e erro bloqueante.
//   - ErrTravelRuleBlocked: registro falhou acima do limiar — BLOQUEIA o payout.
type TravelRuleRecorder interface {
	RecordPayout(ctx context.Context, p PayoutTravelRuleParams) error
}

// ErrTravelRuleBelowThreshold indica que o valor esta abaixo do limiar de Travel Rule.
// Nao e erro bloqueante — o payout pode prosseguir sem registro.
var ErrTravelRuleBelowThreshold = errors.New("crypto: transferencia abaixo do limiar de Travel Rule")

// ErrTravelRuleBlocked indica que o registro de Travel Rule falhou acima do limiar.
// O payout DEVE ser bloqueado (fail-closed).
var ErrTravelRuleBlocked = errors.New("crypto: Travel Rule obrigatorio — payout bloqueado (fail-closed)")

// ErrScreeningBlocked indica que o screening bloqueou a operacao.
// Pode ser sancao, risco alto ou screener indisponivel — todos fail-closed.
var ErrScreeningBlocked = errors.New("crypto: screening bloqueou a operacao (fail-closed)")

// ---------------------------------------------------------------------------
// Erros exportados
// ---------------------------------------------------------------------------

// ErrInvalidSignature indica falha na verificacao de assinatura do webhook.
var ErrInvalidSignature = errors.New("safe_webhook: assinatura invalida — rejeitado")

// ErrWebhookTooOld indica que o timestamp do webhook e antigo (anti-replay).
var ErrWebhookTooOld = errors.New("safe_webhook: webhook muito antigo (possivel replay)")

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

const (
	maxBodyBytes = 512 * 1024  // 512 KB
	replayWindow = 10 * time.Minute
)

// SafeWebhookHandler e o handler de webhooks do custodiante Safe/Defender.
//
// Instanciado em services/payments/cmd/payments/main.go e registrado em
// /webhooks/safe. Recebe eventos do custodiante Safe multisig.
//
// Campos de compliance (K6):
//   - screener: se nao-nil, screena o endereco originador/destino antes de
//     finalizar deposito ou submeter payout. Fail-closed.
//   - travelRecorder: se nao-nil, registra payouts acima do limiar de
//     Travel Rule antes de construir a tx. Fail-closed.
//
// Em desenvolvimento (COMPLIANCE_ENABLED=false), ambos podem ser nil — o
// handler processa sem screening. Em producao, AMBOS devem ser nao-nil.
type SafeWebhookHandler struct {
	webhookSecret string // SAFE_WEBHOOK_SECRET — NUNCA logue
	pool          *pgxpool.Pool
	assetLoader   ledger.AssetLoader
	connector     *chainconnector.EVMSafe // para InjectDeposit (WatchDeposits)
	logger        *slog.Logger

	// chainConfigs mapeia chain_id -> config da chain (para resolucao de contas)
	chainConfigs map[int64]ChainAccountConfig

	// screener (K6): screening on-chain de enderecos. OPCIONAL mas obrigatorio
	// em producao. Se nil, screening e ignorado (sem compliance).
	// Fail-closed: qualquer erro bloqueia deposito/payout.
	screener AddressScreener

	// travelRecorder (K6): registro de Travel Rule para payouts acima do limiar.
	// OPCIONAL mas obrigatorio em producao. Se nil, Travel Rule e ignorado.
	// Fail-closed: falha acima do limiar bloqueia o payout.
	travelRecorder TravelRuleRecorder
}

// ChainAccountConfig mapeia chain_id para as contas do ledger usadas nos postings.
type ChainAccountConfig struct {
	// AssetCode e o codigo do ativo no Asset Registry (ex.: "USDC").
	AssetCode string
	// AssetScale DEVE bater com Asset Registry — nunca hardcoded aqui.
	// Lido da configuracao carregada do Asset Registry no boot.
	AssetScale int32
	// PlatformCashAccountID e o ID da conta platform:cash:{asset}.
	// Populado por EnsureAccount no boot do servico.
	PlatformCashAccountID int64
	// AdvAccountPrefix e o prefixo das contas de anunciante (ex.: "adv").
	// O account ID real e resolvido via EnsureAccount por tenant_id.
	AdvAccountPrefix string
}

// Config agrupa a configuracao do handler.
type Config struct {
	// WebhookSecret e o segredo HMAC-SHA256 para verificar webhooks Safe/Defender.
	// Lido de SAFE_WEBHOOK_SECRET via env/OpenBao — NUNCA em git/imagem.
	WebhookSecret string

	// ChainConfigs mapeia chain_id -> config de conta do ledger.
	ChainConfigs map[int64]ChainAccountConfig

	// Screener (K6) para screening de enderecos on-chain. OPCIONAL.
	// Se nil, depositos e payouts prosseguem sem screening (sem compliance).
	// Em producao com COMPLIANCE_ENABLED, DEVE ser nao-nil.
	// A implementacao concreta e *chainalysis.Screener injetada pelo main.go.
	Screener AddressScreener

	// TravelRecorder (K6) para registro de Travel Rule. OPCIONAL.
	// Se nil, Travel Rule e ignorado. Em producao com COMPLIANCE_ENABLED, DEVE
	// ser nao-nil. A implementacao concreta e *travelrule.Recorder (via adaptador).
	TravelRecorder TravelRuleRecorder
}

// NewSafeWebhookHandler cria um handler pronto para uso.
//
// pool e assetLoader sao obrigatorios em producao (para gravar postings no
// ledger). Em testes de assinatura/parsing, podem ser nil — o handler
// retornara erro 500 apenas ao tentar acessar o banco, nao na construcao.
// O WebhookSecret e o unico parametro obrigatorio.
func NewSafeWebhookHandler(
	cfg Config,
	pool *pgxpool.Pool,
	assetLoader ledger.AssetLoader,
	connector *chainconnector.EVMSafe,
	logger *slog.Logger,
) (*SafeWebhookHandler, error) {
	if cfg.WebhookSecret == "" {
		return nil, errors.New("safe_webhook: WebhookSecret obrigatorio (SAFE_WEBHOOK_SECRET)")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &SafeWebhookHandler{
		webhookSecret:  cfg.WebhookSecret,
		pool:           pool,
		assetLoader:    assetLoader,
		connector:      connector,
		logger:         logger,
		chainConfigs:   cfg.ChainConfigs,
		screener:       cfg.Screener,       // nil = sem compliance (dev/staging)
		travelRecorder: cfg.TravelRecorder, // nil = sem Travel Rule (dev/staging)
	}, nil
}

// ---------------------------------------------------------------------------
// Tipos de payload do webhook
// ---------------------------------------------------------------------------

// webhookEventType lista os tipos de evento suportados.
type webhookEventType string

const (
	eventTypeDepositDetected  webhookEventType = "deposit.detected"
	eventTypeDepositFinalized webhookEventType = "deposit.finalized"
	eventTypeReorg            webhookEventType = "chain.reorg"
	eventTypePayoutSubmitted  webhookEventType = "payout.submitted"
)

// safeWebhookPayload e o payload JSON enviado pelo custodiante Safe/Defender.
//
// Campos PII: ausentes — enderecos on-chain de custodia da plataforma nao sao PII.
// A associacao anunciante<->endereco fica no cofre de compliance (K6).
type safeWebhookPayload struct {
	// Type e o tipo do evento (deposit.detected, deposit.finalized, chain.reorg).
	Type webhookEventType `json:"type"`

	// Timestamp e o unix timestamp do evento (usado para anti-replay).
	Timestamp int64 `json:"timestamp"`

	// TxHash e o hash da transacao on-chain.
	TxHash string `json:"tx_hash"`

	// ChainID e o EVM chain id (1=Ethereum mainnet).
	ChainID int64 `json:"chain_id"`

	// BlockNumber e o numero do bloco da transacao.
	BlockNumber uint64 `json:"block_number"`

	// Confirmations e o numero de confirmacoes no momento do evento.
	Confirmations uint32 `json:"confirmations"`

	// ToAddress e o endereco de destino (carteira Safe da plataforma).
	// NAO e PII de anunciante.
	ToAddress string `json:"to_address"`

	// FromAddress e o endereco de origem do deposito (carteira do remetente).
	// Usado para screening Chainalysis no caminho de finalizacao (K6).
	// NAO e PII de anunciante — a associacao fica no cofre de compliance.
	// Campo adicionado em K6; ausencia (vazio) implica screening ignorado
	// com aviso (nao bloqueia deposit.detected, bloqueia deposit.finalized
	// quando screener estiver ativo — o custodiante DEVE enviar este campo).
	FromAddress string `json:"from_address,omitempty"`

	// AssetCode e o codigo do ativo (ex.: "USDC").
	AssetCode string `json:"asset_code"`

	// AmountMinorUnits e o valor do deposito em minor-units (int64, sem float).
	// Obrigatorio para deposit.detected e deposit.finalized.
	// Float PROIBIDO: o custodiante deve enviar minor-units inteiros.
	AmountMinorUnits int64 `json:"amount_minor_units"`

	// TenantID e o identificador pseudonimo do tenant (UUID).
	// Usado para rotear o posting ao ledger correto.
	// NAO e PII — e o tenant_id pseudonimo (TX-3).
	TenantID string `json:"tenant_id"`

	// AdvertiserID e o identificador pseudonimo do anunciante.
	// Nao e PII — e o pseudonimo usado no ledger (DA-11).
	AdvertiserID string `json:"advertiser_id"`

	// OriginalTxHash e o hash da tx original (para reorg).
	OriginalTxHash string `json:"original_tx_hash,omitempty"`

	// ReorgReason e o motivo do estorno (para reorg).
	ReorgReason string `json:"reorg_reason,omitempty"`
}

// ---------------------------------------------------------------------------
// http.Handler
// ---------------------------------------------------------------------------

// ServeHTTP implementa http.Handler.
//
// Seguranca (identico ao padrao K4):
//  1. Limite de tamanho do body.
//  2. Verificacao HMAC-SHA256 (tempo-constante, anti-replay).
//  3. Parse do payload (campos PII ausentes por design).
//  4. Despacha para handler especifico por tipo de evento.
func (h *SafeWebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 1. Limite de tamanho do body
	rawBody, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		h.logger.Warn("safe_webhook: falha ao ler body", "err", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// 2. Verificacao de assinatura
	sig := r.Header.Get("X-Safe-Signature")
	if sig == "" {
		h.logger.Warn("safe_webhook: X-Safe-Signature ausente — rejeitado")
		http.Error(w, "missing signature", http.StatusUnauthorized)
		return
	}
	if err := h.verifySignature(rawBody, sig); err != nil {
		h.logger.Warn("safe_webhook: assinatura invalida", "err", err)
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	// 3. Parse do payload
	var payload safeWebhookPayload
	if err := json.Unmarshal(rawBody, &payload); err != nil {
		h.logger.Warn("safe_webhook: payload invalido", "err", err)
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}

	// 4. Despacha por tipo
	ctx := r.Context()
	var handleErr error
	switch payload.Type {
	case eventTypeDepositDetected:
		handleErr = h.handleDepositDetected(ctx, payload)
	case eventTypeDepositFinalized:
		handleErr = h.handleDepositFinalized(ctx, payload)
	case eventTypeReorg:
		handleErr = h.handleReorg(ctx, payload)
	case eventTypePayoutSubmitted:
		// Payout submetido ao custodiante: apenas loga (nao altera ledger aqui)
		h.logger.Info("safe_webhook: payout submetido ao custodiante",
			"tx_hash", payload.TxHash,
			"chain_id", payload.ChainID,
		)
	default:
		h.logger.Debug("safe_webhook: tipo de evento ignorado", "type", payload.Type)
	}

	if handleErr != nil {
		h.logger.Error("safe_webhook: erro ao processar evento",
			"type", payload.Type,
			"tx_hash", payload.TxHash,
			"err", handleErr,
		)
		// 500 para que o custodiante retente (nao 400 que seria final)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// ---------------------------------------------------------------------------
// Handlers de eventos especificos
// ---------------------------------------------------------------------------

// handleDepositDetected processa deposit.detected.
//
// Registra o deposito como PENDING no ledger. O saldo permanece pending ate
// FinalizeDeposit (N confirmacoes via webhook). Float PROIBIDO.
// Idempotente: "deposit:{tx_hash}" — reprocessamento nao duplica.
func (h *SafeWebhookHandler) handleDepositDetected(ctx context.Context, p safeWebhookPayload) error {
	if h.pool == nil {
		return fmt.Errorf("safe_webhook: pool de banco de dados nao configurado (ambiente de teste sem banco)")
	}
	if p.TxHash == "" || p.AmountMinorUnits <= 0 {
		return fmt.Errorf("safe_webhook: deposit.detected: dados invalidos tx=%q amount=%d",
			p.TxHash, p.AmountMinorUnits)
	}
	if p.TenantID == "" {
		return fmt.Errorf("safe_webhook: deposit.detected: tenant_id obrigatorio")
	}

	// Obtem scale do ativo do Asset Registry — nunca hardcoded (TX-2)
	asset, err := h.assetLoader.LoadAsset(ctx, p.AssetCode)
	if err != nil {
		return fmt.Errorf("safe_webhook: deposit.detected: asset %q: %w", p.AssetCode, err)
	}

	// Provisiona contas (idempotente)
	codes := ledger.AccountCodes(p.AdvertiserID, p.AssetCode)
	platformCash, err := ledger.EnsureAccount(ctx, h.pool, h.assetLoader, ledger.EnsureAccountParams{
		TenantID:  p.TenantID,
		Code:      codes.PlatformCash,
		Name:      "Caixa cripto " + p.AssetCode,
		Kind:      ledger.AccountKindAsset,
		AssetCode: p.AssetCode,
		Scale:     asset.Scale,
	})
	if err != nil {
		return fmt.Errorf("safe_webhook: EnsureAccount cash: %w", err)
	}

	advBalance, err := ledger.EnsureAccount(ctx, h.pool, h.assetLoader, ledger.EnsureAccountParams{
		TenantID:  p.TenantID,
		Code:      codes.AdvBalance,
		Name:      "Saldo anunciante " + p.AssetCode,
		Kind:      ledger.AccountKindLiability,
		AssetCode: p.AssetCode,
		Scale:     asset.Scale,
	})
	if err != nil {
		return fmt.Errorf("safe_webhook: EnsureAccount adv: %w", err)
	}

	_, err = ledger.RecordDeposit(ctx, h.pool, h.assetLoader, ledger.DepositParams{
		TenantID:              p.TenantID,
		TxHash:                p.TxHash,
		AdvertiserID:          p.AdvertiserID,
		AssetCode:             p.AssetCode,
		Scale:                 asset.Scale,
		AmountMinor:           p.AmountMinorUnits, // int64, sem float (TX-2)
		EffectiveAt:           time.Now().UTC(),
		PlatformCashAccountID: platformCash.ID,
		AdvAccountID:          advBalance.ID,
	})
	if err != nil {
		return fmt.Errorf("safe_webhook: RecordDeposit: %w", err)
	}

	// Reemite no canal de WatchDeposits (se houver subscriber)
	if h.connector != nil {
		// Monta DepositEvent para WatchDeposits
		ev := chainconnector.DepositEvent{
			TxHash:        p.TxHash,
			ChainID:       p.ChainID,
			BlockNumber:   p.BlockNumber,
			Confirmations: p.Confirmations,
			ToAddress:     p.ToAddress,
			DetectedAt:    time.Now().UTC(),
		}
		// Amount montado manualmente para evitar dep circular
		// (moneyv1 ja e importado pelo chainconnector)
		h.connector.InjectDeposit(p.ToAddress, p.ChainID, ev)
	}

	h.logger.Info("safe_webhook: deposito PENDING gravado no ledger",
		"tx_hash", p.TxHash,
		"chain_id", p.ChainID,
		"asset", p.AssetCode,
		"amount_minor", p.AmountMinorUnits,
		"tenant_id", p.TenantID,
		// Sem PII: nao loga advertiser_id completo, enderecos externos, chaves
	)
	return nil
}

// handleDepositFinalized processa deposit.finalized (N confirmacoes atingidas).
//
// SCREENING (K6): antes de mover pending -> posted, screena o endereco
// originador (FromAddress) via AddressScreener se configurado.
// Fail-closed: sancao, risco alto ou screener indisponivel BLOQUEIA a
// finalizacao — o deposito permanece pending ate revisao manual.
// Se FromAddress estiver ausente e screener ativo, bloqueia com aviso.
//
// Idempotente: se ja estiver posted, retorna sem erro.
func (h *SafeWebhookHandler) handleDepositFinalized(ctx context.Context, p safeWebhookPayload) error {
	if p.TxHash == "" {
		return fmt.Errorf("safe_webhook: deposit.finalized: tx_hash obrigatorio")
	}
	if p.TenantID == "" {
		return fmt.Errorf("safe_webhook: deposit.finalized: tenant_id obrigatorio")
	}

	// K6: screening do endereco originador antes de creditar o deposito.
	// Fail-closed: qualquer erro de screening bloqueia FinalizeDeposit.
	// PII: nao loga from_address + tenant_id juntos (DA-11).
	if h.screener != nil {
		if p.FromAddress == "" {
			// Ausencia de from_address com screener ativo e tratada como
			// bloqueio — o custodiante DEVE enviar o endereco originador.
			h.logger.Warn("safe_webhook: deposit.finalized: from_address ausente com screener ativo — deposito BLOQUEADO (fail-closed)",
				"tx_hash", p.TxHash,
				"chain_id", p.ChainID,
				// Sem PII: nao loga tenant_id aqui para nao associar a from_address vazio
			)
			return fmt.Errorf("%w: from_address ausente no payload de finalizacao", ErrScreeningBlocked)
		}
		if err := h.screener.ScreenAddress(ctx, ScreenAddressParams{
			TenantID:  p.TenantID,
			Address:   p.FromAddress,
			ChainID:   p.ChainID,
			Direction: "deposit",
			TxRef:     p.TxHash,
			Network:   chainIDToNetwork(p.ChainID),
		}); err != nil {
			h.logger.Warn("safe_webhook: deposit.finalized: screening BLOQUEOU finalizacao — deposito permanece pending",
				"tx_hash", p.TxHash,
				"chain_id", p.ChainID,
				"direction", "deposit",
				"screening_err", err.Error(),
				// PII: nao loga from_address nem tenant_id juntos (DA-11)
			)
			return fmt.Errorf("%w: %w", ErrScreeningBlocked, err)
		}
	}

	// Guarda de banco: screening ja passou; agora precisamos do pool.
	if h.pool == nil {
		return fmt.Errorf("safe_webhook: pool de banco de dados nao configurado (ambiente de teste sem banco)")
	}

	err := ledger.FinalizeDeposit(ctx, h.pool, ledger.FinalizeDepositParams{
		TenantID:      p.TenantID,
		TxHash:        p.TxHash,
		Confirmations: p.Confirmations,
	})
	if err != nil {
		return fmt.Errorf("safe_webhook: FinalizeDeposit: %w", err)
	}

	h.logger.Info("safe_webhook: deposito FINALIZADO (pending->posted)",
		"tx_hash", p.TxHash,
		"chain_id", p.ChainID,
		"confirmations", p.Confirmations,
		"tenant_id", p.TenantID,
	)
	return nil
}

// chainIDToNetwork converte um EVM chain_id para o nome de rede esperado pelo
// Chainalysis. Retorna "ethereum" como default para chains desconhecidas.
func chainIDToNetwork(chainID int64) string {
	switch chainID {
	case 1:
		return "ethereum"
	case 137:
		return "polygon"
	case 56:
		return "bsc"
	case 43114:
		return "avalanche"
	default:
		return "ethereum"
	}
}

// handleReorg processa chain.reorg.
//
// Gera estorno auditavel (novo par de postings invertido).
// O posting original NUNCA e editado (invariante §2.6).
// Idempotente: "void:deposit:{original_tx_hash}".
func (h *SafeWebhookHandler) handleReorg(ctx context.Context, p safeWebhookPayload) error {
	if h.pool == nil {
		return fmt.Errorf("safe_webhook: pool de banco de dados nao configurado (ambiente de teste sem banco)")
	}
	if p.OriginalTxHash == "" {
		return fmt.Errorf("safe_webhook: chain.reorg: original_tx_hash obrigatorio")
	}
	if p.TenantID == "" {
		return fmt.Errorf("safe_webhook: chain.reorg: tenant_id obrigatorio")
	}
	if p.AmountMinorUnits <= 0 {
		return fmt.Errorf("safe_webhook: chain.reorg: amount_minor_units invalido: %d", p.AmountMinorUnits)
	}

	// Obtem scale do ativo do Asset Registry (nunca hardcoded)
	asset, err := h.assetLoader.LoadAsset(ctx, p.AssetCode)
	if err != nil {
		return fmt.Errorf("safe_webhook: chain.reorg: asset %q: %w", p.AssetCode, err)
	}

	// Reconstroi referencias de conta
	codes := ledger.AccountCodes(p.AdvertiserID, p.AssetCode)
	platformCash, err := ledger.EnsureAccount(ctx, h.pool, h.assetLoader, ledger.EnsureAccountParams{
		TenantID:  p.TenantID,
		Code:      codes.PlatformCash,
		Name:      "Caixa cripto " + p.AssetCode,
		Kind:      ledger.AccountKindAsset,
		AssetCode: p.AssetCode,
		Scale:     asset.Scale,
	})
	if err != nil {
		return fmt.Errorf("safe_webhook: reorg EnsureAccount cash: %w", err)
	}

	advBalance, err := ledger.EnsureAccount(ctx, h.pool, h.assetLoader, ledger.EnsureAccountParams{
		TenantID:  p.TenantID,
		Code:      codes.AdvBalance,
		Name:      "Saldo anunciante " + p.AssetCode,
		Kind:      ledger.AccountKindLiability,
		AssetCode: p.AssetCode,
		Scale:     asset.Scale,
	})
	if err != nil {
		return fmt.Errorf("safe_webhook: reorg EnsureAccount adv: %w", err)
	}

	reason := p.ReorgReason
	if reason == "" {
		reason = "chain_reorg"
	}

	_, err = ledger.RecordReversal(ctx, h.pool, h.assetLoader, ledger.ReversalParams{
		TenantID:              p.TenantID,
		OriginalTxHash:        p.OriginalTxHash,
		AssetCode:             p.AssetCode,
		Scale:                 asset.Scale,
		AmountMinor:           p.AmountMinorUnits, // int64, sem float (TX-2)
		EffectiveAt:           time.Now().UTC(),
		Reason:                reason,
		PlatformCashAccountID: platformCash.ID,
		AdvAccountID:          advBalance.ID,
	})
	if err != nil {
		return fmt.Errorf("safe_webhook: RecordReversal: %w", err)
	}

	h.logger.Info("safe_webhook: reorg — estorno auditavel gravado",
		"original_tx_hash", p.OriginalTxHash,
		"chain_id", p.ChainID,
		"asset", p.AssetCode,
		"amount_minor", p.AmountMinorUnits,
		"reason", reason,
		"tenant_id", p.TenantID,
		// Posting original permanece intocado — novo par de postings invertido
	)
	return nil
}

// ---------------------------------------------------------------------------
// Verificacao de assinatura HMAC-SHA256
// ---------------------------------------------------------------------------

// verifySignature verifica a assinatura HMAC-SHA256 do payload.
//
// Formato do header: "X-Safe-Signature: sha256=<hex>"
// Mensagem assinada: rawBody
// Comparacao em tempo-constante (hmac.Equal) — previne timing attack.
// Anti-replay: valida timestamp do payload dentro da janela de 10 minutos.
//
// SEGURANCA: identico ao padrao dos webhooks fiat (K4: Stripe/Asaas).
func (h *SafeWebhookHandler) verifySignature(rawBody []byte, sigHeader string) error {
	// Parse: "sha256=<hex>"
	const prefix = "sha256="
	if !strings.HasPrefix(sigHeader, prefix) {
		return ErrInvalidSignature
	}
	sigHex := sigHeader[len(prefix):]
	sigBytes, err := hex.DecodeString(sigHex)
	if err != nil {
		return ErrInvalidSignature
	}

	// Computa HMAC-SHA256 esperado
	mac := hmac.New(sha256.New, []byte(h.webhookSecret))
	mac.Write(rawBody)
	expected := mac.Sum(nil)

	// Comparacao em tempo-constante
	if !hmac.Equal(expected, sigBytes) {
		return ErrInvalidSignature
	}

	// Anti-replay: verifica timestamp dentro da janela
	// (parse rapido para evitar unmarshal completo antes da validacao)
	var ts struct {
		Timestamp int64 `json:"timestamp"`
	}
	if err := json.Unmarshal(rawBody, &ts); err != nil {
		return ErrInvalidSignature
	}
	if ts.Timestamp == 0 {
		return ErrInvalidSignature
	}
	eventTime := time.Unix(ts.Timestamp, 0)
	if time.Since(eventTime) > replayWindow {
		return ErrWebhookTooOld
	}

	return nil
}

// SubmitPayout constroi e submete um payout ao custodiante Safe.
//
// # Fluxo K6 (compliance antes do BuildPayout)
//
//  1. SCREENING (K6): screena o endereco de destino (req.ToAddress) via
//     AddressScreener, se configurado. Fail-closed: sancao, risco alto ou
//     screener indisponivel bloqueia o payout ANTES de construir a tx.
//  2. TRAVEL RULE (K6): se travelRecorder configurado, registra o payout se
//     acima do limiar. Fail-closed: ErrTravelRuleRequired bloqueia o payout.
//     Se abaixo do limiar (ErrTravelRuleBelowThreshold), prossegue normalmente.
//  3. BuildPayout via ChainConnector -> construcao da tx nao-assinada.
//  4. STUB: submissao ao Safe Transaction Service (pende de infra viva).
//
// O UnsignedTx e construido pelo ChainConnector (BuildPayout) e entao
// submetido ao custodiante via API Safe/Defender. A assinatura multisig
// e responsabilidade do custodiante — nao e realizada aqui.
//
// Idempotencia: payout:{payment_event_id} no ledger (K3).
func (h *SafeWebhookHandler) SubmitPayout(ctx context.Context, req chainconnector.PayoutRequest, connector chainconnector.ChainConnector) error {
	if req.IdempotencyKey == "" {
		return fmt.Errorf("safe_webhook: SubmitPayout: IdempotencyKey obrigatorio")
	}
	if req.Amount == nil || req.Amount.GetAmount() <= 0 {
		return fmt.Errorf("safe_webhook: SubmitPayout: Amount invalido (TX-2)")
	}

	// K6 — Passo 1: screening do endereco de destino.
	// Fail-closed: sancao, risco alto ou screener indisponivel BLOQUEIA o payout.
	// PII: nao loga to_address nem idempotency_key juntos (DA-11).
	if h.screener != nil {
		if req.ToAddress == "" {
			h.logger.Warn("safe_webhook: SubmitPayout: to_address ausente com screener ativo — payout BLOQUEADO",
				"idempotency_key", req.IdempotencyKey,
				"direction", "payout",
			)
			return fmt.Errorf("%w: to_address ausente no PayoutRequest", ErrScreeningBlocked)
		}
		if err := h.screener.ScreenAddress(ctx, ScreenAddressParams{
			// TenantID: nao disponivel no PayoutRequest; screener usa address+chain como
			// identificador principal. Idempotencia por (address, direction, tx_ref).
			TenantID:  "",
			Address:   req.ToAddress,
			ChainID:   req.ChainID,
			Direction: "payout",
			TxRef:     req.IdempotencyKey,
			Network:   chainIDToNetwork(req.ChainID),
		}); err != nil {
			h.logger.Warn("safe_webhook: SubmitPayout: screening BLOQUEOU payout",
				"idempotency_key", req.IdempotencyKey,
				"chain_id", req.ChainID,
				"direction", "payout",
				"screening_err", err.Error(),
				// PII: nao loga to_address (DA-11)
			)
			return fmt.Errorf("%w: %w", ErrScreeningBlocked, err)
		}
	}

	// K6 — Passo 2: Travel Rule para payouts acima do limiar.
	// Fail-closed: ErrTravelRuleBlocked bloqueia o payout.
	// ErrTravelRuleBelowThreshold nao e bloqueante — prossegue normalmente.
	if h.travelRecorder != nil {
		trErr := h.travelRecorder.RecordPayout(ctx, PayoutTravelRuleParams{
			TxRef:            req.IdempotencyKey,
			AmountMinorUnits: req.Amount.GetAmount(),
			AssetCode:        req.Amount.GetAssetCode(),
			OriginatorVASP:   "SAFE_PLATFORM",
		})
		if trErr != nil && !errors.Is(trErr, ErrTravelRuleBelowThreshold) {
			h.logger.Warn("safe_webhook: SubmitPayout: Travel Rule BLOQUEOU payout",
				"idempotency_key", req.IdempotencyKey,
				"asset_code", req.Amount.GetAssetCode(),
				"amount_minor", req.Amount.GetAmount(),
				"travel_rule_err", trErr.Error(),
				// PII: nao loga to_address nem nomes de originador/beneficiario (DA-11)
			)
			return fmt.Errorf("%w: %w", ErrTravelRuleBlocked, trErr)
		}
	}

	// K6 passou (ou compliance desabilitado): constroi a tx nao-assinada.
	unsignedTx, err := connector.BuildPayout(ctx, req)
	if err != nil {
		return fmt.Errorf("safe_webhook: SubmitPayout: BuildPayout: %w", err)
	}

	// STUB: submissao ao Safe Transaction Service.
	//
	// Em producao, este passo chama o Safe Transaction Service (API REST)
	// com o UnsignedTx.Payload e aguarda a coleta de assinaturas pelos
	// signatarios do multisig. As chaves dos signatarios estao no OpenBao/KMS
	// (SAFE_SIGNER_KEY_* — nunca em imagem/git, §2.7 ADR-0004).
	//
	// Referencia de implementacao futura:
	//   POST https://safe-transaction.{network}.gnosis.io/api/v1/safes/{safe_address}/multisig-transactions/
	//   Body: { to, value, data: hex(unsignedTx.Payload[20:]), safeTxGas, ... }
	//
	// O despacho HTTP ao Safe Transaction Service pende de:
	//   - SAFE_TX_SERVICE_URL (por chain)
	//   - SAFE_ADDRESS (por chain, lido do EVMSafeChainConfig)
	//   - SAFE_SIGNER_KEY_* (OpenBao — nunca em imagem/git)
	//
	// UPGRADE: quando AUM justificar Fireblocks (gatilho C ADR-0004), a
	// submissao passa a usar a API Fireblocks — nenhuma mudanca na interface
	// ChainConnector nem no trilho de pagamento.
	h.logger.Info("safe_webhook: SubmitPayout (STUB — submissao pendente de infra viva)",
		"idempotency_key", req.IdempotencyKey,
		"chain_id", req.ChainID,
		"amount_minor", req.Amount.GetAmount(),
		"asset_code", req.Amount.GetAssetCode(),
		"payload_len", len(unsignedTx.Payload),
		// Sem PII: nao loga to_address externo, chaves, segredos
	)
	// Em producao, retornaria erro se a submissao falhar.
	// STUB retorna nil (sucesso simulado para testes).
	return nil
}
