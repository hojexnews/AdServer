// Package chainconnector — evm_safe_test.go
//
// Testes do EVMSafe sem rede real.
// Usa httptest.Server para mockar o endpoint JSON-RPC.
//
// Casos cobertos:
//   - GetBalance: valor normal, overflow uint256->int64 (deve ERRAR, nao truncar)
//   - Confirmations: finalidade atingida, nao atingida, tx desconhecida
//   - BuildPayout: calldata correto, amount negativo/zero rejeitado
//   - InjectDeposit: canal recebe evento
package chainconnector

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	moneyv1 "github.com/hojex/adserver/gen/go/adserver/money/v1"
)

// ---------------------------------------------------------------------------
// Mock de EVMRPCClient baseado em httptest.Server
// ---------------------------------------------------------------------------

// mockRPCHandler retorna uma func que trata requisicoes JSON-RPC de teste.
// O mapa responses mapeia method -> resultado a retornar (json.RawMessage).
// A funcao e chamada em ordem de chegada; cada entrada e consumida uma vez
// (para simular sequencias de chamadas distintas).
type rpcCall struct {
	method string
	result json.RawMessage
	errMsg string // se nao vazio, retorna erro RPC
}

type mockRPCServer struct {
	mu    sync.Mutex
	calls []rpcCall
	idx   int
}

func newMockRPCServer(calls []rpcCall) *mockRPCServer {
	return &mockRPCServer{calls: calls}
}

func (m *mockRPCServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		m.mu.Lock()
		if m.idx >= len(m.calls) {
			m.mu.Unlock()
			// Retorna null (ErrRPCNotFound) se esgotou as respostas
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":null}`, req.ID)
			return
		}
		call := m.calls[m.idx]
		m.idx++
		m.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if call.errMsg != "" {
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"error":{"code":-32000,"message":%q}}`,
				req.ID, call.errMsg)
			return
		}
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":%s}`, req.ID, string(call.result))
	}
}

// buildTestEVMSafe cria um EVMSafe conectado ao servidor de teste.
func buildTestEVMSafe(t *testing.T, serverURL string, minConfs uint32) *EVMSafe {
	t.Helper()
	cfg := EVMSafeConfig{
		Chains: map[int64]EVMSafeChainConfig{
			1: {
				ChainID:          1,
				RPCURL:           serverURL,
				ContractAddress:  "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48",
				AssetCode:        "USDC",
				AssetScale:       6,
				MinConfirmations: minConfs,
				SafeAddress:      "0xSafeMultisigPlatform",
			},
		},
	}
	safe, err := NewEVMSafe(cfg)
	if err != nil {
		t.Fatalf("NewEVMSafe: %v", err)
	}
	return safe
}

// ---------------------------------------------------------------------------
// GetBalance
// ---------------------------------------------------------------------------

func TestEVMSafe_GetBalance_Normal(t *testing.T) {
	// USDC balance: 1_500_000 minor-units (= 1.5 USDC com scale=6)
	// uint256 hex de 1_500_000 = 0x16E360
	calls := []rpcCall{
		{method: "eth_call", result: json.RawMessage(`"0x00000000000000000000000000000000000000000000000000000000001E8480"`)},
	}
	// 0x1E8480 = 2_000_000
	srv := httptest.NewServer(newMockRPCServer(calls).handler())
	defer srv.Close()

	safe := buildTestEVMSafe(t, srv.URL, 6)
	ctx := context.Background()

	const testAddr = "0xdead000000000000000000000000000000000001"
	money, err := safe.GetBalance(ctx, testAddr, 1, "USDC")
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if money.GetAssetCode() != "USDC" {
		t.Errorf("asset_code: got %q, want USDC", money.GetAssetCode())
	}
	if money.GetAmount() != 2_000_000 {
		t.Errorf("amount: got %d, want 2_000_000", money.GetAmount())
	}
	if money.GetScale() != 6 {
		t.Errorf("scale: got %d, want 6", money.GetScale())
	}
}

