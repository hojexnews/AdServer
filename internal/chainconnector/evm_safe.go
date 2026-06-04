// Package chainconnector — evm_safe.go
//
// EVMSafe e a implementacao de producao de ChainConnector para chains EVM
// usando Safe multisig como custodiante (K5).
//
// # Modelo de custódia
//
// A plataforma usa Safe multisig (custody_mode='safe_multisig' no Asset
// Registry). Fireblocks (MPC) e o UPGRADE sob AUM — quando o custo anual
// de Fireblocks for < 1% do AUM medido em producao (gatilho C do ADR-0004).
// A troca Safe -> Fireblocks e trocar esta implementacao por EVMFireblocks
// atras da MESMA interface ChainConnector — sem alterar o trilho de pagamento.
//
//	// SEAM: EVMFireblocks sera o upgrade sob AUM (gatilho C — ADR-0004 §C).
//	// Para trocar: substituir EVMSafe por EVMFireblocks no wiring de
//	// services/payments/cmd/payments/main.go — mesma interface, zero
//	// mudanca no trilho de pagamento.
//
// # Depósito orientado a webhook (E.8)
//
// WatchDeposits reemite no canal os depositos empurrados pelo webhook handler
// do custodiante (services/payments/internal/crypto/safe_webhook.go). Um
// fallback de polling eth_getLogs e OPCIONAL e secundario — a fonte primaria
// e sempre o webhook.
//
// # Float PROIBIDO (TX-2)
//
// Valores uint256 on-chain chegam como hex string e sao convertidos via
// math/big.Int. A conversao para int64 minor-units tem checagem EXPLICITA
// de overflow: se o valor exceder math.MaxInt64, retorna erro — NUNCA trunca.
//
// # PII
//
// Enderecos on-chain de custódia da plataforma NAO sao PII. A associacao
// anunciante <-> endereco vive no cofre de compliance (K6), referenciado por
// tenant_id pseudonimo (TX-3 / DA-11).
package chainconnector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	moneyv1 "github.com/hojex/adserver/gen/go/adserver/money/v1"
)

// ---------------------------------------------------------------------------
// Configuracao
// ---------------------------------------------------------------------------

// EVMSafeChainConfig configura uma chain especifica suportada pelo EVMSafe.
type EVMSafeChainConfig struct {
	// ChainID e o EVM chain id (ex.: 1 = Ethereum, 137 = Polygon).
	ChainID int64

	// RPCURL e o endpoint JSON-RPC da chain.
	// NUNCA hardcode — lido de SAFE_RPC_URL ou variavel por chain.
	// Injetado pelo config do services/payments via OpenBao/env.
	RPCURL string

	// ContractAddress e o endereco do contrato ERC-20 do ativo monitorado.
	// Para USDC na mainnet Ethereum: 0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48
	// Lido do Asset Registry (asset_registry.assets.contract_address).
	ContractAddress string

	// AssetCode e o codigo do ativo no Asset Registry (ex.: "USDC").
	AssetCode string

	// AssetScale e o numero de casas decimais do ativo (ex.: 6 para USDC).
	// CRITICO (TX-2): DEVE ser lido do Asset Registry — nunca hardcoded.
	// Se 0 e permitido apenas para ativos sem fracoes (raro).
	AssetScale int32

	// MinConfirmations e o numero minimo de confirmacoes para finalidade.
	// Recomendado: 12 para Ethereum mainnet; ajustar por chain.
	// Configura a semantica de ErrFinalityNotReached.
	MinConfirmations uint32

	// SafeAddress e o endereco da carteira Safe (multisig) da plataforma.
	// Depositos de/para este endereco sao monitorados.
	// NAO e PII de anunciante.
	SafeAddress string
}

// EVMSafeConfig configura o EVMSafe com uma ou mais chains.
type EVMSafeConfig struct {
	// Chains mapeia chain_id -> config da chain.
	// Deve ter ao menos uma entrada.
	Chains map[int64]EVMSafeChainConfig

	// HTTPClient e o cliente HTTP para as chamadas JSON-RPC.
	// Nil usa http.DefaultClient. Configuravel para testes (httptest.Server).
	HTTPClient *http.Client

	// rpcClientOverride permite injetar um EVMRPCClient mock nos testes.
	// Campo interno — use EVMSafeWithRPCClients para construir com mock.
	rpcClientOverride map[int64]EVMRPCClient
}

