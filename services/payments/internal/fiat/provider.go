// Package fiat define a interface FiatProvider e os tipos compartilhados
// do trilho fiat (Stripe, Asaas/PIX, Mercado Pago).
//
// # Posicionamento arquitetural
//
// O trilho fiat vive INTEIRAMENTE FORA do hot path (ADR-0004 §E.0 / §C).
// Nenhum tipo deste pacote e importado por services/decision, internal/cascade
// nem internal/ranker.
//
// # SAQ-A / PCI
//
// O cartao de credito NUNCA transita pelo backend.
// A tokenizacao e client-side via Stripe Elements/Checkout. O backend recebe
// apenas PaymentIntent IDs e client_secret (para o front montar o formulario).
// Nenhum campo PAN, CVV, numero de cartao ou track data aparece em nenhum
// struct, log ou handler deste pacote (TX-3 / ADR-0004 §F).
//
// # Float proibido (TX-2)
//
// Todos os valores monetarios sao int64 (minor-units). BRL usa scale=2;
// USD usa scale=2. O scale SEMPRE vem do Asset Registry — nunca hardcoded
// no handler.
//
// # PII
//
// PII (nome, CPF, email do pagador) NUNCA entra neste pacote. Identificadores
// usados: tenant_id, advertiser_id (pseudonimos). Cofre de compliance = K6.
package fiat

import (
	"context"
	"time"
)

// PaymentIntentRequest parametriza a criacao de um PaymentIntent fiat.
//
// Campos PAN/CVV/cartao: ausentes por design (SAQ-A).
// O front usa o client_secret retornado para montar o formulario Stripe Elements
// ou a tela de PIX — o cartao nunca chega ao backend.
type PaymentIntentRequest struct {
	// TenantID e o ID do tenant (pseudonimo; sem PII — DA-11).
	TenantID string

	// AdvertiserID e o identificador do anunciante (pseudonimo).
	AdvertiserID string

	// AmountMinor e o valor em minor-units do ativo (int64, sem float — TX-2).
	// Para BRL: centavos. Para USD: cents.
	AmountMinor int64

	// AssetCode e o codigo do ativo ("BRL", "USD").
	// Scale DEVE ser obtido do Asset Registry, nao derivado do code.
	AssetCode string

	// Scale e o numero de casas decimais do ativo (Asset Registry).
	Scale int32

	// IdempotencyKey e a chave de idempotencia para a criacao do intent.
	// Formato recomendado: "pi:{advertiser_id}:{campaign_id}:{period}".
	IdempotencyKey string

	// Description e uma descricao livre (sem PII) para auditoria.
	Description string
}

// PaymentIntentResult e o resultado da criacao de um PaymentIntent.
//
// ClientSecret e o unico campo necessario para o front montar o
// formulario de pagamento. NUNCA logue o ClientSecret — ele permite
// completar o pagamento como o cliente.
type PaymentIntentResult struct {
	// PaymentID e o ID do intent no provedor (ex.: "pi_3P...").
	PaymentID string

	// ClientSecret e necessario pelo front para confirmar o pagamento.
	// Nao e um segredo de servidor, mas nao deve ser logado.
	// Vida util: expira apos o intent ser confirmado ou cancelado.
	ClientSecret string

	// Rail indica o trilho pelo qual o intent foi criado.
	Rail string
}

// PixChargeRequest parametriza a criacao de uma cobranca PIX.
type PixChargeRequest struct {
	// TenantID e o ID do tenant (pseudonimo; sem PII).
	TenantID string

	// AdvertiserID e o identificador do anunciante (pseudonimo).
	AdvertiserID string

	// AmountMinor e o valor em minor-units BRL (centavos, int64 — TX-2).
	AmountMinor int64

	// Scale e sempre 2 para BRL (Asset Registry). Passado explicitamente.
	Scale int32

	// IdempotencyKey para o provedor.
	IdempotencyKey string

	// Description sem PII.
	Description string

	// DueDate e opcional: prazo para pagamento PIX.
	DueDate *time.Time

	// Recorrente indica se e Pix Automatico (recorrencia — Asaas).
	Recorrente bool
}

// PixChargeResult e o retorno de uma cobranca PIX criada.
type PixChargeResult struct {
	// ChargeID e o ID da cobranca no provedor (ex.: ID Asaas).
	ChargeID string

	// QRCode e o payload do QR Code dinamico (copiavel ou escaneavel).
	// Nunca inclui dados do pagador (sem PII).
	QRCode string

	// QRCodeImageURL e a URL da imagem do QR Code (opcional).
	QRCodeImageURL string

	// PixKey e a chave PIX associada (pode ser txid ou chave aleatoria).
	PixKey string

	// Rail indica o trilho.
	Rail string
}

// WebhookEvent representa um evento normalizado recebido de qualquer provedor fiat.
//
// PII do pagador (nome, CPF, email) e descartada antes de popular esta struct.
// Apenas identificadores pseudonimos do provedor (ChargeID, PixEndToEndID).
type WebhookEvent struct {
	// ProviderEventID e o ID unico do evento no provedor (chave de idempotencia).
	ProviderEventID string

	// ChargeID e o ID da cobranca/intent associada.
	ChargeID string

	// Status normalizado: "captured", "pending", "failed", "reversed".
	Status string

	// AmountMinor e o valor confirmado em minor-units (int64 — TX-2).
	AmountMinor int64

	// AssetCode e o ativo do pagamento (ex.: "BRL", "USD").
	AssetCode string

	// Scale e as casas decimais do ativo (Asset Registry).
	Scale int32

	// Rail identifica o trilho de origem.
	Rail string

	// PixEndToEndID e o End-to-End ID do PIX (txid/E2E — §2.6).
	// Vazio para trilhos nao-PIX.
	PixEndToEndID string

	// OccurredAt e o timestamp do evento no provedor.
	OccurredAt time.Time
}

// FiatProvider e a interface unica para qualquer trilho fiat.
//
// Implementacoes: StripeProvider, AsaasProvider, MercadoPagoProvider.
// O roteador de failover (FailoverProvider) tambem implementa esta interface.
//
// INVARIANTES:
//   - Nenhuma implementacao recebe PAN/CVV/dados de cartao.
//   - Valores monetarios sempre int64 minor-units (sem float — TX-2).
//   - PII do pagador descartada antes de retornar qualquer tipo.
type FiatProvider interface {
	// CreatePaymentIntent cria um intent de pagamento fiat (Stripe) ou
	// cobranca cartao (Mercado Pago). Para PIX, use CreatePixCharge.
	// Retorna o client_secret para o front completar o pagamento.
	CreatePaymentIntent(ctx context.Context, req PaymentIntentRequest) (PaymentIntentResult, error)

	// CreatePixCharge cria uma cobranca PIX com QR Code dinamico.
	CreatePixCharge(ctx context.Context, req PixChargeRequest) (PixChargeResult, error)

	// RailName retorna o nome canonico do trilho ("stripe", "asaas_pix", "mercadopago").
	RailName() string

	// Healthy retorna nil se o provedor esta disponivel.
	// Usado pelo failover para checar se o primario esta operacional.
	Healthy(ctx context.Context) error
}