func TestEVMSafe_GetBalance_Overflow_MustError(t *testing.T) {
	// Simula um valor uint256 que excede math.MaxInt64.
	// Qualquer valor com bit 63 setado ja excede MaxInt64.
	// Usamos MaxUint64 + 1 = 0x10000000000000000 (17 bytes)
	// Para simplificar: MaxInt64 + 1 = 0x8000000000000000
	maxInt64Plus1 := new(big.Int).Add(
		big.NewInt(math.MaxInt64),
		big.NewInt(1),
	)
	// Converte para hex string ABI-padded (32 bytes = 64 hex chars)
	hexVal := fmt.Sprintf(`"0x%064x"`, maxInt64Plus1)

	calls := []rpcCall{
		{method: "eth_call", result: json.RawMessage(hexVal)},
	}
	srv := httptest.NewServer(newMockRPCServer(calls).handler())
	defer srv.Close()

	safe := buildTestEVMSafe(t, srv.URL, 6)
	ctx := context.Background()

	// INVARIANTE TX-2: overflow deve retornar erro, NUNCA truncar silenciosamente.
	const testAddr = "0xdead000000000000000000000000000000000001"
	// INVARIANTE TX-2: overflow deve retornar erro, NUNCA truncar silenciosamente.
	money, err := safe.GetBalance(ctx, testAddr, 1, "USDC")
	if err == nil {
		t.Fatalf("GetBalance: deveria retornar erro de overflow para valor %s, got money=%v",
			maxInt64Plus1.String(), money)
	}
	t.Logf("GetBalance overflow rejeitado corretamente: %v", err)
}

func TestEVMSafe_GetBalance_UnsupportedChain(t *testing.T) {
	safe := buildTestEVMSafe(t, "http://nao-usado", 6)
	ctx := context.Background()
	_, err := safe.GetBalance(ctx, "0xdead000000000000000000000000000000000001", 137, "USDC")
	if err == nil {
		t.Fatal("esperava ErrUnsupportedChain")
	}
}

func TestEVMSafe_GetBalance_AssetMismatch(t *testing.T) {
	safe := buildTestEVMSafe(t, "http://nao-usado", 6)
	ctx := context.Background()
	_, err := safe.GetBalance(ctx, "0xdead000000000000000000000000000000000001", 1, "USDT") // chain 1 esta configurada para USDC
	if err == nil {
		t.Fatal("esperava erro de asset mismatch")
	}
}

// ---------------------------------------------------------------------------
// Confirmations
// ---------------------------------------------------------------------------

