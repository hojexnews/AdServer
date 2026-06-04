// Testes de unidade do EVMStub (ChainConnector K0).
//
// Verificam que o stub satisfaz a interface ChainConnector e que as
// invariantes criticas (no-float, no-implicit-fx, pending-ate-finalidade)
// sao aplicadas corretamente.
package chainconnector_test

import (
	"context"
	"testing"
	"time"

	"github.com/hojex/adserver/internal/chainconnector"
	moneyv1 "github.com/hojex/adserver/gen/go/adserver/money/v1"
)

// ---------------------------------------------------------------------------
// helpers de teste
// ---------------------------------------------------------------------------

func usdcMoney(amount int64) *moneyv1.Money {
	// USDC: scale=6 (Asset Registry). Nunca float.
	return &moneyv1.Money{AssetCode: "USDC", Amount: amount, Scale: 6}
}

func newStub() *chainconnector.EVMStub {
	return chainconnector.NewEVMStub(chainconnector.EVMStubConfig{
		Balances: map[string]*moneyv1.Money{
			// chave "address:assetCode"
			"0xAABB:USDC": usdcMoney(5_000_000), // 5.000000 USDC
		},
		TxConfirmations: map[string]uint32{
			"0xdeadbeef": 12,
			"0xcafe0001": 2,
		},
		MinConfirmations:     6,
		SupportedChainIDs:    []int64{1, 137},
		DepositChannelBuffer: 4,
	})
}

// ---------------------------------------------------------------------------
// Verifica que EVMStub satisfaz a interface em tempo de compilacao.
// ---------------------------------------------------------------------------

var _ chainconnector.ChainConnector = (*chainconnector.EVMStub)(nil)

// ---------------------------------------------------------------------------
// GetBalance
// ---------------------------------------------------------------------------

func TestGetBalance_Exists(t *testing.T) {
	t.Parallel()
	s := newStub()
	got, err := s.GetBalance(context.Background(), "0xAABB", 1, "USDC")
	if err != nil {
		t.Fatalf("GetBalance inesperado: %v", err)
	}
	if got.GetAmount() != 5_000_000 {
		t.Errorf("Amount: quero 5000000, got %d", got.GetAmount())
	}
	if got.GetScale() != 6 {
		t.Errorf("Scale: quero 6, got %d", got.GetScale())
	}
	// Verificacao anti-float: Scale nao pode ser zero para USDC.
	if got.GetScale() == 0 {
		t.Error("Scale == 0: invalido para USDC (TX-2 / DA-10)")
	}
}

func TestGetBalance_NotFound(t *testing.T) {
	t.Parallel()
	s := newStub()
	_, err := s.GetBalance(context.Background(), "0xDEAD", 1, "USDC")
	if err == nil {
		t.Fatal("esperava ErrNotFound")
	}
}

func TestGetBalance_UnsupportedChain(t *testing.T) {
	t.Parallel()
	s := newStub()
	_, err := s.GetBalance(context.Background(), "0xAABB", 999, "USDC")
	if err == nil {
		t.Fatal("esperava ErrUnsupportedChain")
	}
}

func TestSetBalance_ReplacesMidTest(t *testing.T) {
	t.Parallel()
	s := newStub()
	s.SetBalance("0xAABB", &moneyv1.Money{AssetCode: "USDC", Amount: 9_999_999, Scale: 6})
	got, err := s.GetBalance(context.Background(), "0xAABB", 1, "USDC")
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if got.GetAmount() != 9_999_999 {
		t.Errorf("quero 9999999, got %d", got.GetAmount())
	}
}

// ---------------------------------------------------------------------------
// Confirmations
// ---------------------------------------------------------------------------

func TestConfirmations_Finalized(t *testing.T) {
	t.Parallel()
	s := newStub()
	confs, err := s.Confirmations(context.Background(), "0xdeadbeef", 1)
	if err != nil {
		t.Fatalf("Confirmations: %v", err)
	}
	if confs != 12 {
		t.Errorf("quero 12 confs, got %d", confs)
	}
}

func TestConfirmations_FinalityNotReached(t *testing.T) {
	t.Parallel()
	s := newStub()
	// 0xcafe0001 tem 2 confirmacoes; minimo e 6 => ErrFinalityNotReached.
	confs, err := s.Confirmations(context.Background(), "0xcafe0001", 1)
	if err == nil {
		t.Fatal("esperava ErrFinalityNotReached")
	}
	// Mesmo com erro, o numero de confirmacoes e retornado (informativo).
	if confs != 2 {
		t.Errorf("quero 2 confs mesmo com erro, got %d", confs)
	}
}

