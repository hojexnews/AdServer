// Package travelrule implementa o registro de Travel Rule para transferencias
// cripto acima do limiar configuravel (K6 — ADR-0004 §E.10 / FATF Rec. 16).
//
// # Posicionamento
//
// Vive em services/payments (celula AML/KYC). NAO e importado pelo motor de
// decisao, internal/ranker, internal/cascade nem internal/decision.
//
// # Fronteira PII (TX-3 / DA-11)
//
// Dados de originador e beneficiario (nome, endereco) sao PII sensiveis e
// ficam EXCLUSIVAMENTE no cofre compliance.travel_rule_records, cifrados em
// repouso via envelope KMS antes de producao. Nunca transitam pelo ledger,
// telemetria ou logs.
//
// # Cifra em repouso (KMS envelope)
//
// O Recorder recebe uma interface KMSEnvelope (kmsenvelope.KMSEnvelope) que
// cifra OriginatorName e BeneficiaryName antes do INSERT em
// compliance.travel_rule_records. Em dev/CI, usa LocalEnvelope (chave lida
// de PII_ENVELOPE_KEY). Em producao, ExternalEnvelope com KMS real.
//
// Se envelope for nil (testes de logica pura sem PII), os nomes sao gravados
// em claro — NUNCA permitido em producao (o main.go garante isso).
//
// # Limiar configuravel
//
// O limiar de Travel Rule e configurado em minor-units do ativo (int64, TX-2:
// sem float). Acima do limiar, o registro e obrigatorio antes de submeter o
// payout. Abaixo do limiar, o payout pode seguir sem registro.
//
// # Fail-closed
//
// Se o registro falhar, a operacao de payout e bloqueada (ErrTravelRuleRequired).
// O payout NAO e submetido sem o registro de Travel Rule quando acima do limiar.
//
// # Interface VASP / Stub
//
// A troca de mensagens com o VASP receptor (ex.: Notabene, Sygna) e modelada
// pela interface VASPTransport. A implementacao padrao e StubVASPTransport,
// que registra o record como 'sent' sem envio real — troca por implementacao
// Notabene/Sygna quando disponivel sem alterar o chamador.
//
// # Invariantes inegociaveis
//
//   - amount em minor-units int64 — TX-2: sem float.
//   - PII (nomes) nunca logada — apenas ids pseudonimos em logs.
//   - Registro idempotente: (tenant_id, tx_ref) unico no cofre.
//   - Fail-closed: payout bloqueado se registro falhar acima do limiar.
//   - TX-2: cifra incide SOMENTE sobre campos TEXT de nome — NUNCA sobre
//     amount_minor_units (NUMERIC(20,0)) ou qualquer valor monetario.
package travelrule

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/hojex/adserver/services/payments/internal/kmsenvelope"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------------------------------------------------------------------------
// dbExecer — interface minima de banco injetavel (para testes)
//
// Isola o Recorder do *pgxpool.Pool concreto, permitindo que testes
// injetem um mock que capture os argumentos do Exec sem banco real.
// *pgxpool.Pool satisfaz esta interface automaticamente.
// ---------------------------------------------------------------------------

// dbExecer abstrai as operacoes de banco usadas pelo Recorder.
// Implementado por *pgxpool.Pool em producao e por fakes em testes.
type dbExecer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// ---------------------------------------------------------------------------
// Erros exportados
// ---------------------------------------------------------------------------

// ErrTravelRuleRequired e retornado quando a transferencia esta acima do limiar
// e o registro de Travel Rule falhou ou nao foi concluido.
// O chamador DEVE bloquear o payout.
var ErrTravelRuleRequired = errors.New("travelrule: registro obrigatorio — payout bloqueado (fail-closed)")

// ErrBelowThreshold e retornado quando a transferencia esta abaixo do limiar
// configurado e o registro NAO e obrigatorio.
var ErrBelowThreshold = errors.New("travelrule: abaixo do limiar — registro nao obrigatorio")

// ---------------------------------------------------------------------------
// Tipos
// ---------------------------------------------------------------------------

// Direction indica o sentido da transferencia.
type Direction string

const (
	// DirectionOutbound e uma transferencia de saida (payout da plataforma).
	DirectionOutbound Direction = "outbound"
	// DirectionInbound e uma transferencia de entrada (deposito recebido).
	DirectionInbound Direction = "inbound"
)

