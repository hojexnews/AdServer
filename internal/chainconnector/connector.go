// Package chainconnector define a interface ChainConnector — UNICO ponto de
// contato com qualquer chain on-chain no AdServer (§2.6 / ADR-0004 §C).
//
// # Fronteira plugavel
//
// Todo acesso a chain (Safe multisig, Fireblocks MPC, chain propria nao-EVM)
// passa por esta interface. Trocar Safe -> Fireblocks, ou EVM -> nao-EVM, e
// trocar a implementacao — nunca reescrever o trilho de pagamento.
//
// # Invariantes inegociaveis
//
//   - Deposito cripto permanece PENDING ate finalidade (N confirmacoes).
//     O conector nao credita saldo — isso e responsabilidade do ledger (K3).
//   - Webhook do custodiante e preferido a logica de reorg propria (E.8).
//   - Nenhum metodo deste pacote e chamado no hot path de decisao.
//     ChainConnector vive exclusivamente em services/payments.
//   - Float PROIBIDO: valores monetarios trafegam como int64 minor-units
//     (Money.Amount) + scale do Asset Registry (TX-2 / DA-10).
//   - PII/KYC nunca entra neste pacote: enderecos on-chain nao sao PII
//     de anunciante — a associacao anunciante <-> endereco vive no cofre
//     de compliance referenciado por tenant_id (TX-3 / DA-11).
//
// # Implementacoes previstas
//
//   - EVMStub      — implementacao deterministica para testes (este pacote).
//   - EVMSafe      — Safe multisig via viem/go-ethereum (K5, prod).
//   - EVMFireblocks — Fireblocks MPC (upgrade sob AUM, gatilho C ADR-0004).
//   - NativeChain  — chain propria nao-EVM (unico caso que justifica
//     signer/indexer/confirmacoes proprios — E.1 ADR-0004).
package chainconnector

import (
	"context"
	"errors"
	"time"

	moneyv1 "github.com/hojex/adserver/gen/go/adserver/money/v1"
)

// ---------------------------------------------------------------------------
// Erros padrao
// ---------------------------------------------------------------------------

// ErrNotFound e retornado quando um deposito ou transacao nao e encontrado.
var ErrNotFound = errors.New("chainconnector: recurso nao encontrado")

// ErrFinalityNotReached e retornado quando uma transacao ainda nao atingiu
// o numero minimo de confirmacoes exigido pela chain.
var ErrFinalityNotReached = errors.New("chainconnector: finalidade nao atingida")

// ErrUnsupportedChain e retornado quando a chain_id nao e suportada
// pela implementacao concreta.
var ErrUnsupportedChain = errors.New("chainconnector: chain nao suportada")

// ---------------------------------------------------------------------------
// Tipos de dominio
// ---------------------------------------------------------------------------

// DepositEvent representa um deposito detectado on-chain.
//
// O deposito chega com status PENDING; o conector NUNCA credita saldo.
// A transicao PENDING -> definitivo cabe ao ledger (K3) apos o evento
// DepositFinalized.
type DepositEvent struct {
	// TxHash e o hash da transacao on-chain (identificador unico).
	TxHash string

	// ChainID e o EVM chain id (ex.: 1 = Ethereum, 137 = Polygon).
	// Zero para chain nao-EVM (implementacao NativeChain define seu proprio
	// esquema de identificacao).
	ChainID int64

	// BlockNumber e o bloco em que a transacao foi incluida.
	// Zero se ainda estiver no mempool.
	BlockNumber uint64

	// Confirmations e o numero de confirmacoes no momento da deteccao.
	// Pode ser zero (mempool/recente). Finalidade exige >= MinConfirmations
	// configurado por chain.
	Confirmations uint32

	// Amount e o valor do deposito em Money canonico (TX-2).
	// Float PROIBIDO: Amount.Amount e int64 minor-units.
	// Amount.Scale DEVE coincidir com o scale do Asset Registry para o
	// asset_code informado (DA-10). Scale = 0 e invalido para a maioria
	// dos ativos — valide antes de usar.
	Amount *moneyv1.Money

	// ToAddress e o endereco on-chain de destino (carteira do custodiante).
	// Nunca um endereco de anunciante com PII — a associacao vive no cofre
	// de compliance (TX-3).
	ToAddress string

	// DetectedAt e o instante em que o conector detectou o deposito.
	DetectedAt time.Time
}

// DepositFinalized representa a confirmacao de finalidade de um deposito.
//
// Emitido pelo conector quando a transacao atinge >= MinConfirmations.
// Preferir webhook do custodiante a polling ativo (E.8 ADR-0004).
type DepositFinalized struct {
	// TxHash identifica a transacao finalizada.
	TxHash string

	// ChainID e o EVM chain id.
	ChainID int64

	// Confirmations e o numero de confirmacoes no momento da finalizacao.
	Confirmations uint32

	// Amount e o valor final do deposito (Money canonico, TX-2).
	Amount *moneyv1.Money

	// FinalizedBlock e o numero do bloco de finalizacao.
	FinalizedBlock uint64

	// FinalizedAt e o instante da finalizacao (timestamp do bloco).
	FinalizedAt time.Time
}