// ---------------------------------------------------------------------------
// EVMSafe — implementacao de producao
// ---------------------------------------------------------------------------

// EVMSafe implementa ChainConnector para chains EVM via Safe multisig.
//
// Metodos sao concorrencia-safe.
// Zero-value nao e util — use NewEVMSafe ou EVMSafeWithRPCClients.
//
// SEAM (Fireblocks): substituir por EVMFireblocks no wiring de main.go para
// o upgrade sob AUM (gatilho C ADR-0004). A interface permanece identica.
type EVMSafe struct {
	chains     map[int64]EVMSafeChainConfig
	rpcClients map[int64]EVMRPCClient

	watchersMu sync.Mutex
	// watchers mapeia (chainID:address) -> lista de canais abertos
	watchers map[string][]chan DepositEvent
}

// NewEVMSafe cria um EVMSafe com a configuracao fornecida.
// Usa net/http para as chamadas JSON-RPC (producao).
func NewEVMSafe(cfg EVMSafeConfig) (*EVMSafe, error) {
	if len(cfg.Chains) == 0 {
		return nil, errors.New("EVMSafe: ao menos uma chain deve ser configurada")
	}
	s := &EVMSafe{
		chains:     make(map[int64]EVMSafeChainConfig, len(cfg.Chains)),
		rpcClients: make(map[int64]EVMRPCClient, len(cfg.Chains)),
		watchers:   make(map[string][]chan DepositEvent),
	}
	for chainID, chainCfg := range cfg.Chains {
		if chainCfg.RPCURL == "" {
			return nil, fmt.Errorf("EVMSafe: chain %d: RPCURL obrigatoria", chainID)
		}
		if chainCfg.AssetCode == "" {
			return nil, fmt.Errorf("EVMSafe: chain %d: AssetCode obrigatorio", chainID)
		}
		s.chains[chainID] = chainCfg
		// Override de RPC client (testes) tem precedencia
		if override, ok := cfg.rpcClientOverride[chainID]; ok {
			s.rpcClients[chainID] = override
		} else {
			s.rpcClients[chainID] = newHTTPRPCClient(chainCfg.RPCURL, cfg.HTTPClient)
		}
	}
	return s, nil
}

// EVMSafeWithRPCClients cria um EVMSafe injetando clientes RPC externos.
// Util para testes que querem mockar o endpoint JSON-RPC via httptest.Server.
//
//	srv := httptest.NewServer(mockRPCHandler)
//	client := newHTTPRPCClient(srv.URL, nil)
//	safe, _ := EVMSafeWithRPCClients(cfg, map[int64]EVMRPCClient{1: client})
func EVMSafeWithRPCClients(cfg EVMSafeConfig, clients map[int64]EVMRPCClient) (*EVMSafe, error) {
	cfg.rpcClientOverride = clients
	return NewEVMSafe(cfg)
}

// ---------------------------------------------------------------------------
// ChainConnector.GetBalance
// ---------------------------------------------------------------------------