func TestConfirmations_NotFound(t *testing.T) {
	t.Parallel()
	s := newStub()
	_, err := s.Confirmations(context.Background(), "0xunknown", 1)
	if err == nil {
		t.Fatal("esperava ErrNotFound")
	}
}

func TestSetConfirmations_Progression(t *testing.T) {
	t.Parallel()
	s := newStub()
	// Simula progressao de 2 -> 6 (atingiu finalidade).
	s.SetConfirmations("0xcafe0001", 6)
	confs, err := s.Confirmations(context.Background(), "0xcafe0001", 1)
	if err != nil {
		t.Fatalf("esperava finalidade: %v", err)
	}
	if confs != 6 {
		t.Errorf("quero 6, got %d", confs)
	}
}

// ---------------------------------------------------------------------------
// BuildPayout
// ---------------------------------------------------------------------------

func TestBuildPayout_Valid(t *testing.T) {
	t.Parallel()
	s := newStub()
	req := chainconnector.PayoutRequest{
		ToAddress:      "0xPublisherWallet",
		Amount:         usdcMoney(1_000_000), // 1.000000 USDC
		ChainID:        1,
		IdempotencyKey: "evt-123",
		TenantID:       "aaaaaaaa-0000-0000-0000-000000000001", // TX-3: pseudonimo do contexto autenticado
	}
	tx, err := s.BuildPayout(context.Background(), req)
	if err != nil {
		t.Fatalf("BuildPayout: %v", err)
	}
	if tx.ChainID != 1 {
		t.Errorf("ChainID: quero 1, got %d", tx.ChainID)
	}
	if len(tx.Payload) == 0 {
		t.Error("Payload nao deve ser vazio")
	}
	// Fee deterministica do stub (21000 gas).
	if tx.EstimatedFeeMinorUnits != 21_000 {
		t.Errorf("Fee: quero 21000, got %d", tx.EstimatedFeeMinorUnits)
	}
}

func TestBuildPayout_ZeroAmount(t *testing.T) {
	t.Parallel()
	s := newStub()
	req := chainconnector.PayoutRequest{
		ToAddress: "0xAnyone",
		Amount:    &moneyv1.Money{AssetCode: "USDC", Amount: 0, Scale: 6},
		ChainID:   1,
		TenantID:  "aaaaaaaa-0000-0000-0000-000000000001",
	}
	_, err := s.BuildPayout(context.Background(), req)
	if err == nil {
		t.Fatal("esperava erro para Amount=0 (invariante TX-2)")
	}
}

func TestBuildPayout_NegativeAmount(t *testing.T) {
	t.Parallel()
	s := newStub()
	req := chainconnector.PayoutRequest{
		ToAddress: "0xAnyone",
		Amount:    &moneyv1.Money{AssetCode: "USDC", Amount: -1_000_000, Scale: 6},
		ChainID:   1,
		TenantID:  "aaaaaaaa-0000-0000-0000-000000000001",
	}
	_, err := s.BuildPayout(context.Background(), req)
	if err == nil {
		t.Fatal("esperava erro para Amount negativo (invariante TX-2)")
	}
}

func TestBuildPayout_NilAmount(t *testing.T) {
	t.Parallel()
	s := newStub()
	req := chainconnector.PayoutRequest{
		ToAddress: "0xAnyone",
		Amount:    nil,
		ChainID:   1,
		TenantID:  "aaaaaaaa-0000-0000-0000-000000000001",
	}
	_, err := s.BuildPayout(context.Background(), req)
	if err == nil {
		t.Fatal("esperava erro para Amount nil")
	}
}

func TestBuildPayout_UnsupportedChain(t *testing.T) {
	t.Parallel()
	s := newStub()
	req := chainconnector.PayoutRequest{
		ToAddress: "0xAnyone",
		Amount:    usdcMoney(100),
		ChainID:   42161, // Arbitrum — nao suportado pelo stub desta config
		TenantID:  "aaaaaaaa-0000-0000-0000-000000000001",
	}
	_, err := s.BuildPayout(context.Background(), req)
	if err == nil {
		t.Fatal("esperava ErrUnsupportedChain")
	}
}

// ---------------------------------------------------------------------------
// WatchDeposits + InjectDeposit
// ---------------------------------------------------------------------------

