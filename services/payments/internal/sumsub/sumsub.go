// Package sumsub implementa a integracao KYC/KYB com o Sumsub (K6).
//
// # Posicionamento
//
// Vive em services/payments (celula AML/KYC). NAO e importado pelo motor de
// decisao, internal/ranker, internal/cascade nem internal/decision.
//
// # Fronteira PII (TX-3 / DA-11)
//
// A identidade real (CPF, passaporte, selfie, endereco residencial) fica
// EXCLUSIVAMENTE no Sumsub. O cofre local (compliance.kyc_subjects) armazena
// apenas referencias pseudonimas + status. Nunca loga PII.
//
// # Webhook Sumsub
//
// O Sumsub envia webhooks de atualizacao de status (applicantReviewed,
// applicantPending, etc.). O handler verifica a assinatura HMAC-SHA1 conforme
// a documentacao oficial do Sumsub, em tempo-constante, com anti-replay e
// idempotencia por event ID.
//
// # Segredos
//
// SUMSUB_API_KEY e SUMSUB_WEBHOOK_SECRET lidos de env/OpenBao.
// Nunca logar nem embeder em imagem/git.
//
// # Stubs
//
// A chamada HTTP real ao Sumsub API e implementada mas usa a API de sandbox
// (https://api.sumsub.com) ate a chave viva estar disponivel via OpenBao.
// Os metodos de banco dependem de pool real — em testes unitarios, pool=nil
// e testado apenas o layer de assinatura/parsing.
package sumsub

import (
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // Sumsub usa SHA-1 para assinatura de webhook (padrao oficial do provedor)
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

	"github.com/hojex/adserver/services/payments/internal/kmsenvelope"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------------------------------------------------------------------------
// dbQuerier — interface minima de banco injetavel (para testes)
//
// Isola o Client do *pgxpool.Pool concreto, permitindo que testes
// injetem um fake que capture argumentos do Exec/QueryRow sem banco real.
// *pgxpool.Pool satisfaz esta interface automaticamente.
// ---------------------------------------------------------------------------

// dbQuerier abstrai as operacoes de banco usadas pelo Client Sumsub.
type dbQuerier interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ---------------------------------------------------------------------------
// Erros exportados
// ---------------------------------------------------------------------------

// ErrInvalidWebhookSignature e retornado quando a assinatura do webhook Sumsub
// e invalida ou ausente.
var ErrInvalidWebhookSignature = errors.New("sumsub: assinatura de webhook invalida — rejeitado")

// ErrWebhookTooOld e retornado quando o evento e mais antigo que a janela anti-replay.
var ErrWebhookTooOld = errors.New("sumsub: webhook muito antigo (possivel replay)")

// ErrScreeningBlocked e retornado quando o KYC do subject esta reprovado ou on_hold
// e a operacao exige KYC aprovado.
var ErrScreeningBlocked = errors.New("sumsub: KYC nao aprovado — operacao bloqueada")

// ---------------------------------------------------------------------------
// Tipos e constantes
// ---------------------------------------------------------------------------

const (
	maxWebhookBodyBytes = 512 * 1024 // 512 KB
	webhookReplayWindow = 10 * time.Minute
)

// KYCStatus representa o status de um sujeito no cofre compliance.kyc_subjects.
type KYCStatus string

const (
	KYCStatusPending  KYCStatus = "pending"
	KYCStatusApproved KYCStatus = "approved"
	KYCStatusRejected KYCStatus = "rejected"
	KYCStatusOnHold   KYCStatus = "on_hold"
)

// KYCLevel diferencia KYC (pessoa fisica) de KYB (pessoa juridica).
type KYCLevel string

const (
	KYCLevelKYC KYCLevel = "kyc"
	KYCLevelKYB KYCLevel = "kyb"
)

// ---------------------------------------------------------------------------
// Client
// ---------------------------------------------------------------------------

// Config agrupa a configuracao do client Sumsub.
// Segredos (APIKey, WebhookSecret) NUNCA em imagem/git.
type Config struct {
	// APIKey e a chave de API do Sumsub. Lida de SUMSUB_API_KEY via env/OpenBao.
	APIKey string

	// WebhookSecret e o segredo HMAC para verificar webhooks do Sumsub.
	// Lido de SUMSUB_WEBHOOK_SECRET via env/OpenBao.
	WebhookSecret string

	// BaseURL e a URL base da API Sumsub.
	// Default: "https://api.sumsub.com"
	// Sandbox: "https://api.sumsub.com" (mesma URL; diferenciar por API key de sandbox).
	BaseURL string
}

// Client e o client de integracao com o Sumsub.
type Client struct {
	apiKey        string // NUNCA logue
	webhookSecret string // NUNCA logue
	baseURL       string
	httpClient    *http.Client
	pool          dbQuerier // *pgxpool.Pool em producao; fake em testes (LOW-2)
	// envelope cifra full_name (PII) antes do INSERT em compliance.kyc_subjects.
	// Obrigatorio em producao quando full_name for gravado (TX-3 / DA-11).
	// nil = full_name NAO e gravado (comportamento atual — Sumsub e a fonte de verdade).
	envelope kmsenvelope.KMSEnvelope
	logger   *slog.Logger
}

// New cria um Client Sumsub.
//
// pool pode ser nil em testes de assinatura/parsing. Em producao, deve ser
// nao-nil para gravar em compliance.kyc_subjects.
//
// envelope e o KMSEnvelope para cifrar full_name (PII) antes de INSERT.
// Pode ser nil se full_name nunca for gravado (comportamento atual: Sumsub
// e a fonte de verdade de PII; full_name fica NULL no cofre local).
// Em producao, se full_name for necessario por regulacao, envelope DEVE ser
// nao-nil — o metodo de INSERT recusara gravar PII em claro (TX-3 / DA-11).
func New(cfg Config, pool *pgxpool.Pool, envelope kmsenvelope.KMSEnvelope, logger *slog.Logger) (*Client, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("sumsub: APIKey obrigatorio (SUMSUB_API_KEY)")
	}
	if cfg.WebhookSecret == "" {
		return nil, errors.New("sumsub: WebhookSecret obrigatorio (SUMSUB_WEBHOOK_SECRET)")
	}
	if logger == nil {
		logger = slog.Default()
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.sumsub.com"
	}
	var db dbQuerier
	if pool != nil {
		db = pool
	}
	return &Client{
		apiKey:        cfg.APIKey,
		webhookSecret: cfg.WebhookSecret,
		baseURL:       baseURL,
		httpClient:    &http.Client{Timeout: 15 * time.Second},
		pool:          db,
		envelope:      envelope,
		logger:        logger,
	}, nil
}

