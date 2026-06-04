// Implementacao EVM stub do ChainConnector para testes (K0).
//
// EVMStub nao se conecta a nenhuma rede. Retorna valores deterministicos
// e controlados por teste. Satisfaz 100% a interface ChainConnector.
//
// # Uso nos testes
//
//	stub := chainconnector.NewEVMStub(chainconnector.EVMStubConfig{
//	    Balances: map[string]*moneyv1.Money{
//	        "0xABCD": {AssetCode: "USDC", Amount: 1_000_000, Scale: 6},
//	    },
//	    TxConfirmations: map[string]uint32{
//	        "0xdeadbeef": 12,
//	    },
//	    MinConfirmations: 6,
//	})
//
// # Invariantes mantidas pelo stub
//
//   - Nao usa float32 nem float64 em nenhum valor monetario (TX-2).
//   - Depositos injetados via InjectDeposit chegam no canal de WatchDeposits.
//   - GetBalance retorna ErrNotFound se o endereco nao estiver no mapa.
//   - BuildPayout valida que Amount.Amount > 0 e retorna payload deterministico.
//   - Confirmations retorna ErrNotFound para hashes desconhecidos.
//   - Chamadas concorrentes sao seguras (mutex interno).
package chainconnector

import (
	"context"
	"encoding/hex"
	"fmt"
	"sync"

	moneyv1 "github.com/hojex/adserver/gen/go/adserver/money/v1"
)

// EVMStubConfig parametriza o EVMStub para um caso de teste.
type EVMStubConfig struct {
	// Balances mapeia endereco -> Money pre-carregado.
	// Chave: endereco on-chain (case-insensitive nao garantido — use a
	// forma exata que sera passada a GetBalance).
	Balances map[string]*moneyv1.Money

	// TxConfirmations mapeia txHash -> numero de confirmacoes.
	TxConfirmations map[string]uint32

	// MinConfirmations e o numero minimo de confirmacoes para finalidade.
	// Usado pelo stub para decidir se ErrFinalityNotReached e retornado.
	// Zero significa "sem minimo" (qualquer numero de confirmacoes aceito).
	MinConfirmations uint32

	// SupportedChainIDs lista as chain ids que o stub aceita.
	// Se vazio, todas as chain ids sao aceitas (modo permissivo para teste).
	SupportedChainIDs []int64

	// DepositChannelBuffer define o buffer do canal de WatchDeposits.
	// Default 0 (unbuffered). Use > 0 em testes que injetam depositos sem
	// consumer imediato.
	DepositChannelBuffer int
}

// EVMStub e a implementacao EVM stub de ChainConnector.
// Zero-value nao e util — use NewEVMStub.
type EVMStub struct {
	cfg EVMStubConfig

	mu       sync.Mutex
	balances map[string]*moneyv1.Money
	txConfs  map[string]uint32

	// watchers armazena os canais abertos por WatchDeposits, indexados
	// por endereco. Fecha-os quando ctx for cancelado.
	watchersMu sync.Mutex
	watchers   map[string][]chan DepositEvent
}

// NewEVMStub cria um EVMStub com a configuracao fornecida.
// Copia os mapas para evitar mutacao externa.
func NewEVMStub(cfg EVMStubConfig) *EVMStub {
	s := &EVMStub{
		cfg:      cfg,
		balances: make(map[string]*moneyv1.Money, len(cfg.Balances)),
		txConfs:  make(map[string]uint32, len(cfg.TxConfirmations)),
		watchers: make(map[string][]chan DepositEvent),
	}
	for k, v := range cfg.Balances {
		// Copia rasa e suficiente: Money e imutavel por convencao.
		s.balances[k] = v
	}
	for k, v := range cfg.TxConfirmations {
		s.txConfs[k] = v
	}
	return s
}

// WatchDeposits implementa ChainConnector.WatchDeposits.
//
// Retorna um canal que recebe DepositEvents injetados via InjectDeposit.
// O canal e fechado quando ctx for cancelado.
// Retorna ErrUnsupportedChain se chainID nao for suportado.
func (s *EVMStub) WatchDeposits(ctx context.Context, address string, chainID int64) (<-chan DepositEvent, error) {
	if err := s.checkChain(chainID); err != nil {
		return nil, err
	}

	buf := s.cfg.DepositChannelBuffer
	ch := make(chan DepositEvent, buf)

	s.watchersMu.Lock()
	s.watchers[address] = append(s.watchers[address], ch)
	s.watchersMu.Unlock()

	// Goroutine de ciclo de vida: fecha o canal ao cancelar ctx.
	go func() {
		<-ctx.Done()
		s.watchersMu.Lock()
		defer s.watchersMu.Unlock()
		channels := s.watchers[address]
		remaining := channels[:0]
		for _, c := range channels {
			if c == ch {
				close(c)
			} else {
				remaining = append(remaining, c)
			}
		}
		s.watchers[address] = remaining
	}()

	return ch, nil
}

