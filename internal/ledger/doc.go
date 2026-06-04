// Package ledger fornece acesso ao ledger Postgres double-entry para o trilho
// cripto do AdServer (K3 / ADR-0004 §D).
//
// # Posicionamento arquitetural
//
// Este pacote e um pacote Go COMPARTILHADO (internal/) reutilizavel por
// services/payments (trilho cripto/fiat) e pelo futuro job de billing batch
// (I3). Ele NUNCA e importado por services/decision, internal/cascade nem
// internal/ranker — o motor de decisao le apenas budgets pre-computados,
// nunca consulta o ledger em tempo de decisao (ADR-0004 §C).
//
// # Invariantes inegociaveis (money-ledger-guardian vai auditar)
//
//   - TX-2 / DA-10: float PROIBIDO. Valores monetarios em int64 minor-units
//     (Money.Amount) + scale do Asset Registry (Money.Scale). NUMERIC(40,18)
//     no Postgres; math/big ou string nos SELECTS; nunca float64.
//   - Multi-moeda sem conversao automatica (DA-10): cada asset_code e ledger
//     isolado. Cambio AEV/BND <-> USDC/fiat so como par de postings explicito
//     com taxa registrada em metadata. Proibido cambio implicito.
//   - Toda movimentacao = par de postings idempotente (UNIQUE idempotency_key
//     por tenant). Reprocessamentos at-least-once nao criam duplicatas.
//   - Nenhuma captura grava saldo direto. Saldo derivado de postings via view
//     ledger.account_balances.
//   - Deposito cripto permanece PENDING ate finalidade (N confirmacoes via
//     webhook do custodiante — E.8 ADR-0004). Reorg = estorno auditavel (novo
//     par de postings com void:{original_key}).
//   - AEV/BND ficam enabled=false no Asset Registry ate spec oficial de scale.
//     Qualquer tentativa de criar conta/posting em ativo disabled retorna erro.
//   - Reconciliacao ABRE EXCECOES (ledger.reconciliation_exceptions) e NUNCA
//     autocorrige. Correcao exige posting de ajuste humano.
//   - PII/KYC nunca entra neste pacote: apenas tenant_id/advertiser_id
//     pseudonimos e enderecos de custodia da plataforma.
//
// # Schema
//
// Veja db/ledger/migrations/0001_ledger_schema_up.sql (schema base Fase 1) e
// db/ledger/migrations/0002_reconciliation_exceptions_up.sql (K3).
//
// # Uso tipico
//
//	store, err := ledger.New(ctx, dsn, logger)
//	// Provisionar conta cripto (idempotente):
//	acc, err := store.EnsureAccount(ctx, ledger.EnsureAccountParams{...})
//	// Registrar deposito cripto como PENDING:
//	ref, err := store.RecordDeposit(ctx, ledger.DepositParams{...})
//	// Finalizar deposito (PENDING -> posted):
//	err = store.FinalizeDeposit(ctx, ledger.FinalizeDepositParams{...})
//	// Estorno de reorg:
//	err = store.RecordReversal(ctx, ledger.ReversalParams{...})
//	// Consultar saldo derivado:
//	bal, err := store.Balance(ctx, tenantID, accountCode)
package ledger