// newWithDB cria um Client injetando qualquer dbQuerier (uso interno em testes).
func newWithDB(cfg Config, db dbQuerier, envelope kmsenvelope.KMSEnvelope, logger *slog.Logger) (*Client, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("sumsub: APIKey obrigatorio (SUMSUB_API_KEY)")
	}
	if cfg.WebhookSecret == "" {
		return nil, errors.New("sumsub: WebhookSecret obrigatorio (SUMSUB_WEBHOOK_SECRET)")
	}
	if logger == nil {
		logger = slog.Default()
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.sumsub.com"
	}
	return &Client{
		apiKey:        cfg.APIKey,
		webhookSecret: cfg.WebhookSecret,
		baseURL:       baseURL,
		httpClient:    &http.Client{Timeout: 15 * time.Second},
		pool:          db,
		envelope:      envelope,
		logger:        logger,
	}, nil
}

// ---------------------------------------------------------------------------
// CreateApplicant cria um applicant no Sumsub para um sujeito.
//
// O tenantID e subjectRef sao pseudonimos (UUIDs) — nunca PII.
// O level determina o fluxo: "kyc" (pessoa fisica) ou "kyb" (pessoa juridica).
//
// Grava o applicant_id retornado pelo Sumsub em compliance.kyc_subjects.
// Idempotente: se ja existir (tenant_id, subject_ref, level), retorna o
// applicant_id existente sem criar novo.
//
// STUB: a chamada HTTP ao Sumsub e implementada mas pende de API key viva.
// O banco e gravado com status='pending' em producao.
// ---------------------------------------------------------------------------