// RecordParams agrupa os parametros para registro de Travel Rule.
//
// PII: OriginatorName e BeneficiaryName sao PII sensiveis — a camada de
// aplicacao deve cifra-los via envelope KMS antes de passar para Record.
// Nunca logar esses campos.
type RecordParams struct {
	// TenantID e o UUID pseudonimo do tenant.
	TenantID string
	// TxRef e a referencia da transacao (hash on-chain ou idempotency_key do payout).
	TxRef string
	// OriginatorVASP e o identificador do VASP de origem (ex.: "SAFE_PLATFORM", LEI, BIC).
	OriginatorVASP string
	// BeneficiaryVASP e o identificador do VASP receptor.
	BeneficiaryVASP string
	// OriginatorName e o nome do originador (PII — cifrar antes de INSERT).
	// Pode ser vazio se o VASP de origem e a plataforma e o nome ja e conhecido.
	OriginatorName string
	// BeneficiaryName e o nome do beneficiario (PII — cifrar antes de INSERT).
	BeneficiaryName string
	// AmountMinorUnits e o valor da transferencia em minor-units (int64, TX-2: sem float).
	AmountMinorUnits int64
	// AssetCode e o codigo do ativo (ex.: "USDC").
	AssetCode string
	// Direction indica se e outbound (payout) ou inbound (deposito).
	Direction Direction
}

// ---------------------------------------------------------------------------
// VASPTransport — interface para envio do registro ao VASP receptor
// ---------------------------------------------------------------------------

// VASPTransport abstrai o envio do registro de Travel Rule ao VASP receptor.
// A implementacao padrao (StubVASPTransport) nao envia — marca como 'sent'.
// Implementacoes reais: Notabene, Sygna, OpenVASP.
type VASPTransport interface {
	// Send envia o registro de Travel Rule ao VASP receptor.
	// Retorna o status resultante ("sent", "confirmed") ou erro.
	// Nao deve logar PII dos parametros.
	Send(ctx context.Context, p RecordParams) (string, error)
}

// StubVASPTransport e a implementacao stub do VASPTransport.
// Registra o record como 'sent' sem envio real ao VASP.
// Usado em desenvolvimento, staging e como fallback ate integracao real.
type StubVASPTransport struct{}

// Send na implementacao stub registra sucesso imediato sem enviar.
func (StubVASPTransport) Send(_ context.Context, _ RecordParams) (string, error) {
	return "sent", nil
}

// ---------------------------------------------------------------------------
// Recorder
// ---------------------------------------------------------------------------

// Config agrupa a configuracao do Recorder de Travel Rule.
type Config struct {
	// ThresholdMinorUnits e o limiar em minor-units do ativo acima do qual
	// o registro e obrigatorio. TX-2: int64, sem float.
	// Default: 0 (registrar todas as transferencias — comportamento conservador).
	// Para USDC (scale=6): USD 1.000 = 1_000_000_000 minor-units.
	// Para BRL  (scale=2): BRL 1.000 = 100_000 minor-units.
	ThresholdMinorUnits int64

	// AssetCode filtra o limiar para um ativo especifico.
	// Se vazio, aplica-se a todos os ativos.
	AssetCode string
}

// Recorder registra transferencias cripto acima do limiar em compliance.travel_rule_records.
type Recorder struct {
	cfg       Config
	pool      dbExecer // *pgxpool.Pool em producao; fake em testes (LOW-2)
	transport VASPTransport
	envelope  kmsenvelope.KMSEnvelope // cifra OriginatorName e BeneficiaryName antes do INSERT
	logger    *slog.Logger
}

// New cria um Recorder de Travel Rule.
//
// pool pode ser nil em testes — gravar no banco sera ignorado.
// transport pode ser nil — usa StubVASPTransport por padrao.
// envelope pode ser nil APENAS em testes de logica pura sem banco.
// Em producao (pool nao-nil), envelope DEVE ser nao-nil — insertRecord
// falha com erro explicito se envelope for nil e houver PII a cifrar.
func New(cfg Config, pool *pgxpool.Pool, transport VASPTransport, envelope kmsenvelope.KMSEnvelope, logger *slog.Logger) *Recorder {
	if logger == nil {
		logger = slog.Default()
	}
	if transport == nil {
		transport = StubVASPTransport{}
	}
	var db dbExecer
	if pool != nil {
		db = pool
	}
	return &Recorder{
		cfg:       cfg,
		pool:      db,
		transport: transport,
		envelope:  envelope,
		logger:    logger,
	}
}