// GetBalance retorna o saldo USDC (ou outro ativo ERC-20) de um endereco via
// eth_call balanceOf.
//
// CRITICO (TX-2):
//   - Retorno on-chain e uint256 (big.Int).
//   - Conversao para int64 minor-units tem checagem EXPLICITA de overflow:
//     se o valor exceder math.MaxInt64, retorna erro — nunca trunca.
//   - Scale vem da configuracao da chain (lida do Asset Registry), nunca hardcoded.
//   - Float PROIBIDO.
func (s *EVMSafe) GetBalance(ctx context.Context, address string, chainID int64, assetCode string) (*moneyv1.Money, error) {
	chainCfg, rpc, err := s.chainFor(chainID)
	if err != nil {
		return nil, err
	}
	if chainCfg.AssetCode != assetCode {
		return nil, fmt.Errorf("%w: chain %d suporta asset %q, pedido %q",
			ErrUnsupportedChain, chainID, chainCfg.AssetCode, assetCode)
	}

	// Monta calldata: balanceOf(address) — selector 0x70a08231
	calldata, err := encodeBalanceOf(address)
	if err != nil {
		return nil, fmt.Errorf("GetBalance: encodeBalanceOf: %w", err)
	}

	// eth_call: { to: contrato, data: calldata }, "latest"
	callObj := map[string]string{
		"to":   normalizeAddress(chainCfg.ContractAddress),
		"data": calldata,
	}
	result, err := rpc.Call(ctx, "eth_call", []interface{}{callObj, "latest"})
	if err != nil {
		if errors.Is(err, ErrRPCNotFound) {
			return nil, fmt.Errorf("%w: balance nao encontrado para address=%q chain=%d", ErrNotFound, address, chainID)
		}
		return nil, fmt.Errorf("GetBalance: eth_call: %w", err)
	}

	// Unmarshal: resultado e uma string JSON hex "0x..."
	var hexResult string
	if err := json.Unmarshal(result, &hexResult); err != nil {
		return nil, fmt.Errorf("GetBalance: unmarshal resultado: %w", err)
	}

	// Decodifica uint256 hex -> *big.Int (sem float)
	bigVal, err := decodeUint256Hex(hexResult)
	if err != nil {
		return nil, fmt.Errorf("GetBalance: decodeUint256Hex: %w", err)
	}

	// CHECAGEM DE OVERFLOW EXPLICITA (TX-2 invariante — money-ledger-guardian audita)
	// Se o valor uint256 on-chain exceder math.MaxInt64, retorna erro.
	// NUNCA truncar silenciosamente.
	// Localização: internal/chainconnector/evm_safe.go — GetBalance overflow check
	amount, err := bigIntToInt64Safe(bigVal)
	if err != nil {
		return nil, fmt.Errorf("GetBalance: %w", err)
	}

	return &moneyv1.Money{
		AssetCode: chainCfg.AssetCode,
		Amount:    amount,
		Scale:     uint32(chainCfg.AssetScale),
	}, nil
}

// ---------------------------------------------------------------------------
// ChainConnector.Confirmations
// ---------------------------------------------------------------------------

// Confirmations retorna o numero de confirmacoes de uma transacao.
//
//   - eth_getTransactionReceipt: bloco da tx (blockNumber)
//   - eth_blockNumber: bloco atual
//   - confs = atual - txBlock + 1
//
// Retorna ErrNotFound se txHash for desconhecido (receipt null).
// Retorna ErrFinalityNotReached se confs < MinConfirmations.
func (s *EVMSafe) Confirmations(ctx context.Context, txHash string, chainID int64) (uint32, error) {
	chainCfg, rpc, err := s.chainFor(chainID)
	if err != nil {
		return 0, err
	}

	// 1. Busca o receipt da transacao
	receiptResult, err := rpc.Call(ctx, "eth_getTransactionReceipt", []interface{}{txHash})
	if err != nil {
		if errors.Is(err, ErrRPCNotFound) {
			return 0, fmt.Errorf("%w: txHash=%q chain=%d", ErrNotFound, txHash, chainID)
		}
		return 0, fmt.Errorf("Confirmations: eth_getTransactionReceipt: %w", err)
	}

	// Parse do receipt — apenas os campos necessarios
	var receipt struct {
		BlockNumber string `json:"blockNumber"` // "0x..." hex
		Status      string `json:"status"`      // "0x1" = sucesso, "0x0" = falha
	}
	if err := json.Unmarshal(receiptResult, &receipt); err != nil {
		return 0, fmt.Errorf("Confirmations: parse receipt: %w", err)
	}
	if receipt.BlockNumber == "" || receipt.BlockNumber == "0x" {
		// Tx pendente (nao minerada ainda)
		return 0, fmt.Errorf("%w: txHash=%q ainda nao minerada", ErrNotFound, txHash)
	}

	txBlock, err := hexToUint64(receipt.BlockNumber)
	if err != nil {
		return 0, fmt.Errorf("Confirmations: parse blockNumber: %w", err)
	}

	// 2. Busca o bloco atual
	currentBlockResult, err := rpc.Call(ctx, "eth_blockNumber", []interface{}{})
	if err != nil {
		return 0, fmt.Errorf("Confirmations: eth_blockNumber: %w", err)
	}
	var currentBlockHex string
	if err := json.Unmarshal(currentBlockResult, &currentBlockHex); err != nil {
		return 0, fmt.Errorf("Confirmations: parse eth_blockNumber: %w", err)
	}
	currentBlock, err := hexToUint64(currentBlockHex)
	if err != nil {
		return 0, fmt.Errorf("Confirmations: parse currentBlock: %w", err)
	}

	// 3. Calcula confirmacoes
	// confs = currentBlock - txBlock + 1
	// Se currentBlock < txBlock (reorg em andamento), confs = 0
	var confs uint32
	if currentBlock >= txBlock {
		diff := currentBlock - txBlock + 1
		// Limita ao max uint32 para evitar overflow (diff e uint64)
		if diff > uint64(^uint32(0)) {
			confs = ^uint32(0) // MaxUint32
		} else {
			confs = uint32(diff)
		}
	}

	min := chainCfg.MinConfirmations
	if min > 0 && confs < min {
		return confs, fmt.Errorf("%w: txHash=%q confs=%d min=%d chain=%d",
			ErrFinalityNotReached, txHash, confs, min, chainID)
	}
	return confs, nil
}