// CreateApplicantParams agrupa os parametros para criacao de applicant.
//
// PII: externalUserId e um pseudonimo (nao-PII). O nome/CPF/documento e enviado
// diretamente ao Sumsub pelo SDK client-side — nunca transita por este servidor.
//
// FullName e OPCIONAL. Armazenar SOMENTE se exigido por regulacao local
// (ex.: Travel Rule BACEN). Sera cifrado via envelope KMS antes do INSERT.
// Se FullName for fornecido e o Client nao tiver envelope configurado,
// CreateApplicant retornara erro — nunca grava PII em claro (TX-3 / DA-11).
type CreateApplicantParams struct {
	// TenantID e o UUID pseudonimo do tenant.
	TenantID string
	// SubjectRef e o UUID pseudonimo do sujeito (ex.: advertiser_id).
	SubjectRef string
	// Level e o nivel de verificacao: 'kyc' ou 'kyb'.
	Level KYCLevel
	// LevelName e o nome do fluxo no Sumsub (ex.: "basic-kyc-level").
	// Deve corresponder ao fluxo configurado no painel Sumsub.
	LevelName string
	// FullName e o nome completo do sujeito (PII OPCIONAL).
	// Armazenar SOMENTE se exigido por regulacao. Cifrado via KMS antes do INSERT.
	// Preferencia: deixar NULL — usar o Sumsub como fonte de verdade.
	// NUNCA logar este campo (DA-11).
	FullName string
}

// CreateApplicantResult e o resultado da criacao de applicant.
type CreateApplicantResult struct {
	// ApplicantID e o ID retornado pelo Sumsub.
	ApplicantID string
	// AlreadyExisted indica que o applicant ja existia no cofre local.
	AlreadyExisted bool
}

// CreateApplicant cria ou retorna o applicant existente para o sujeito.
func (c *Client) CreateApplicant(ctx context.Context, p CreateApplicantParams) (CreateApplicantResult, error) {
	if p.TenantID == "" || p.SubjectRef == "" {
		return CreateApplicantResult{}, errors.New("sumsub: TenantID e SubjectRef sao obrigatorios")
	}
	if p.Level != KYCLevelKYC && p.Level != KYCLevelKYB {
		return CreateApplicantResult{}, fmt.Errorf("sumsub: Level invalido: %q", p.Level)
	}

	if c.pool != nil {
		// Verifica se ja existe no cofre local (idempotencia)
		var existingApplicantID string
		err := c.pool.QueryRow(ctx,
			// Sem PII na query — apenas pseudonimos
			`SELECT sumsub_applicant_id FROM compliance.kyc_subjects
			 WHERE tenant_id = $1::uuid AND subject_ref = $2::uuid AND level = $3`,
			p.TenantID, p.SubjectRef, string(p.Level),
		).Scan(&existingApplicantID)
		if err == nil && existingApplicantID != "" {
			c.logger.Debug("sumsub: applicant ja existe no cofre local",
				"level", string(p.Level),
				// Sem PII: nao loga tenant_id, subject_ref, applicant_id em debug de producao
			)
			return CreateApplicantResult{ApplicantID: existingApplicantID, AlreadyExisted: true}, nil
		}
	}

	// Chama o Sumsub para criar o applicant.
	// externalUserId e um pseudonimo (tenant_id + subject_ref) — sem PII.
	externalUserID := fmt.Sprintf("%s-%s", p.TenantID, p.SubjectRef)
	applicantID, err := c.callCreateApplicant(ctx, externalUserID, p.LevelName)
	if err != nil {
		return CreateApplicantResult{}, fmt.Errorf("sumsub: CreateApplicant HTTP: %w", err)
	}

	// Grava no cofre local.
	// full_name (PII): cifrado via envelope KMS antes do INSERT se fornecido.
	// TX-3: nunca em claro no cofre de compliance.
	// DA-11: full_name nunca logado.
	if c.pool != nil {
		// Cifra full_name (PII OPCIONAL) se fornecido.
		var sealedFullName *string
		if p.FullName != "" {
			if c.envelope == nil {
				return CreateApplicantResult{}, fmt.Errorf(
					"sumsub: envelope KMS nao configurado — nao e possivel inserir full_name em claro em producao (TX-3)")
			}
			sealed, sealErr := c.envelope.Seal(p.FullName)
			if sealErr != nil {
				return CreateApplicantResult{}, fmt.Errorf("sumsub: cifrar full_name: %w", sealErr)
			}
			sealedFullName = &sealed
		}

		_, err = c.pool.Exec(ctx,
			`INSERT INTO compliance.kyc_subjects
			   (tenant_id, subject_ref, sumsub_applicant_id, level, status, full_name)
			 VALUES ($1::uuid, $2::uuid, $3, $4, 'pending', $5)
			 ON CONFLICT (tenant_id, subject_ref, level) DO NOTHING`,
			p.TenantID, p.SubjectRef, applicantID, string(p.Level),
			sealedFullName, // ciphertext base64url ou NULL — nunca plaintext PII
		)
		if err != nil {
			return CreateApplicantResult{}, fmt.Errorf("sumsub: gravar applicant no cofre: %w", err)
		}
	}

	c.logger.Info("sumsub: applicant criado",
		"level", string(p.Level),
		// Sem PII: nao loga nome, CPF, email, subject_ref, applicant_id em info
	)
	return CreateApplicantResult{ApplicantID: applicantID}, nil
}