func TestWatchDeposits_ReceivesInjected(t *testing.T) {
	t.Parallel()
	s := newStub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := s.WatchDeposits(ctx, "0xWatchAddr", 1)
	if err != nil {
		t.Fatalf("WatchDeposits: %v", err)
	}

	ev := chainconnector.DepositEvent{
		TxHash:        "0xabcdef01",
		ChainID:       1,
		BlockNumber:   1000,
		Confirmations: 0, // recente: ainda PENDING
		Amount:        usdcMoney(500_000),
		ToAddress:     "0xWatchAddr",
		DetectedAt:    time.Now(),
	}
	sent := s.InjectDeposit("0xWatchAddr", ev)
	if sent != 1 {
		t.Errorf("quero 1 watcher notificado, got %d", sent)
	}

	select {
	case received := <-ch:
		if received.TxHash != "0xabcdef01" {
			t.Errorf("TxHash: quero 0xabcdef01, got %s", received.TxHash)
		}
		if received.Amount.GetAmount() != 500_000 {
			t.Errorf("Amount: quero 500000, got %d", received.Amount.GetAmount())
		}
		// Confirmations=0: deposito ainda PENDING (invariante §C ADR-0004).
		if received.Confirmations != 0 {
			t.Errorf("Confirmations: quero 0 (PENDING), got %d", received.Confirmations)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timeout aguardando DepositEvent")
	}
}

func TestWatchDeposits_ChannelClosedOnCtxCancel(t *testing.T) {
	t.Parallel()
	s := newStub()
	ctx, cancel := context.WithCancel(context.Background())

	ch, err := s.WatchDeposits(ctx, "0xCloseTest", 1)
	if err != nil {
		t.Fatalf("WatchDeposits: %v", err)
	}

	cancel()

	// Apos cancelamento, o canal deve ser fechado (receive retorna zero-value + false).
	select {
	case _, open := <-ch:
		if open {
			t.Error("canal deveria estar fechado apos cancel()")
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("timeout aguardando fechamento do canal")
	}
}

func TestWatchDeposits_UnsupportedChain(t *testing.T) {
	t.Parallel()
	s := newStub()
	_, err := s.WatchDeposits(context.Background(), "0xAny", 56) // BSC — nao suportado
	if err == nil {
		t.Fatal("esperava ErrUnsupportedChain")
	}
}

// ---------------------------------------------------------------------------
// Invariante: sem float em nenhum valor monetario (TX-2)
// ---------------------------------------------------------------------------
// Teste documental: verifica que o compilador rejeitaria float em Money.
// Como o tipo Money usa int64/uint32, qualquer atribuicao de float
// seria erro de compilacao — este teste garante que os valores de teste
// usam apenas inteiros.
func TestNoFloat_MoneyUsesIntegerTypes(t *testing.T) {
	t.Parallel()
	m := usdcMoney(1_234_567)
	// Verifica em runtime que o valor nao foi truncado (indicativo de float).
	if m.GetAmount() != 1_234_567 {
		t.Errorf("Amount deve ser exato (int64): quero 1234567, got %d", m.GetAmount())
	}
	if m.GetScale() != 6 {
		t.Errorf("Scale deve ser 6 (USDC), got %d", m.GetScale())
	}
}

// ---------------------------------------------------------------------------
// Invariante: cross-chain nao mistura chain ids (DA-10 analogia)
// ---------------------------------------------------------------------------

func TestBuildPayout_DifferentChainIDs_AreIsolated(t *testing.T) {
	t.Parallel()
	s := chainconnector.NewEVMStub(chainconnector.EVMStubConfig{
		SupportedChainIDs: []int64{1, 137},
	})
	// Chain 1 (Ethereum mainnet)
	tx1, err := s.BuildPayout(context.Background(), chainconnector.PayoutRequest{
		ToAddress: "0xA",
		Amount:    usdcMoney(100),
		ChainID:   1,
		TenantID:  "aaaaaaaa-0000-0000-0000-000000000001",
	})
	if err != nil {
		t.Fatalf("chain 1: %v", err)
	}
	// Chain 137 (Polygon)
	tx137, err := s.BuildPayout(context.Background(), chainconnector.PayoutRequest{
		ToAddress: "0xA",
		Amount:    usdcMoney(100),
		ChainID:   137,
		TenantID:  "aaaaaaaa-0000-0000-0000-000000000001",
	})
	if err != nil {
		t.Fatalf("chain 137: %v", err)
	}
	// Payloads devem diferir (chain_id esta embutido no payload).
	if string(tx1.Payload) == string(tx137.Payload) {
		t.Error("Payload nao deve ser identico para chains diferentes")
	}
}