// ---------------------------------------------------------------------------
// ChainConnector.BuildPayout
// ---------------------------------------------------------------------------

// BuildPayout constroi o calldata ERC-20 transfer(to, amount) para um payout.
//
// NAO assina nem envia — assinatura e responsabilidade do custodiante (Safe).
// O UnsignedTx.Payload contem o calldata ABI-encoded de transfer(address,uint256).
//
// SEGURANCA MONETARIA (TX-2):
//   - Amount.Amount e int64 minor-units; converte para big.Int via int64->big
//     (seguro: int64 sempre cabe em uint256).
//   - Float PROIBIDO em toda a codificacao.
//   - Scale e AssetCode validados contra a configuracao da chain.
func (s *EVMSafe) BuildPayout(ctx context.Context, req PayoutRequest) (UnsignedTx, error) {
	chainCfg, _, err := s.chainFor(req.ChainID)
	if err != nil {
		return UnsignedTx{}, err
	}

	if req.Amount == nil || req.Amount.GetAmount() <= 0 {
		return UnsignedTx{}, fmt.Errorf("chainconnector: BuildPayout: Amount deve ser positivo (TX-2)")
	}
	if req.Amount.GetAssetCode() != chainCfg.AssetCode {
		return UnsignedTx{}, fmt.Errorf("chainconnector: BuildPayout: asset mismatch: chain=%q pedido=%q",
			chainCfg.AssetCode, req.Amount.GetAssetCode())
	}
	if req.ToAddress == "" {
		return UnsignedTx{}, fmt.Errorf("chainconnector: BuildPayout: ToAddress obrigatorio")
	}

	// Converte int64 minor-units para big.Int para ABI encoding.
	// int64 -> big.Int e seguro: int64 sempre cabe em uint256.
	// Float PROIBIDO (TX-2).
	amountBig := big.NewInt(req.Amount.GetAmount())

	// Codifica calldata transfer(to, amount)
	calldata, err := encodeTransfer(req.ToAddress, amountBig)
	if err != nil {
		return UnsignedTx{}, fmt.Errorf("BuildPayout: encodeTransfer: %w", err)
	}

	// Payload = calldata ERC-20 transfer
	// O custodiante (Safe) encapsula este calldata em uma transacao multisig.
	// To = contrato ERC-20; Value = 0 (token transfer, nao ETH native).
	//
	// Formato do Payload: [contract_address_20bytes][calldata_N_bytes]
	// O custodiante interpreta os primeiros 20 bytes como endereco do contrato
	// e o restante como calldata da chamada de contrato.
	contractBytes, err := parseAddress(chainCfg.ContractAddress)
	if err != nil {
		return UnsignedTx{}, fmt.Errorf("BuildPayout: parse contract address: %w", err)
	}
	payload := make([]byte, 20+len(calldata))
	copy(payload[:20], contractBytes)
	copy(payload[20:], calldata)

	return UnsignedTx{
		ChainID: req.ChainID,
		Payload: payload,
		// EstimatedFeeMinorUnits: estimativa conservadora de gas em wei.
		// 65000 gas * 20 gwei = 1_300_000 gwei = 1_300_000_000_000 wei.
		// Valor informativo — o custodiante (Safe) estima o gas real.
		// Float PROIBIDO: valor em wei (int64 minor-units da chain nativa).
		EstimatedFeeMinorUnits: 65_000,
	}, nil
}