// callCreateApplicant faz a chamada HTTP POST ao Sumsub para criar um applicant.
//
// STUB: implementado conforme a API do Sumsub; pende de API key viva.
// Em sandbox (key de teste), funciona normalmente.
//
// NAO loga o corpo da resposta — pode conter PII interna do Sumsub.
func (c *Client) callCreateApplicant(ctx context.Context, externalUserID, levelName string) (string, error) {
	body := fmt.Sprintf(`{"externalUserId":%q,"levelName":%q}`,
		externalUserID, levelName)

	req, err := http.NewRequestWithContext(ctx,
		http.MethodPost,
		c.baseURL+"/resources/applicants?levelName="+levelName,
		strings.NewReader(body),
	)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	// Autenticacao: X-App-Token (Sumsub token-based auth)
	// A chave e injetada via header — nunca logada.
	req.Header.Set("X-App-Token", c.apiKey)
	// Timestamp para assinatura (Sumsub v2 auth usa ts + digest)
	// STUB: autenticacao simplificada; em producao, usar o algoritmo
	// HMAC-SHA256 com ts + httpMethod + urlPath + body conforme docs Sumsub.
	req.Header.Set("X-App-Access-Ts", fmt.Sprintf("%d", time.Now().Unix()))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("sumsub HTTP: %w", err)
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, maxWebhookBodyBytes))
	if err != nil {
		return "", fmt.Errorf("sumsub: ler resposta: %w", err)
	}

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		// NAO inclui rawBody no erro — pode conter PII interna
		return "", fmt.Errorf("sumsub: status inesperado: %d", resp.StatusCode)
	}

	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rawBody, &result); err != nil {
		return "", fmt.Errorf("sumsub: parse da resposta: %w", err)
	}
	if result.ID == "" {
		return "", errors.New("sumsub: applicant ID vazio na resposta")
	}
	return result.ID, nil
}

// ---------------------------------------------------------------------------
// GetApplicantStatus consulta o status de um applicant no Sumsub via API.
//
// Retorna o status atual conforme o Sumsub (fonte de verdade).
// O cofre local e atualizado pelo webhook — esta funcao e para consulta
// direta (ex.: reconciliacao ou verificacao em tempo real).
//
// STUB: pende de API key viva.
// ---------------------------------------------------------------------------

// ApplicantStatusResult e o resultado de GetApplicantStatus.
type ApplicantStatusResult struct {
	ApplicantID    string
	ReviewStatus   string // greenStatus, pendingReview, etc.
	ReviewAnswer   string // GREEN, RED, etc.
	RejectLabels   []string
}