// newWithDB cria um Recorder injetando qualquer dbExecer (uso em testes).
// Nao exportado: os testes do pacote acessam diretamente (package travelrule).
func newWithDB(cfg Config, db dbExecer, transport VASPTransport, envelope kmsenvelope.KMSEnvelope, logger *slog.Logger) *Recorder {
	if logger == nil {
		logger = slog.Default()
	}
	if transport == nil {
		transport = StubVASPTransport{}
	}
	return &Recorder{
		cfg:       cfg,
		pool:      db,
		transport: transport,
		envelope:  envelope,
		logger:    logger,
	}
}

// ---------------------------------------------------------------------------
// Record registra uma transferencia cripto se estiver acima do limiar.
//
// FLUXO (ADR-0004 §E.10 / K6):
//  1. Verifica se o valor esta acima do limiar configurado.
//     - Abaixo: retorna ErrBelowThreshold (nao e erro bloqueante — apenas
//       indica que o registro nao e necessario).
//  2. Persiste o record em compliance.travel_rule_records (idempotente).
//  3. Envia ao VASP receptor via VASPTransport.
//  4. Atualiza o status no cofre (sent/failed).
//
// Fail-closed: se o INSERT ou o envio falharem, retorna ErrTravelRuleRequired
// — o chamador DEVE bloquear o payout.
//
// PII: OriginatorName e BeneficiaryName NUNCA logados — apenas TxRef e ids
// pseudonimos aparecem em logs (DA-11).
// ---------------------------------------------------------------------------

// Record registra a transferencia se acima do limiar.
// Retorna ErrBelowThreshold se abaixo (nao bloqueante).
// Retorna ErrTravelRuleRequired se acima e o registro falhar (bloqueante).
func (r *Recorder) Record(ctx context.Context, p RecordParams) error {
	if p.TenantID == "" {
		return fmt.Errorf("travelrule: TenantID obrigatorio")
	}
	if p.TxRef == "" {
		return fmt.Errorf("travelrule: TxRef obrigatorio")
	}
	if p.AmountMinorUnits < 0 {
		return fmt.Errorf("travelrule: AmountMinorUnits invalido: %d", p.AmountMinorUnits)
	}

	// Verifica limiar (TX-2: comparacao de int64, sem float)
	if r.cfg.ThresholdMinorUnits > 0 && p.AmountMinorUnits < r.cfg.ThresholdMinorUnits {
		r.logger.Debug("travelrule: abaixo do limiar — sem registro",
			"tx_ref", p.TxRef,
			"amount_minor", p.AmountMinorUnits,
			"threshold", r.cfg.ThresholdMinorUnits,
			// Sem PII: nao loga nomes, tenant_id em debug
		)
		return ErrBelowThreshold
	}

	// Persiste o record no cofre (idempotente)
	if err := r.insertRecord(ctx, p); err != nil {
		r.logger.Error("travelrule: falha ao inserir no cofre — payout BLOQUEADO (fail-closed)",
			"tx_ref", p.TxRef,
			"err", err,
			// Sem PII: nao loga nomes
		)
		return fmt.Errorf("%w: insert: %w", ErrTravelRuleRequired, err)
	}

	// Envia ao VASP receptor via transport
	status, err := r.transport.Send(ctx, p)
	if err != nil {
		// Falha no envio: atualiza status para 'failed' e bloqueia
		_ = r.updateStatus(ctx, p.TenantID, p.TxRef, "failed")
		r.logger.Error("travelrule: falha no envio ao VASP — payout BLOQUEADO (fail-closed)",
			"tx_ref", p.TxRef,
			"err", err,
		)
		return fmt.Errorf("%w: send: %w", ErrTravelRuleRequired, err)
	}

	// Atualiza status para 'sent' (ou 'confirmed' se o transport confirmar imediatamente)
	if err := r.updateStatus(ctx, p.TenantID, p.TxRef, status); err != nil {
		// Falha ao atualizar status: log + nao bloqueia (record ja esta no cofre)
		r.logger.Warn("travelrule: falha ao atualizar status apos envio",
			"tx_ref", p.TxRef,
			"status", status,
			"err", err,
		)
	}

	r.logger.Info("travelrule: registro concluido",
		"tx_ref", p.TxRef,
		"status", status,
		"asset", p.AssetCode,
		"direction", string(p.Direction),
		"amount_minor", p.AmountMinorUnits,
		// Sem PII: nao loga nomes de originador/beneficiario
	)
	return nil
}