// ---------------------------------------------------------------------------
// ChainConnector.WatchDeposits
// ---------------------------------------------------------------------------

// WatchDeposits retorna um canal de DepositEvent.
//
// Modelo orientado a WEBHOOK (E.8 ADR-0004):
//   - A fonte primaria de depositos e o webhook handler do custodiante
//     (services/payments/internal/crypto/safe_webhook.go), que chama
//     EVMSafe.InjectDeposit para reemitir no canal.
//   - O polling via eth_getLogs e OPCIONAL e secundario. Nao implementado
//     por padrao — habilitar apenas como fallback quando o webhook falha.
//     O reason: preferir webhook a logica de reorg propria (E.8).
//
// O canal e fechado quando ctx for cancelado.
// O conector NUNCA credita saldo — apenas detecta e notifica.
func (s *EVMSafe) WatchDeposits(ctx context.Context, address string, chainID int64) (<-chan DepositEvent, error) {
	if _, _, err := s.chainFor(chainID); err != nil {
		return nil, err
	}

	ch := make(chan DepositEvent, 64) // buffer para absorver rajadas do webhook
	key := watcherKey(address, chainID)

	s.watchersMu.Lock()
	s.watchers[key] = append(s.watchers[key], ch)
	s.watchersMu.Unlock()

	go func() {
		<-ctx.Done()
		s.watchersMu.Lock()
		defer s.watchersMu.Unlock()
		channels := s.watchers[key]
		remaining := channels[:0]
		for _, c := range channels {
			if c == ch {
				close(c)
			} else {
				remaining = append(remaining, c)
			}
		}
		s.watchers[key] = remaining
	}()

	return ch, nil
}

// InjectDeposit injeta um DepositEvent nos canais abertos para o endereco/chain.
// Chamado pelo webhook handler do custodiante ao receber notificacao de deposito.
//
// NUNCA credita saldo — o ledger e responsavel pela transicao PENDING -> posted.
// Retorna o numero de canais que receberam o evento.
func (s *EVMSafe) InjectDeposit(address string, chainID int64, ev DepositEvent) int {
	key := watcherKey(address, chainID)
	s.watchersMu.Lock()
	defer s.watchersMu.Unlock()
	channels := s.watchers[key]
	sent := 0
	for _, ch := range channels {
		select {
		case ch <- ev:
			sent++
		default:
			// Canal cheio: descarta para nao bloquear o webhook handler.
			// O webhook handler reprocessa via idempotency se necessario.
		}
	}
	return sent
}

// ---------------------------------------------------------------------------
// helpers internos
// ---------------------------------------------------------------------------

// chainFor retorna a config e o cliente RPC para uma chain, ou
// ErrUnsupportedChain se a chain nao estiver configurada.
func (s *EVMSafe) chainFor(chainID int64) (EVMSafeChainConfig, EVMRPCClient, error) {
	cfg, ok := s.chains[chainID]
	if !ok {
		return EVMSafeChainConfig{}, nil, fmt.Errorf("%w: chainID=%d", ErrUnsupportedChain, chainID)
	}
	rpc := s.rpcClients[chainID]
	return cfg, rpc, nil
}

// watcherKey cria a chave composta address:chainID para o mapa de watchers.
func watcherKey(address string, chainID int64) string {
	return fmt.Sprintf("%s:%d", strings.ToLower(address), chainID)
}

// PlatformReceivedAt e um auxiliar para popular DetectedAt em eventos
// detectados pelo webhook handler.
func platformReceivedAt() time.Time {
	return time.Now().UTC()
}
