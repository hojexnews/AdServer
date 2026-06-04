// Package chainconnector — evm_rpc.go
//
// Cliente JSON-RPC EVM enxuto (sem go-ethereum / sem dep externa).
//
// Implementa apenas as chamadas necessarias para o EVMSafe:
//   - eth_getTransactionReceipt  — bloco e status de uma tx
//   - eth_blockNumber            — bloco atual da chain
//   - eth_call                   — chamada de leitura (balanceOf)
//
// ABI minima hand-rolled:
//   - balanceOf(address)  selector 0x70a08231
//   - transfer(address,uint256) selector 0xa9059cbb
//
// Selectors keccak-256 sao constantes conhecidas; hardcodados com comentario.
// Nenhum float: valores uint256 on-chain chegam como hex string e sao
// convertidos via math/big.Int — nunca via float (TX-2).
//
// Todos os tipos exportados desta interface sao injetaveis via EVMRPCClient
// para que os testes usem httptest.Server sem rede real.
package chainconnector

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync/atomic"
)

// ---------------------------------------------------------------------------
// Interface injetavel — permite mock via httptest.Server nos testes
// ---------------------------------------------------------------------------

// EVMRPCClient e a interface de transporte JSON-RPC EVM.
// A implementacao concreta usa net/http; testes injetam um mock.
type EVMRPCClient interface {
	// Call executa uma chamada JSON-RPC e retorna o campo "result" bruto.
	// Retorna ErrRPCNotFound se o resultado for JSON null (tx desconhecida).
	Call(ctx context.Context, method string, params []interface{}) (json.RawMessage, error)
}

// ErrRPCNotFound e retornado pelo cliente quando o resultado JSON-RPC e null.
// Mapeado para ErrNotFound pelo EVMSafe.
var ErrRPCNotFound = errors.New("evm_rpc: resultado null (recurso nao encontrado na chain)")

// ---------------------------------------------------------------------------
// Implementacao HTTP concreta
// ---------------------------------------------------------------------------

// httpRPCClient implementa EVMRPCClient usando net/http padrao.
// Sem dependencia de go-ethereum.
type httpRPCClient struct {
	endpoint   string
	httpClient *http.Client
	idCounter  atomic.Int64
}

// newHTTPRPCClient cria um cliente HTTP para o endpoint JSON-RPC informado.
func newHTTPRPCClient(endpoint string, httpClient *http.Client) EVMRPCClient {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &httpRPCClient{
		endpoint:   endpoint,
		httpClient: httpClient,
	}
}

// rpcRequest e o corpo de uma requisicao JSON-RPC 2.0.
type rpcRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      int64         `json:"id"`
}

// rpcResponse e o corpo de uma resposta JSON-RPC 2.0.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError representa o campo "error" de uma resposta JSON-RPC.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
}

// Call implementa EVMRPCClient.
func (c *httpRPCClient) Call(ctx context.Context, method string, params []interface{}) (json.RawMessage, error) {
	id := c.idCounter.Add(1)
	reqBody := rpcRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		ID:      id,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("evm_rpc: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("evm_rpc: criar http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("evm_rpc: http do: %w", err)
	}
	defer resp.Body.Close()

	rawResp, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB max
	if err != nil {
		return nil, fmt.Errorf("evm_rpc: ler resposta: %w", err)
	}

	var rpcResp rpcResponse
	if err := json.Unmarshal(rawResp, &rpcResp); err != nil {
		return nil, fmt.Errorf("evm_rpc: unmarshal resposta: %w", err)
	}

	if rpcResp.Error != nil {
		return nil, rpcResp.Error
	}

	// null result = recurso nao encontrado (ex.: tx desconhecida)
	if string(rpcResp.Result) == "null" {
		return nil, ErrRPCNotFound
	}

	return rpcResp.Result, nil
}

// ---------------------------------------------------------------------------
// ABI minima hand-rolled — selectors keccak-256 conhecidos
// ---------------------------------------------------------------------------

// Selectors ERC-20 (4 bytes, keccak-256 das assinaturas de funcao):
//
//	balanceOf(address)       = keccak256("balanceOf(address)")[0:4]  = 0x70a08231
//	transfer(address,uint256)= keccak256("transfer(address,uint256)")[0:4] = 0xa9059cbb
var (
	selectorBalanceOf = [4]byte{0x70, 0xa0, 0x82, 0x31} // balanceOf(address)
	selectorTransfer  = [4]byte{0xa9, 0x05, 0x9c, 0xbb} // transfer(address,uint256)
)

