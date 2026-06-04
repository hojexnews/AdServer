// Package ledger — crypto.go
//
// Operacoes de alto nivel para o trilho cripto:
//   - RecordDeposit:    deposito on-chain como entry PENDING
//   - FinalizeDeposit:  PENDING -> posted (finalidade atingida)
//   - RecordReversal:   estorno auditavel por reorg (novo par de postings)
//   - RecordPayout:     payout da plataforma para publisher/anunciante
//   - RecordFXExchange: cambio explicito entre dois ativos (DA-10)
//
// TenantID e tratado como string (UUID em texto) — mesma convencao do projeto.
package ledger

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DepositParams parametriza o registro de um deposito cripto como PENDING.
// Idempotency key: "deposit:{tx_hash}".
type DepositParams struct {
	TenantID              string    // UUID em texto
	TxHash                string    // hash da transacao on-chain
	AdvertiserID          string    // pseudonimo (nunca PII — DA-11)
	AssetCode             string
	Scale                 int32
	AmountMinor           int64     // minor-units (int64, sem float — TX-2)
	EffectiveAt           time.Time
	PlatformCashAccountID int64     // platform:cash:{asset} — recebe (debit)
	AdvAccountID          int64     // adv:{id}:{asset} — saldo creditado (credit)
}

// RecordDeposit registra um deposito cripto como journal_entry PENDING.
//
// O deposito fica PENDING ate atingir finalidade (N confirmacoes via webhook).
// Transicao para 'posted' ocorre via FinalizeDeposit ao receber DepositFinalizedEvent.
// Idempotente: reprocessar o mesmo tx_hash nao duplica.
func RecordDeposit(ctx context.Context, pool *pgxpool.Pool, loader AssetLoader, p DepositParams) (JournalEntryID, error) {
	idempKey := fmt.Sprintf("deposit:%s", p.TxHash)

	postings := []PostingLine{
		{
			AccountID:        p.PlatformCashAccountID,
			AssetCode:        p.AssetCode,
			Scale:            p.Scale,
			DebitMinorUnits:  p.AmountMinor,
			CreditMinorUnits: 0,
		},
		{
			AccountID:        p.AdvAccountID,
			AssetCode:        p.AssetCode,
			Scale:            p.Scale,
			DebitMinorUnits:  0,
			CreditMinorUnits: p.AmountMinor,
		},
	}

	id, err := RecordEntry(ctx, pool, loader, RecordEntryParams{
		TenantID:       p.TenantID,
		IdempotencyKey: idempKey,
		Description:    fmt.Sprintf("Deposito cripto PENDING: %s %s tx=%s", p.AssetCode, int64ToNumericStr(p.AmountMinor), p.TxHash),
		Status:         EntryStatusPending,
		EffectiveAt:    p.EffectiveAt,
		RefType:        "tx_hash",
		Metadata:       []byte(fmt.Sprintf(`{"tx_hash":%q,"advertiser_id":%q}`, p.TxHash, p.AdvertiserID)),
		Postings:       postings,
	})
	if errors.Is(err, ErrIdempotentDuplicate) {
		return id, nil
	}
	return id, err
}

// FinalizeDepositParams parametriza a transicao PENDING -> posted.
type FinalizeDepositParams struct {
	TenantID      string
	TxHash        string
	Confirmations uint32
}

// FinalizeDeposit transiciona a entry de deposito de 'pending' para 'posted'.
//
// Chamada ao receber DepositFinalizedEvent. Idempotente.
func FinalizeDeposit(ctx context.Context, pool *pgxpool.Pool, p FinalizeDepositParams) error {
	return FinalizeEntry(ctx, pool, FinalizeEntryParams{
		TenantID:       p.TenantID,
		IdempotencyKey: fmt.Sprintf("deposit:%s", p.TxHash),
	})
}

// ReversalParams parametriza o estorno auditavel de um deposito.
//
// Reorg on-chain NUNCA edita o posting original. Gera novo par invertido.
// Idempotency key: "void:deposit:{original_tx_hash}".
type ReversalParams struct {
	TenantID              string
	OriginalTxHash        string
	AssetCode             string
	Scale                 int32
	AmountMinor           int64
	EffectiveAt           time.Time
	Reason                string // ex.: "chain_reorg", "chargeback", "manual_adjustment"
	PlatformCashAccountID int64
	AdvAccountID          int64
}

// RecordReversal registra um estorno auditavel como novo par de postings.
//
// O posting original permanece imutavel para auditoria (invariante §2.6).
func RecordReversal(ctx context.Context, pool *pgxpool.Pool, loader AssetLoader, p ReversalParams) (JournalEntryID, error) {
	voidKey := fmt.Sprintf("void:deposit:%s", p.OriginalTxHash)

	// Postings invertidos (BILLING.md §6).
	postings := []PostingLine{
		{
			AccountID:        p.PlatformCashAccountID,
			AssetCode:        p.AssetCode,
			Scale:            p.Scale,
			DebitMinorUnits:  0,
			CreditMinorUnits: p.AmountMinor, // inverte: credito onde era debito
		},
		{
			AccountID:        p.AdvAccountID,
			AssetCode:        p.AssetCode,
			Scale:            p.Scale,
			DebitMinorUnits:  p.AmountMinor, // inverte: debito onde era credito
			CreditMinorUnits: 0,
		},
	}

	id, err := RecordEntry(ctx, pool, loader, RecordEntryParams{
		TenantID:       p.TenantID,
		IdempotencyKey: voidKey,
		Description:    fmt.Sprintf("Estorno auditavel: %s tx=%s razao=%s", p.AssetCode, p.OriginalTxHash, p.Reason),
		Status:         EntryStatusPosted,
		EffectiveAt:    p.EffectiveAt,
		RefType:        "tx_hash",
		Metadata:       []byte(fmt.Sprintf(`{"original_tx":%q,"reason":%q}`, p.OriginalTxHash, p.Reason)),
		Postings:       postings,
	})
	if errors.Is(err, ErrIdempotentDuplicate) {
		return id, nil
	}
	return id, err
}