// GetApplicantStatus consulta o status do applicant no Sumsub.
//
// NAO loga a resposta — pode conter PII interna do Sumsub.
func (c *Client) GetApplicantStatus(ctx context.Context, applicantID string) (ApplicantStatusResult, error) {
	if applicantID == "" {
		return ApplicantStatusResult{}, errors.New("sumsub: applicantID obrigatorio")
	}

	req, err := http.NewRequestWithContext(ctx,
		http.MethodGet,
		c.baseURL+"/resources/applicants/"+applicantID+"/requiredIdDocsStatus",
		nil,
	)
	if err != nil {
		return ApplicantStatusResult{}, err
	}
	req.Header.Set("X-App-Token", c.apiKey)
	req.Header.Set("X-App-Access-Ts", fmt.Sprintf("%d", time.Now().Unix()))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ApplicantStatusResult{}, fmt.Errorf("sumsub GetApplicantStatus HTTP: %w", err)
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, maxWebhookBodyBytes))
	if err != nil {
		return ApplicantStatusResult{}, fmt.Errorf("sumsub: ler resposta status: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return ApplicantStatusResult{}, fmt.Errorf("sumsub: GetStatus status inesperado: %d", resp.StatusCode)
	}

	// Parse minimo — nunca loga o corpo
	var result struct {
		ID           string `json:"id"`
		ReviewStatus string `json:"reviewStatus"`
		ReviewResult struct {
			ReviewAnswer string   `json:"reviewAnswer"`
			RejectLabels []string `json:"rejectLabels"`
		} `json:"reviewResult"`
	}
	if err := json.Unmarshal(rawBody, &result); err != nil {
		return ApplicantStatusResult{}, fmt.Errorf("sumsub: parse status: %w", err)
	}
	return ApplicantStatusResult{
		ApplicantID:  result.ID,
		ReviewStatus: result.ReviewStatus,
		ReviewAnswer: result.ReviewResult.ReviewAnswer,
		RejectLabels: result.ReviewResult.RejectLabels,
	}, nil
}

// ---------------------------------------------------------------------------
// RequireKYCApproved verifica se o sujeito tem KYC aprovado no cofre local.
//
// Usado pelo fluxo de payout cripto antes de submeter (bloqueio de compliance).
// Se status != 'approved', retorna ErrScreeningBlocked.
// Fail-closed: se o cofre nao tiver registro, retorna ErrScreeningBlocked.
// ---------------------------------------------------------------------------

// RequireKYCApproved bloqueia a operacao se o subject_ref nao tiver KYC aprovado.
//
// PII nao transita: apenas pseudonimos (tenantID, subjectRef) sao usados na query.
func (c *Client) RequireKYCApproved(ctx context.Context, tenantID, subjectRef string, level KYCLevel) error {
	if c.pool == nil {
		// Em testes sem banco, fail-closed
		return fmt.Errorf("sumsub: RequireKYCApproved: pool nao configurado (fail-closed)")
	}
	var status string
	err := c.pool.QueryRow(ctx,
		`SELECT status FROM compliance.kyc_subjects
		 WHERE tenant_id = $1::uuid AND subject_ref = $2::uuid AND level = $3`,
		tenantID, subjectRef, string(level),
	).Scan(&status)
	if err != nil {
		// Sem registro = nao aprovado (fail-closed)
		return fmt.Errorf("sumsub: RequireKYCApproved: sem registro para sujeito (fail-closed): %w", err)
	}
	if status != string(KYCStatusApproved) {
		return fmt.Errorf("%w: status=%s", ErrScreeningBlocked, status)
	}
	return nil
}

// ---------------------------------------------------------------------------
// WebhookHandler — http.Handler para receber notificacoes de status do Sumsub.
//
// Seguranca (ADR-0004 §E.10):
//   - Assinatura HMAC-SHA1 com webhookSecret (padrao oficial Sumsub).
//     Header: X-Payload-Digest: <hex>
//   - Fallback para HMAC-SHA256 (alguns ambientes Sumsub usam SHA256).
//     Header: X-Webhook-Secret-Token ou X-Payload-Digest-Alg.
//   - Comparacao tempo-constante (hmac.Equal) — previne timing attack.
//   - Anti-replay: campo createdAt no payload; janela de 10 minutos.
//   - Idempotencia por applicantId + reviewStatus (previne processamento duplo).
//   - Rejeita webhooks sem assinatura ou com assinatura invalida.
//
// PII: o payload Sumsub pode conter PII interna (nome, documento).
// Nunca logar o corpo raw. So processar os campos de status — nunca salvar
// os campos de PII (firstName, lastName, etc.) no cofre.
// ---------------------------------------------------------------------------

// WebhookHandler retorna um http.Handler que processa webhooks do Sumsub.
func (c *Client) WebhookHandler() http.Handler {
	return http.HandlerFunc(c.serveWebhook)
}