// encodeBalanceOf codifica o calldata de balanceOf(address) para eth_call.
// Retorna hex string com "0x" prefix.
//
// ABI encoding: 4-byte selector + address padded to 32 bytes (left-zero padded).
func encodeBalanceOf(address string) (string, error) {
	addr, err := parseAddress(address)
	if err != nil {
		return "", fmt.Errorf("encodeBalanceOf: %w", err)
	}
	calldata := make([]byte, 4+32)
	copy(calldata[:4], selectorBalanceOf[:])
	// address e 20 bytes; ABI preenche com zeros a esquerda ate 32 bytes
	copy(calldata[4+12:], addr) // offset 4+12 = 16; 20 bytes de endereco
	return "0x" + hex.EncodeToString(calldata), nil
}

// encodeTransfer codifica o calldata de transfer(address,uint256) para BuildPayout.
// Retorna os bytes brutos (sem "0x") — serao embutidos em UnsignedTx.Payload.
//
// ABI encoding:
//
//	4-byte selector
//	address padded to 32 bytes (left-zero padded)
//	uint256 (32 bytes, big-endian)
func encodeTransfer(toAddress string, amount *big.Int) ([]byte, error) {
	addr, err := parseAddress(toAddress)
	if err != nil {
		return nil, fmt.Errorf("encodeTransfer: %w", err)
	}
	if amount == nil || amount.Sign() < 0 {
		return nil, errors.New("encodeTransfer: amount deve ser nao-negativo")
	}

	calldata := make([]byte, 4+32+32)
	copy(calldata[:4], selectorTransfer[:])

	// address: 20 bytes, ABI left-zero padded to 32
	copy(calldata[4+12:4+32], addr)

	// uint256: big-endian 32 bytes
	amtBytes := amount.Bytes()
	if len(amtBytes) > 32 {
		return nil, errors.New("encodeTransfer: amount excede uint256")
	}
	// right-align in 32-byte slot
	copy(calldata[4+32+(32-len(amtBytes)):], amtBytes)

	return calldata, nil
}

// decodeUint256Hex decodifica um resultado hex "0x..." de eth_call (32 bytes
// ABI-encoded uint256) para *big.Int.
//
// Retorna erro se o valor estiver malformado.
// Float PROIBIDO: resultado e sempre *big.Int (TX-2).
func decodeUint256Hex(hexStr string) (*big.Int, error) {
	s := strings.TrimPrefix(hexStr, "0x")
	if s == "" {
		return big.NewInt(0), nil
	}
	// ABI-encoded uint256 tem 64 hex chars (32 bytes); aceita mais curto
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("decodeUint256Hex: %w", err)
	}
	n := new(big.Int).SetBytes(b)
	return n, nil
}

// bigIntToInt64Safe converte *big.Int para int64 com checagem explicita de overflow.
//
// CRITICO (TX-2 / money invariante): uint256 on-chain pode exceder math.MaxInt64.
// Se o valor exceder MaxInt64, retorna erro — NUNCA trunca silenciosamente.
// Float PROIBIDO.
func bigIntToInt64Safe(n *big.Int) (int64, error) {
	if n == nil {
		return 0, nil
	}
	// big.Int.IsInt64() checa se cabe em int64 sem truncar
	if !n.IsInt64() {
		return 0, fmt.Errorf(
			"chainconnector: valor uint256 on-chain (%s) excede math.MaxInt64 (%d) — overflow explicitamente rejeitado (TX-2)",
			n.String(), int64(^uint64(0)>>1),
		)
	}
	return n.Int64(), nil
}

// ---------------------------------------------------------------------------
// Helpers de endereco EVM
// ---------------------------------------------------------------------------

// parseAddress extrai os 20 bytes de um endereco EVM "0x...".
// Nao faz checksum EIP-55 — aceita lowercase/uppercase.
func parseAddress(address string) ([]byte, error) {
	s := strings.TrimPrefix(address, "0x")
	s = strings.TrimPrefix(s, "0X")
	if len(s) != 40 {
		return nil, fmt.Errorf("evm: endereco invalido (esperado 40 hex chars): %q", address)
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("evm: endereco invalido: %w", err)
	}
	return b, nil
}

// normalizeAddress normaliza um endereco EVM para lowercase com "0x".
func normalizeAddress(address string) string {
	s := strings.TrimPrefix(address, "0x")
	s = strings.TrimPrefix(s, "0X")
	return "0x" + strings.ToLower(s)
}

// ---------------------------------------------------------------------------
// Helpers de conversao hex<->numero para respostas RPC
// ---------------------------------------------------------------------------

// hexToUint64 converte uma string "0x..." hexadecimal para uint64.
func hexToUint64(s string) (uint64, error) {
	s = strings.TrimPrefix(s, "0x")
	s = strings.TrimPrefix(s, "0X")
	if s == "" {
		return 0, nil
	}
	n := new(big.Int)
	_, ok := n.SetString(s, 16)
	if !ok {
		return 0, fmt.Errorf("hexToUint64: valor invalido: %q", s)
	}
	if !n.IsUint64() {
		return 0, fmt.Errorf("hexToUint64: valor excede uint64: %s", n.String())
	}
	return n.Uint64(), nil
}