// PayoutRequest encapsula os dados necessarios para construir uma transacao
// de saida (payout da plataforma para publisher/anunciante).
//
// Apenas buildPayout — a assinatura e o envio sao responsabilidade do
// custodiante (Safe/Fireblocks), nunca do trilho de pagamento diretamente.
// O metodo BuildPayout retorna uma transacao nao-assinada (tx bruta) que
// sera submetida ao custodiante via chamada separada.
type PayoutRequest struct {
	// ToAddress e o endereco on-chain de destino.
	ToAddress string

	// Amount e o valor do payout em Money canonico (TX-2).
	// Float PROIBIDO.
	Amount *moneyv1.Money

	// ChainID identifica a chain de destino.
	ChainID int64

	// IdempotencyKey e a chave de idempotencia do ledger (event_id do
	// PaymentEvent). Custodiantes que suportam idempotencia usam este campo
	// para evitar duplo-envio.
	IdempotencyKey string
}

// UnsignedTx e o payload de uma transacao nao-assinada retornado por
// BuildPayout. A assinatura cabe ao custodiante (Safe/Fireblocks).
//
// O formato e opaco ao trilho de pagamento: apenas o conector concreto
// e o custodiante entendem os bytes.
type UnsignedTx struct {
	// ChainID da transacao.
	ChainID int64

	// Payload e a transacao serializada (ex.: RLP para EVM, ou formato
	// nativo para chain nao-EVM).
	Payload []byte

	// EstimatedFee e a taxa estimada em minor-units do ativo nativo da chain
	// (ex.: wei para EVM). Informativo — nao e garantido.
	// Float PROIBIDO: minor-units inteiros.
	EstimatedFeeMinorUnits int64
}

// ---------------------------------------------------------------------------
// Interface principal
// ---------------------------------------------------------------------------

// ChainConnector e a interface UNICA de acesso a qualquer chain on-chain.
//
// Toda implementacao (Safe, Fireblocks, NativeChain) DEVE satisfazer esta
// interface. O trilho de pagamento (services/payments) depende apenas desta
// interface — nunca de uma implementacao concreta.
//
// Os metodos sao nomeados exatamente conforme o stack tecnologico §2.6:
// watchDeposits, getBalance, buildPayout, confirmations.
//
// Nenhum metodo deve ser chamado no hot path de decisao. O conector vive
// exclusivamente na goroutine de processamento de pagamentos (E.0 ADR-0004).
type ChainConnector interface {
	// WatchDeposits retorna um canal de DepositEvent para a carteira indicada.
	//
	// O canal e fechado quando ctx for cancelado. Erros de conexao sao
	// reportados por meio de DepositEvent com Amount nil (sentinel de erro
	// — o consumidor deve verificar).
	//
	// Implementacoes baseadas em webhook preferem receber o push do
	// custodiante e reemitir no canal, ao inves de pollar a chain.
	//
	// O canal NUNCA credita saldo — apenas detecta e notifica. O saldo
	// permanece PENDING ate DepositFinalized (E.8 ADR-0004).
	WatchDeposits(ctx context.Context, address string, chainID int64) (<-chan DepositEvent, error)

	// GetBalance retorna o saldo de um ativo em uma carteira, em Money
	// canonico (TX-2). Float PROIBIDO.
	//
	// Usado apenas fora do hot path (reconciliacao, dashboard de custodia).
	// Nunca chamado pelo motor de decisao nem pelo pacing (DA-4 usa budget
	// pre-computado, nao saldo on-chain).
	//
	// Retorna ErrUnsupportedChain se a chain nao for suportada.
	GetBalance(ctx context.Context, address string, chainID int64, assetCode string) (*moneyv1.Money, error)

	// BuildPayout constroi uma transacao de saida nao-assinada a partir de
	// PayoutRequest. Nao envia nem assina — apenas serializa.
	//
	// A submissao ao custodiante (Safe/Fireblocks) e responsabilidade do
	// caller (services/payments), que usa as chaves armazenadas no OpenBao
	// (nunca em imagem/git — §2.7).
	//
	// Retorna ErrUnsupportedChain se chainID nao for suportado.
	BuildPayout(ctx context.Context, req PayoutRequest) (UnsignedTx, error)

	// Confirmations retorna o numero de confirmacoes de uma transacao
	// identificada por txHash na chain indicada.
	//
	// Retorna ErrNotFound se a transacao nao for conhecida.
	// Retorna ErrFinalityNotReached se a transacao nao atingiu finalidade
	// (util para polling de fallback quando o webhook do custodiante falha).
	Confirmations(ctx context.Context, txHash string, chainID int64) (uint32, error)
}