// PayoutParams parametriza o payout da plataforma para publisher/anunciante.
// Idempotency key: "payout:{payment_event_id}".
type PayoutParams struct {
	TenantID              string
	PaymentEventID        string
	AssetCode             string
	Scale                 int32
	AmountMinor           int64
	EffectiveAt           time.Time
	AdvAccountID          int64
	PlatformCashAccountID int64
}

// RecordPayout registra um payout como par de postings posted.
func RecordPayout(ctx context.Context, pool *pgxpool.Pool, loader AssetLoader, p PayoutParams) (JournalEntryID, error) {
	idempKey := fmt.Sprintf("payout:%s", p.PaymentEventID)

	postings := []PostingLine{
		{
			AccountID:        p.AdvAccountID,
			AssetCode:        p.AssetCode,
			Scale:            p.Scale,
			DebitMinorUnits:  p.AmountMinor,
			CreditMinorUnits: 0,
		},
		{
			AccountID:        p.PlatformCashAccountID,
			AssetCode:        p.AssetCode,
			Scale:            p.Scale,
			DebitMinorUnits:  0,
			CreditMinorUnits: p.AmountMinor,
		},
	}

	id, err := RecordEntry(ctx, pool, loader, RecordEntryParams{
		TenantID:       p.TenantID,
		IdempotencyKey: idempKey,
		Description:    fmt.Sprintf("Payout cripto: %s %s evt=%s", p.AssetCode, int64ToNumericStr(p.AmountMinor), p.PaymentEventID),
		Status:         EntryStatusPosted,
		EffectiveAt:    p.EffectiveAt,
		RefType:        "payment_event",
		Metadata:       []byte(fmt.Sprintf(`{"payment_event_id":%q}`, p.PaymentEventID)),
		Postings:       postings,
	})
	if errors.Is(err, ErrIdempotentDuplicate) {
		return id, nil
	}
	return id, err
}

// FXExchangeParams parametriza um cambio explicito entre dois ativos.
//
// DA-10: cambio NUNCA e implicito/automatico. Exige taxa registrada por humano/desk.
// Dois pares isolados por ativo dentro da mesma entry (BILLING.md §5).
type FXExchangeParams struct {
	TenantID         string
	ReferenceID      string
	AssetFrom        string
	ScaleFrom        int32
	AmountFromMinor  int64
	AssetTo          string
	ScaleTo          int32
	AmountToMinor    int64
	EffectiveAt      time.Time
	RateApplied      string // string decimal, sem float: "5.123456"
	Approver         string // papel/identidade do aprovador (sem PII)
	RateSource       string // "manual", "desk-rate", etc.
	// Par "from": debito + credito no mesmo ativo (balance zero).
	CashFromAccountID int64
	CashFromCounterID int64
	// Par "to": credito + debito no mesmo ativo (balance zero).
	CashToAccountID   int64
	CashToCounterID   int64
}

// RecordFXExchange registra um cambio explicito como dois pares isolados.
//
// Idempotency key: "fx:{reference_id}:{asset_from}-{asset_to}".
func RecordFXExchange(ctx context.Context, pool *pgxpool.Pool, loader AssetLoader, p FXExchangeParams) (JournalEntryID, error) {
	pair := fmt.Sprintf("%s-%s", p.AssetFrom, p.AssetTo)
	idempKey := fmt.Sprintf("fx:%s:%s", p.ReferenceID, pair)

	postings := []PostingLine{
		// Par AssetFrom — debito (saida)
		{AccountID: p.CashFromAccountID, AssetCode: p.AssetFrom, Scale: p.ScaleFrom, DebitMinorUnits: p.AmountFromMinor, CreditMinorUnits: 0},
		// Par AssetFrom — credito (contra-conta equilibra)
		{AccountID: p.CashFromCounterID, AssetCode: p.AssetFrom, Scale: p.ScaleFrom, DebitMinorUnits: 0, CreditMinorUnits: p.AmountFromMinor},
		// Par AssetTo — credito (entrada)
		{AccountID: p.CashToAccountID, AssetCode: p.AssetTo, Scale: p.ScaleTo, DebitMinorUnits: 0, CreditMinorUnits: p.AmountToMinor},
		// Par AssetTo — debito (contra-conta equilibra)
		{AccountID: p.CashToCounterID, AssetCode: p.AssetTo, Scale: p.ScaleTo, DebitMinorUnits: p.AmountToMinor, CreditMinorUnits: 0},
	}

	metadata := fmt.Sprintf(
		`{"rate_applied":%q,"approver":%q,"rate_source":%q,"pair":%q}`,
		p.RateApplied, p.Approver, p.RateSource, pair,
	)

	id, err := RecordEntry(ctx, pool, loader, RecordEntryParams{
		TenantID:       p.TenantID,
		IdempotencyKey: idempKey,
		Description:    fmt.Sprintf("Cambio explicito %s -> %s @ %s aprovado por %s", p.AssetFrom, p.AssetTo, p.RateApplied, p.Approver),
		Status:         EntryStatusPosted,
		EffectiveAt:    p.EffectiveAt,
		RefType:        "fx_exchange",
		Metadata:       []byte(metadata),
		Postings:       postings,
	})
	if errors.Is(err, ErrIdempotentDuplicate) {
		return id, nil
	}
	return id, err
}