// AboveThreshold retorna true se o valor esta acima do limiar configurado.
// Util para pre-verificacao antes de construir o RecordParams completo.
// TX-2: comparacao int64, sem float.
func (r *Recorder) AboveThreshold(amountMinorUnits int64) bool {
	if r.cfg.ThresholdMinorUnits <= 0 {
		return true // sem limiar = sempre acima
	}
	return amountMinorUnits >= r.cfg.ThresholdMinorUnits
}

// ---------------------------------------------------------------------------
// Helpers privados
// ---------------------------------------------------------------------------

// insertRecord persiste o record em compliance.travel_rule_records (idempotente).
// Se pool for nil (testes), retorna nil.
//
// PII (TX-3 / DA-11): OriginatorName e BeneficiaryName sao cifrados via
// envelope KMS (AES-256-GCM) antes do INSERT. Se envelope for nil e houver
// PII a cifrar, retorna erro — producao nao pode inserir PII em claro.
//
// TX-2: amount_minor_units e int64 -> NUMERIC(20,0); a cifra NAO e chamada
// sobre este campo nem sobre qualquer valor monetario.
func (r *Recorder) insertRecord(ctx context.Context, p RecordParams) error {
	if r.pool == nil {
		return nil
	}

	// Cifra OriginatorName (PII) antes do INSERT.
	// TX-3: nome cifrado — nunca em claro no cofre de compliance.
	var originatorName *string
	if p.OriginatorName != "" {
		if r.envelope == nil {
			return fmt.Errorf("travelrule: envelope KMS nao configurado — nao e possivel inserir PII em claro em producao")
		}
		sealed, err := r.envelope.Seal(p.OriginatorName)
		if err != nil {
			return fmt.Errorf("travelrule: cifrar originator_name: %w", err)
		}
		originatorName = &sealed
	}

	// Cifra BeneficiaryName (PII) antes do INSERT.
	// TX-3: nome cifrado — nunca em claro no cofre de compliance.
	var beneficiaryName *string
	if p.BeneficiaryName != "" {
		if r.envelope == nil {
			return fmt.Errorf("travelrule: envelope KMS nao configurado — nao e possivel inserir PII em claro em producao")
		}
		sealed, err := r.envelope.Seal(p.BeneficiaryName)
		if err != nil {
			return fmt.Errorf("travelrule: cifrar beneficiary_name: %w", err)
		}
		beneficiaryName = &sealed
	}

	// amount_minor_units: NUMERIC(20,0) — TX-2: sem float; NAO cifrado.
	_, err := r.pool.Exec(ctx,
		`INSERT INTO compliance.travel_rule_records
		   (tenant_id, tx_ref, originator_vasp, beneficiary_vasp,
		    originator_name, beneficiary_name,
		    threshold_currency, amount_minor_units, status, provider)
		 VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8::numeric, 'pending', 'stub')
		 ON CONFLICT (tenant_id, tx_ref) DO NOTHING`,
		p.TenantID,
		p.TxRef,
		p.OriginatorVASP,
		p.BeneficiaryVASP,
		originatorName,  // ciphertext base64url ou NULL — nunca plaintext PII
		beneficiaryName, // ciphertext base64url ou NULL — nunca plaintext PII
		p.AssetCode,
		p.AmountMinorUnits, // int64 -> NUMERIC(20,0); nao cifrado (TX-2)
	)
	if err != nil {
		return fmt.Errorf("travelrule: insertRecord: %w", err)
	}
	return nil
}

// updateStatus atualiza o status do record no cofre.
// Se pool for nil (testes), retorna nil.
func (r *Recorder) updateStatus(ctx context.Context, tenantID, txRef, status string) error {
	if r.pool == nil {
		return nil
	}
	_, err := r.pool.Exec(ctx,
		`UPDATE compliance.travel_rule_records
		 SET status = $1, updated_at = $2
		 WHERE tenant_id = $3::uuid AND tx_ref = $4`,
		status, time.Now().UTC(), tenantID, txRef,
	)
	if err != nil {
		return fmt.Errorf("travelrule: updateStatus: %w", err)
	}
	return nil
}