// txReceiptJSON monta um JSON de receipt com blockNumber hex.
func txReceiptJSON(blockNum uint64) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"blockNumber":"0x%x","status":"0x1"}`, blockNum))
}

func TestEVMSafe_Confirmations_FinalityReached(t *testing.T) {
	// tx no bloco 100, bloco atual 113 -> 14 confirmacoes >= 12 min
	calls := []rpcCall{
		{method: "eth_getTransactionReceipt", result: txReceiptJSON(100)},
		{method: "eth_blockNumber", result: json.RawMessage(`"0x71"`)}, // 0x71 = 113
	}
	srv := httptest.NewServer(newMockRPCServer(calls).handler())
	defer srv.Close()

	safe := buildTestEVMSafe(t, srv.URL, 12)
	ctx := context.Background()

	confs, err := safe.Confirmations(ctx, "0xabc", 1)
	if err != nil {
		t.Fatalf("Confirmations: %v", err)
	}
	if confs != 14 { // 113 - 100 + 1 = 14
		t.Errorf("confs: got %d, want 14", confs)
	}
}

func TestEVMSafe_Confirmations_FinalityNotReached(t *testing.T) {
	// tx no bloco 100, bloco atual 105 -> 6 confirmacoes < 12 min
	calls := []rpcCall{
		{method: "eth_getTransactionReceipt", result: txReceiptJSON(100)},
		{method: "eth_blockNumber", result: json.RawMessage(`"0x69"`)}, // 0x69 = 105
	}
	srv := httptest.NewServer(newMockRPCServer(calls).handler())
	defer srv.Close()

	safe := buildTestEVMSafe(t, srv.URL, 12)
	ctx := context.Background()

	confs, err := safe.Confirmations(ctx, "0xabc", 1)
	if confs != 6 {
		t.Errorf("confs: got %d, want 6", confs)
	}
	if err == nil {
		t.Fatal("esperava ErrFinalityNotReached")
	}
	// Verifica que e ErrFinalityNotReached especificamente
	// (não apenas qualquer erro)
	var target *rpcError
	_ = target
	// errors.Is ou contains — nao podemos usar errors.Is diretamente aqui
	// pois o erro e wrappado; checamos a string do sentinel
	if !containsError(err, ErrFinalityNotReached) {
		t.Errorf("esperava ErrFinalityNotReached, got: %v", err)
	}
}

func TestEVMSafe_Confirmations_TxNotFound(t *testing.T) {
	// Servidor retorna null (ErrRPCNotFound) para receipt
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":null}`, req.ID)
	}))
	defer srv.Close()

	safe := buildTestEVMSafe(t, srv.URL, 12)
	ctx := context.Background()

	_, err := safe.Confirmations(ctx, "0xdeadbeef", 1)
	if err == nil {
		t.Fatal("esperava ErrNotFound")
	}
	if !containsError(err, ErrNotFound) {
		t.Errorf("esperava ErrNotFound, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// BuildPayout
// ---------------------------------------------------------------------------

func TestEVMSafe_BuildPayout_CalldataCorrect(t *testing.T) {
	safe := buildTestEVMSafe(t, "http://nao-usado", 6)
	ctx := context.Background()

	req := PayoutRequest{
		ToAddress:      "0xDead000000000000000000000000000000000001",
		Amount:         &moneyv1.Money{AssetCode: "USDC", Amount: 1_000_000, Scale: 6},
		ChainID:        1,
		IdempotencyKey: "payout:evt-001",
	}

	tx, err := safe.BuildPayout(ctx, req)
	if err != nil {
		t.Fatalf("BuildPayout: %v", err)
	}
	if tx.ChainID != 1 {
		t.Errorf("chainID: got %d, want 1", tx.ChainID)
	}
	// Payload deve ter: 20 bytes (contrato) + 4 (selector) + 32 (address) + 32 (uint256) = 88 bytes
	const expectedPayloadLen = 20 + 4 + 32 + 32
	if len(tx.Payload) != expectedPayloadLen {
		t.Errorf("payload len: got %d, want %d", len(tx.Payload), expectedPayloadLen)
	}
	// Verifica selector transfer = 0xa9059cbb nos bytes [20:24]
	if tx.Payload[20] != 0xa9 || tx.Payload[21] != 0x05 || tx.Payload[22] != 0x9c || tx.Payload[23] != 0xbb {
		t.Errorf("selector errado: got %x %x %x %x, want a9 05 9c bb",
			tx.Payload[20], tx.Payload[21], tx.Payload[22], tx.Payload[23])
	}
}

func TestEVMSafe_BuildPayout_NegativeAmount(t *testing.T) {
	safe := buildTestEVMSafe(t, "http://nao-usado", 6)
	ctx := context.Background()

	req := PayoutRequest{
		ToAddress: "0xDead000000000000000000000000000000000001",
		Amount:    &moneyv1.Money{AssetCode: "USDC", Amount: -1, Scale: 6},
		ChainID:   1,
	}
	_, err := safe.BuildPayout(ctx, req)
	if err == nil {
		t.Fatal("esperava erro para amount negativo")
	}
}

func TestEVMSafe_BuildPayout_ZeroAmount(t *testing.T) {
	safe := buildTestEVMSafe(t, "http://nao-usado", 6)
	ctx := context.Background()

	req := PayoutRequest{
		ToAddress: "0xDead000000000000000000000000000000000001",
		Amount:    &moneyv1.Money{AssetCode: "USDC", Amount: 0, Scale: 6},
		ChainID:   1,
	}
	_, err := safe.BuildPayout(ctx, req)
	if err == nil {
		t.Fatal("esperava erro para amount zero")
	}
}

func TestEVMSafe_BuildPayout_NilAmount(t *testing.T) {
	safe := buildTestEVMSafe(t, "http://nao-usado", 6)
	ctx := context.Background()

	req := PayoutRequest{
		ToAddress: "0xDead000000000000000000000000000000000001",
		Amount:    nil,
		ChainID:   1,
	}
	_, err := safe.BuildPayout(ctx, req)
	if err == nil {
		t.Fatal("esperava erro para amount nil")
	}
}

// ---------------------------------------------------------------------------
// WatchDeposits / InjectDeposit
// ---------------------------------------------------------------------------

func TestEVMSafe_WatchDeposits_ReceivesInjected(t *testing.T) {
	safe := buildTestEVMSafe(t, "http://nao-usado", 6)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := safe.WatchDeposits(ctx, "0xSafe", 1)
	if err != nil {
		t.Fatalf("WatchDeposits: %v", err)
	}

	ev := DepositEvent{
		TxHash:        "0x1234",
		ChainID:       1,
		BlockNumber:   1000,
		Confirmations: 0,
		Amount: &moneyv1.Money{
			AssetCode: "USDC",
			Amount:    5_000_000,
			Scale:     6,
		},
		ToAddress:  "0xSafe",
		DetectedAt: time.Now().UTC(),
	}

	n := safe.InjectDeposit("0xSafe", 1, ev)
	if n != 1 {
		t.Errorf("InjectDeposit: got %d receivers, want 1", n)
	}

	select {
	case received := <-ch:
		if received.TxHash != ev.TxHash {
			t.Errorf("TxHash: got %q, want %q", received.TxHash, ev.TxHash)
		}
		if received.Amount.GetAmount() != 5_000_000 {
			t.Errorf("Amount: got %d, want 5_000_000", received.Amount.GetAmount())
		}
	case <-time.After(time.Second):
		t.Fatal("timeout: nenhum evento recebido no canal de WatchDeposits")
	}
}

func TestEVMSafe_WatchDeposits_ChannelClosedOnContextCancel(t *testing.T) {
	safe := buildTestEVMSafe(t, "http://nao-usado", 6)
	ctx, cancel := context.WithCancel(context.Background())

	ch, err := safe.WatchDeposits(ctx, "0xSafe", 1)
	if err != nil {
		t.Fatalf("WatchDeposits: %v", err)
	}

	cancel()
	// Aguarda o canal ser fechado
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("canal deveria estar fechado apos cancel do contexto")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout: canal nao foi fechado apos cancel")
	}
}

// ---------------------------------------------------------------------------
// overflow helpers
// ---------------------------------------------------------------------------

func TestBigIntToInt64Safe_MaxInt64_Passes(t *testing.T) {
	n := big.NewInt(math.MaxInt64)
	v, err := bigIntToInt64Safe(n)
	if err != nil {
		t.Fatalf("bigIntToInt64Safe(MaxInt64): %v", err)
	}
	if v != math.MaxInt64 {
		t.Errorf("got %d, want %d", v, int64(math.MaxInt64))
	}
}

func TestBigIntToInt64Safe_MaxInt64Plus1_Errors(t *testing.T) {
	n := new(big.Int).Add(big.NewInt(math.MaxInt64), big.NewInt(1))
	_, err := bigIntToInt64Safe(n)
	if err == nil {
		t.Fatal("esperava erro de overflow para MaxInt64+1")
	}
	t.Logf("overflow corretamente rejeitado: %v", err)
}

func TestBigIntToInt64Safe_MaxUint256_Errors(t *testing.T) {
	// 2^256 - 1 (valor maximo de uint256)
	maxUint256 := new(big.Int).Sub(
		new(big.Int).Lsh(big.NewInt(1), 256),
		big.NewInt(1),
	)
	_, err := bigIntToInt64Safe(maxUint256)
	if err == nil {
		t.Fatal("esperava erro de overflow para maxUint256")
	}
}

// ---------------------------------------------------------------------------
// helper
// ---------------------------------------------------------------------------

// containsError verifica se err wrappa o target, percorrendo a cadeia de Unwrap.
func containsError(err, target error) bool {
	if err == nil {
		return false
	}
	for {
		if err == target {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
}