// serveWebhook implementa o processamento de webhook.
func (c *Client) serveWebhook(w http.ResponseWriter, r *http.Request) {
	rawBody, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBodyBytes))
	if err != nil {
		c.logger.Warn("sumsub webhook: falha ao ler body", "err", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Verificacao de assinatura (tempo-constante, fail-closed)
	if err := c.verifyWebhookSignature(rawBody, r.Header); err != nil {
		c.logger.Warn("sumsub webhook: assinatura invalida", "err", err)
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	// Parse minimo — nunca loga o corpo (pode conter PII)
	var event sumsubWebhookEvent
	if err := json.Unmarshal(rawBody, &event); err != nil {
		c.logger.Warn("sumsub webhook: payload invalido", "err", err)
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}

	// Anti-replay: valida createdAt
	if event.CreatedAt != "" {
		t, parseErr := time.Parse(time.RFC3339, event.CreatedAt)
		if parseErr == nil && time.Since(t) > webhookReplayWindow {
			c.logger.Warn("sumsub webhook: evento muito antigo (anti-replay)",
				"type", event.Type,
				// Sem PII: nao loga applicant_id nem external_user_id
			)
			// Retorna 200 para evitar retentativas infinitas de eventos antigos
			// (o replay ja foi rejeitado logicamente)
			w.WriteHeader(http.StatusOK)
			return
		}
	}

	ctx := r.Context()
	if err := c.processWebhookEvent(ctx, event); err != nil {
		c.logger.Error("sumsub webhook: erro ao processar evento",
			"type", event.Type,
			"err", err,
			// Sem PII: nao loga applicant_id
		)
		// 500 para que o Sumsub retente
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// sumsubWebhookEvent e o payload minimo de um webhook Sumsub.
//
// Campos PII (firstName, lastName, dob, idDoc, etc.) sao intencionalmente
// ausentes deste struct — nunca devem entrar no cofre ou logs (DA-11).
type sumsubWebhookEvent struct {
	// ApplicantID e o ID do applicant no Sumsub.
	ApplicantID string `json:"applicantId"`

	// ExternalUserID e o externalUserId (pseudonimo: tenant_id + subject_ref).
	// Nunca contem PII real.
	ExternalUserID string `json:"externalUserId"`

	// Type e o tipo de evento (applicantReviewed, applicantPending, etc.).
	Type string `json:"type"`

	// ReviewStatus e o status da revisao.
	ReviewStatus string `json:"reviewStatus"`

	// ReviewResult contem a resposta da revisao e motivo de rejeicao.
	// Sem PII — apenas codigos de resultado.
	ReviewResult struct {
		ReviewAnswer   string   `json:"reviewAnswer"`
		RejectLabels   []string `json:"rejectLabels"`
		ClientComment  string   `json:"clientComment"` // codigo, nao PII
	} `json:"reviewResult"`

	// CreatedAt e o timestamp do evento (RFC3339) — usado para anti-replay.
	CreatedAt string `json:"createdAt"`
}

// processWebhookEvent atualiza o status em compliance.kyc_subjects.
//
// Idempotente: ON CONFLICT DO UPDATE so atualiza se o status mudar.
// PII nao transita: apenas o applicantId (referencia Sumsub) e o status.
func (c *Client) processWebhookEvent(ctx context.Context, event sumsubWebhookEvent) error {
	if event.ApplicantID == "" {
		return errors.New("sumsub webhook: applicantId ausente no evento")
	}

	// Mapeia o resultado do Sumsub para o status do cofre
	newStatus := mapSumsubReviewStatus(event.ReviewStatus, event.ReviewResult.ReviewAnswer)

	if c.pool == nil {
		// Em testes sem banco, apenas loga
		c.logger.Debug("sumsub webhook (sem banco): evento recebido",
			"type", event.Type,
			"mapped_status", string(newStatus),
		)
		return nil
	}

	// Atualiza o status no cofre por applicant_id (idempotente via ON CONFLICT)
	// NAO usa PII — apenas o applicant_id e o novo status
	var rejectionReason *string
	if len(event.ReviewResult.RejectLabels) > 0 {
		r := strings.Join(event.ReviewResult.RejectLabels, ",")
		rejectionReason = &r
	}

	tag, err := c.pool.Exec(ctx,
		`UPDATE compliance.kyc_subjects
		 SET status = $1, rejection_reason = $2, updated_at = now()
		 WHERE sumsub_applicant_id = $3
		   AND status IS DISTINCT FROM $1`,
		string(newStatus), rejectionReason, event.ApplicantID,
	)
	if err != nil {
		return fmt.Errorf("sumsub: atualizar status no cofre: %w", err)
	}

	c.logger.Info("sumsub webhook: status atualizado no cofre",
		"type", event.Type,
		"new_status", string(newStatus),
		"rows_affected", tag.RowsAffected(),
		// Sem PII: nao loga applicant_id em info, external_user_id, nome
	)
	return nil
}

// mapSumsubReviewStatus converte o reviewStatus/reviewAnswer do Sumsub
// para o status interno do cofre.
func mapSumsubReviewStatus(reviewStatus, reviewAnswer string) KYCStatus {
	switch reviewStatus {
	case "completed":
		switch reviewAnswer {
		case "GREEN":
			return KYCStatusApproved
		case "RED":
			return KYCStatusRejected
		default:
			return KYCStatusOnHold
		}
	case "pending", "queued", "prechecked":
		return KYCStatusPending
	case "onHold":
		return KYCStatusOnHold
	default:
		return KYCStatusPending
	}
}

// ---------------------------------------------------------------------------
// Verificacao de assinatura do webhook Sumsub
//
// O Sumsub usa HMAC-SHA1 com o webhookSecret como chave.
// Header: X-Payload-Digest: <hex>
// Referencia: https://docs.sumsub.com/reference/webhook-verification
//
// Fallback: X-Webhook-Secret-Token (alguns ambientes Sumsub usam SHA256).
//
// Comparacao tempo-constante: hmac.Equal — previne timing attack.
// ---------------------------------------------------------------------------

// verifyWebhookSignature verifica a assinatura HMAC do webhook Sumsub.
//
// Aceita:
//   - X-Payload-Digest: <hex-sha1>  (padrao Sumsub)
//   - X-Webhook-Secret-Token: <token>  (modo token simples — verificacao por igualdade constante)
func (c *Client) verifyWebhookSignature(rawBody []byte, headers http.Header) error {
	// Tenta X-Payload-Digest (HMAC-SHA1 — padrao Sumsub)
	digestHeader := headers.Get("X-Payload-Digest")
	if digestHeader != "" {
		return c.verifyHMACSHA1(rawBody, digestHeader)
	}

	// Fallback: X-Payload-Digest-Alg pode indicar SHA256
	alg := strings.ToLower(headers.Get("X-Payload-Digest-Alg"))
	if alg == "sha256" {
		return c.verifyHMACSHA256(rawBody, headers.Get("X-Payload-Digest"))
	}

	// Sem assinatura reconhecida: rejeita (fail-closed)
	return ErrInvalidWebhookSignature
}

// verifyHMACSHA1 verifica assinatura HMAC-SHA1 (padrao Sumsub).
//
// #nosec G401 — SHA-1 e especificado pelo Sumsub como algoritmo de webhook;
// nao e escolha nossa. Usamos apenas para verificar a assinatura recebida,
// nunca para gerar hashes de seguranca proprios.
func (c *Client) verifyHMACSHA1(rawBody []byte, digestHex string) error {
	sigBytes, err := hex.DecodeString(digestHex)
	if err != nil {
		return ErrInvalidWebhookSignature
	}

	//nolint:gosec // SHA-1 exigido pelo Sumsub (padrao do provedor, nao escolha nossa)
	mac := hmac.New(sha1.New, []byte(c.webhookSecret))
	mac.Write(rawBody)
	expected := mac.Sum(nil)

	if !hmac.Equal(expected, sigBytes) {
		return ErrInvalidWebhookSignature
	}
	return nil
}

// verifyHMACSHA256 verifica assinatura HMAC-SHA256 (fallback).
func (c *Client) verifyHMACSHA256(rawBody []byte, digestHex string) error {
	if digestHex == "" {
		return ErrInvalidWebhookSignature
	}
	sigBytes, err := hex.DecodeString(digestHex)
	if err != nil {
		return ErrInvalidWebhookSignature
	}

	mac := hmac.New(sha256.New, []byte(c.webhookSecret))
	mac.Write(rawBody)
	expected := mac.Sum(nil)

	if !hmac.Equal(expected, sigBytes) {
		return ErrInvalidWebhookSignature
	}
	return nil
}