// InjectDeposit injeta um DepositEvent no canal de WatchDeposits para o
// endereco indicado. Usado exclusivamente nos testes para simular chegada
// de deposito on-chain.
//
// Retorna o numero de watchers que receberam o evento.
func (s *EVMStub) InjectDeposit(address string, ev DepositEvent) int {
	s.watchersMu.Lock()
	defer s.watchersMu.Unlock()
	channels := s.watchers[address]
	sent := 0
	for _, ch := range channels {
		select {
		case ch <- ev:
			sent++
		default:
			// Canal cheio: descarta (nao bloqueia — comportamento de stub).
		}
	}
	return sent
}

// GetBalance implementa ChainConnector.GetBalance.
//
// Retorna o Money pre-carregado para o endereco ou ErrNotFound se ausente.
// Retorna ErrUnsupportedChain se chainID nao for suportado.
// Float PROIBIDO: o Money retornado usa int64 minor-units (TX-2).
func (s *EVMStub) GetBalance(ctx context.Context, address string, chainID int64, assetCode string) (*moneyv1.Money, error) {
	if err := s.checkChain(chainID); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := balanceKey(address, assetCode)
	m, ok := s.balances[key]
	if !ok {
		// Tenta sem asset_code (modo legado de testes simples).
		m, ok = s.balances[address]
		if !ok {
			return nil, fmt.Errorf("%w: endereco=%q assetCode=%q", ErrNotFound, address, assetCode)
		}
	}
	// Verifica que o asset_code bate (invariante DA-10).
	if m.GetAssetCode() != "" && m.GetAssetCode() != assetCode {
		return nil, fmt.Errorf("%w: asset_code mismatch: registro=%q pedido=%q",
			ErrNotFound, m.GetAssetCode(), assetCode)
	}
	return m, nil
}

// SetBalance substitui o saldo de um endereco no stub.
// Util para testes que precisam alterar saldo mid-test.
// Float PROIBIDO: use minor-units inteiros em m.Amount.
func (s *EVMStub) SetBalance(address string, m *moneyv1.Money) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := balanceKey(address, m.GetAssetCode())
	s.balances[key] = m
}

// BuildPayout implementa ChainConnector.BuildPayout.
//
// Valida que Amount.Amount > 0 e retorna um UnsignedTx com payload
// deterministico (hex(chainID:to:amount:assetCode:idempotencyKey)).
// Retorna ErrUnsupportedChain se chainID nao for suportado.
// Float PROIBIDO: Amount.Amount deve ser int64 minor-units (TX-2).
func (s *EVMStub) BuildPayout(ctx context.Context, req PayoutRequest) (UnsignedTx, error) {
	if err := s.checkChain(req.ChainID); err != nil {
		return UnsignedTx{}, err
	}
	if req.Amount == nil || req.Amount.GetAmount() <= 0 {
		return UnsignedTx{}, fmt.Errorf("chainconnector: BuildPayout: Amount deve ser positivo (TX-2)")
	}
	// Payload deterministico para testes: hex de uma string descritiva.
	raw := fmt.Sprintf("chain=%d:to=%s:amount=%d:asset=%s:idem=%s",
		req.ChainID,
		req.ToAddress,
		req.Amount.GetAmount(),
		req.Amount.GetAssetCode(),
		req.IdempotencyKey,
	)
	return UnsignedTx{
		ChainID:                req.ChainID,
		Payload:                []byte(hex.EncodeToString([]byte(raw))),
		EstimatedFeeMinorUnits: 21_000, // 21000 gas (deterministico para stub)
	}, nil
}

// Confirmations implementa ChainConnector.Confirmations.
//
// Retorna o numero de confirmacoes pre-carregado para txHash.
// Retorna ErrNotFound se txHash nao existir.
// Retorna ErrFinalityNotReached se confirmacoes < MinConfirmations.
func (s *EVMStub) Confirmations(ctx context.Context, txHash string, chainID int64) (uint32, error) {
	if err := s.checkChain(chainID); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	confs, ok := s.txConfs[txHash]
	if !ok {
		return 0, fmt.Errorf("%w: txHash=%q", ErrNotFound, txHash)
	}
	min := s.cfg.MinConfirmations
	if min > 0 && confs < min {
		return confs, fmt.Errorf("%w: txHash=%q confs=%d min=%d",
			ErrFinalityNotReached, txHash, confs, min)
	}
	return confs, nil
}

// SetConfirmations substitui o numero de confirmacoes de uma transacao.
// Util para testes que simulam progressao de finalizacao.
func (s *EVMStub) SetConfirmations(txHash string, confs uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.txConfs[txHash] = confs
}

// ---------------------------------------------------------------------------
// helpers internos
// ---------------------------------------------------------------------------

// checkChain valida que chainID e suportado pelo stub.
// Se SupportedChainIDs for vazio, todas as chains sao aceitas.
func (s *EVMStub) checkChain(chainID int64) error {
	if len(s.cfg.SupportedChainIDs) == 0 {
		return nil
	}
	for _, id := range s.cfg.SupportedChainIDs {
		if id == chainID {
			return nil
		}
	}
	return fmt.Errorf("%w: chainID=%d", ErrUnsupportedChain, chainID)
}

// balanceKey cria a chave composta endereco+assetCode para o mapa de saldos.
func balanceKey(address, assetCode string) string {
	return address + ":" + assetCode
}
