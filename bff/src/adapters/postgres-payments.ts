/**
 * PostgresPaymentsAdapter — adapter real de pagamentos para K7 (Fase 3).
 *
 * Substitui InMemoryPaymentsAdapter em produção (BFF_PG_DSN configurado).
 * Consulta ledger.account_balances + ledger.journal_entries no Postgres real.
 *
 * INVARIANTES INVIOLÁVEIS:
 *   TX-2 — valores monetários como string DECIMAL via pgNumericToWire/minorUnitsToWire.
 *           Sem Number(), sem aritmética monetária. Sem conversão automática de moeda (DA-10).
 *   TX-3 — SELECT set_config('adserver.tenant_id', $1, true) por request ativa a RLS do K3.
 *           Equivalente a SET LOCAL mas parametrizável no driver pg (SET não aceita $1).
 *           tenantId SEMPRE do ctx (sessão autenticada), nunca do cliente.
 *   DA-11 — payload de saída sem DSN, sem chaves de provedor, sem PII de contraparte.
 *   DA-10 — saldos isolados por ativo; nenhuma conversão automática BRL↔USDC.
 *   CA-1  — sem IDOR: filtra por tenant_id (RLS) E id na query de detalhe.
 *
 * ESTRUTURA DO LEDGER (K3):
 *   ledger.account_balances — VIEW: saldo derivado de postings por conta.
 *   ledger.journal_entries  — cabeçalho de transação (idempotency_key, status, metadata).
 *   ledger.postings         — linhas debit/credit em NUMERIC(40,18) minor units.
 *   asset_registry.assets   — scale autoritativo por ativo (TX-2/DA-10).
 *
 * MAPEAMENTO DE RAIL (idempotency_key prefix → PaymentRail):
 *   "stripe:"  → fiat_stripe
 *   "asaas:"   → fiat_asaas
 *   "mp:"      → fiat_mp
 *   "deposit:" → crypto_safe
 *
 * CONVERSÃO NUMERIC → string DECIMAL:
 *   ledger.postings.debit_amount/credit_amount são NUMERIC(40,18) minor-units.
 *   scale do ativo (asset_registry.assets.scale) divide os minor-units.
 *   Usamos aritmética de string BIGINT — NUNCA Number()/parseFloat.
 *
 * DSN via env BFF_PG_DSN (nunca do cliente, nunca no payload).
 */

import pg from "pg";
import type { Pool } from "pg";
import type { PaymentsAdapter } from "./payments-adapter.js";
import type {
  Balance,
  PaymentStatusItem,
  ListPaymentStatusInput,
} from "../schemas/payments.js";
import { pgNumericToWire } from "../schemas/money.js";
import type { PaymentRail } from "../schemas/payments.js";

// ---------------------------------------------------------------------------
// Pool singleton — criado uma vez por processo, reutilizado por todos os
// requests. O DSN (BFF_PG_DSN) é lido do ambiente apenas aqui no BFF.
// NUNCA expor o DSN no payload de saída.
// ---------------------------------------------------------------------------

/**
 * Cria um Pool pg a partir do env BFF_PG_DSN.
 * Retorna undefined se a variável não estiver configurada (modo dev/stub).
 * O DSN é lido estritamente do ambiente — nunca de parâmetros do cliente.
 */
export function createPgPool(): Pool | undefined {
  const dsn = process.env["BFF_PG_DSN"];
  if (!dsn) {
    return undefined;
  }
  // pg.Pool aceita connectionString. SSL configurado via query param no DSN
  // (ex.: ?sslmode=require) ou via env PGSSLMODE. Sem hardcode de cert aqui.
  return new pg.Pool({ connectionString: dsn, max: 10 });
}

// ---------------------------------------------------------------------------
// Helpers de conversão NUMERIC → string DECIMAL (TX-2)
// ---------------------------------------------------------------------------

/**
 * Converte minor-units (NUMERIC do Postgres) + scale para string DECIMAL.
 *
 * O Postgres retorna NUMERIC como string quando o driver pg é configurado
 * com parseType desabilitado para numerics (padrão para NUMERIC sem cast).
 * Na prática, pg retorna NUMERIC(40,18) como string JavaScript.
 *
 * Estratégia: BigInt(minorUnitsStr) para trabalhar em inteiros;
 * dividir inserindo o ponto decimal conforme o scale.
 * NUNCA usa Number() ou parseFloat (TX-2).
 *
 * Exemplos:
 *   minorUnitsToWire("150000", 2)     → { amount: "1500.00", currency: "BRL" }
 *   minorUnitsToWire("1000000", 6)    → { amount: "1.000000", currency: "USDC" }
 *   minorUnitsToWire("-5000", 2)      → { amount: "-50.00", currency: "BRL" }
 *
 * NOTA: ledger.postings usa NUMERIC(40,18) para minor-units com escala INTERNA de 18.
 * Porém os valores reais gravados pelos serviços Go usam int64 com a escala do ativo.
 * Exemplo: BRL scale=2 → "15000.000000000000000000" no Postgres = 150.00 BRL.
 * Logo: dividimos pelo fator 10^scale do ativo sobre o valor inteiro dos minor-units.
 *
 * O valor NUMERIC(40,18) do Postgres tem 18 casas decimais internas. Para obter
 * o valor em unidades maiores, usamos o scale do ativo (não as 18 casas do NUMERIC).
 * Os serviços Go gravam os minor-units como inteiros (ex.: 150000 para 1500.00 BRL),
 * armazenados como NUMERIC(40,18) = "150000.000000000000000000".
 * Precisamos pegar a parte inteira e dividir por 10^scale.
 */
function minorUnitsToDecimalStr(pgNumericStr: string, scale: number): string {
  if (typeof pgNumericStr !== "string") {
    throw new TypeError(
      `minorUnitsToDecimalStr: esperava string NUMERIC do Postgres, recebeu ${typeof pgNumericStr}. ` +
        "Nunca use Number para money (TX-2)."
    );
  }

  // Remove as casas decimais internas do NUMERIC(40,18) — os serviços Go
  // gravam inteiros; a parte fracionária interna é sempre ".000000000000000000".
  // Fazemos isso via BigInt da parte inteira.
  // Se o valor vier como "1500.000000000000000000", pegamos só "1500".
  const dotIdx = pgNumericStr.indexOf(".");
  const intPart = dotIdx >= 0 ? pgNumericStr.slice(0, dotIdx) : pgNumericStr;

  // Fix-5 (Money LOW): minor-units devem ser inteiros; qualquer fração real
  // (após remover zeros à direita) indica bug a montante — silêncio seria pior.
  if (dotIdx >= 0) {
    const fracPart = pgNumericStr.slice(dotIdx + 1).replace(/0+$/, "");
    if (fracPart !== "") {
      throw new Error(
        `minorUnitsToDecimalStr: fração inesperada em minor-units: ${pgNumericStr}. ` +
          "Minor-units devem ser inteiros; fração indica bug a montante (TX-2)."
      );
    }
  }

  // Trata o sinal negativo (TX-2: saldos negativos são válidos no ledger).
  const negative = intPart.startsWith("-");
  const absIntStr = negative ? intPart.slice(1) : intPart;

  if (scale === 0) {
    return negative ? `-${absIntStr}` : absIntStr;
  }

  // Insere o ponto decimal dividindo por 10^scale via manipulação de string.
  // Ex.: absIntStr="150000", scale=2 → intMajor="1500", frac="00" → "1500.00"
  const padded = absIntStr.padStart(scale + 1, "0");
  const major = padded.slice(0, padded.length - scale);
  const frac = padded.slice(padded.length - scale);
  const decimal = `${major || "0"}.${frac}`;

  return negative ? `-${decimal}` : decimal;
}

// ---------------------------------------------------------------------------
// Mapeamento de idempotency_key prefix → PaymentRail
// ---------------------------------------------------------------------------

/**
 * Infere o rail de pagamento a partir do prefixo da idempotency_key.
 * O Go grava: "stripe:{event_id}", "asaas:{charge_id}", "mp:{id}", "deposit:{tx_hash}".
 * Retorna undefined se o prefixo não for reconhecido (entry de outro tipo).
 */
function railFromIdempotencyKey(key: string): PaymentRail | undefined {
  if (key.startsWith("stripe:")) return "fiat_stripe";
  if (key.startsWith("asaas:")) return "fiat_asaas";
  if (key.startsWith("mp:")) return "fiat_mp";
  if (key.startsWith("deposit:")) return "crypto_safe";
  return undefined;
}

// ---------------------------------------------------------------------------
// PostgresPaymentsAdapter
// ---------------------------------------------------------------------------

export class PostgresPaymentsAdapter implements PaymentsAdapter {
  private readonly pool: Pool;

  constructor(pool: Pool) {
    this.pool = pool;
  }

  // -------------------------------------------------------------------------
  // getBalances
  //
  // Consulta ledger.account_balances (VIEW K3) agregando saldos por ativo.
  // Filtra apenas contas de passivo (kind = 'liability') — são as contas de
  // saldo do anunciante (adv:{id}:{asset}). Saldo positivo = crédito disponível.
  //
  // RLS: SET LOCAL adserver.tenant_id ativa a política do K3.
  // TX-2: balance (NUMERIC) convertido via minorUnitsToDecimalStr + pgNumericToWire.
  // DA-10: nenhuma conversão automática; cada ativo é linha independente.
  // -------------------------------------------------------------------------
  async getBalances(tenantId: string): Promise<Balance[]> {
    const client = await this.pool.connect();
    try {
      // Fix-1 (CRITICAL-1): SET LOCAL só persiste dentro de uma transação explícita.
      // Em autocommit, SET LOCAL é revertido após o próprio statement, ANTES do SELECT.
      // Envolvemos em BEGIN/COMMIT para que o SET LOCAL valha para todos os SELECTs.
      await client.query("BEGIN");

      // Fix-2 (defesa em camadas): set_config('adserver.tenant_id', $1, true) é o
      // equivalente parametrizável de SET LOCAL — escopo de transação (is_local=true).
      // SET LOCAL adserver.tenant_id = $1 é inválido no Postgres: SET/SET LOCAL é um
      // utility statement que não aceita bind parameters ($1). set_config() é uma
      // função SQL normal e aceita $1 corretamente.
      // Ativa a RLS (migração 0003: security_invoker na view account_balances +
      // políticas nas tabelas-base). O filtro explícito WHERE ... = current_setting(...)
      // é defense-in-depth adicional (CA-1/TX-3). Os dois mecanismos devem coincidir.
      // tenantId vem EXCLUSIVAMENTE do ctx (sessão autenticada), nunca do cliente.
      await client.query(
        "SELECT set_config('adserver.tenant_id', $1, true)",
        [tenantId]
      );

      // Agrega saldo por ativo: soma (debit - credit) de postings por conta do tenant.
      // A view account_balances já faz isso por conta; aqui somamos por asset_code.
      // Filtramos kind = 'liability' para pegar apenas as contas do anunciante.
      // JOIN com asset_registry para obter scale e name autoritativos (TX-2/DA-10).
      const result = await client.query<{
        asset_code: string;
        balance: string;       // NUMERIC retorna como string no driver pg
        name: string;
        scale: number;         // SMALLINT retorna como number (inteiro, não float)
      }>(
        `SELECT
           ab.asset_code,
           -- Agrega saldo de TODAS as contas do tenant neste ativo (liability).
           -- NUMERIC::text preserva a precisão sem perda de float (TX-2).
           SUM(ab.balance)::text               AS balance,
           COALESCE(ar.name, ab.asset_code)    AS name,
           -- Fix-6: ::int uniformiza o cast (igual a listPaymentStatus/getPaymentDetail).
           COALESCE(ar.scale::int, 2)          AS scale
         FROM ledger.account_balances ab
         LEFT JOIN asset_registry.assets ar ON ar.code = ab.asset_code
         WHERE ab.kind = 'liability'
           AND ab.tenant_id = NULLIF(current_setting('adserver.tenant_id', true), '')::uuid
         GROUP BY ab.asset_code, ar.name, ar.scale
         ORDER BY ab.asset_code`
      );

      await client.query("COMMIT");

      return result.rows.map((row) => {
        // TX-2: converte NUMERIC (minor-units) para string DECIMAL via scale.
        const scale = row.scale as number;
        const decimalStr = minorUnitsToDecimalStr(row.balance, scale);
        // pgNumericToWire valida que o resultado é string DECIMAL válida.
        const wire = pgNumericToWire(decimalStr, row.asset_code);
        return {
          asset_code: row.asset_code,
          amount: wire.amount,
          scale,
          label: row.name,
        };
      });
    } catch (err) {
      await client.query("ROLLBACK").catch(() => undefined);
      throw err;
    } finally {
      client.release();
    }
  }

  // -------------------------------------------------------------------------
  // listPaymentStatus
  //
  // Consulta ledger.journal_entries com ref_type = 'payment_event' (fiat) ou
  // idempotency_key LIKE 'deposit:%' (cripto). Une com ledger.postings para
  // obter o amount (debit_amount da conta da plataforma — lado que recebeu).
  //
  // Rail: inferido do prefixo da idempotency_key.
  // pix_end_to_end_id: extraído de metadata->>'pix_e2e_id' (asaas).
  // tx_hash: extraído de metadata->>'tx_hash' (crypto_safe).
  //
  // RLS: SET LOCAL adserver.tenant_id ativa a política do K3.
  // TX-3: filtra por tenant_id via RLS; sem IDOR.
  // TX-2: amount como string DECIMAL (minor-units → decimal via scale).
  // DA-11: sem PII no payload (nome, CPF, endereço descartados).
  // Sem conversão automática de moeda (DA-10).
  // -------------------------------------------------------------------------
  async listPaymentStatus(
    tenantId: string,
    input: ListPaymentStatusInput
  ): Promise<{ items: PaymentStatusItem[]; total: number }> {
    const client = await this.pool.connect();
    try {
      // Fix-1 (CRITICAL-1): transação explícita para que SET LOCAL persista ao SELECT.
      await client.query("BEGIN");

      // Fix-2 (defesa em camadas): set_config('adserver.tenant_id', $1, true) é o
      // equivalente parametrizável de SET LOCAL — escopo de transação (is_local=true).
      // SET LOCAL adserver.tenant_id = $1 é inválido no Postgres: SET/SET LOCAL é um
      // utility statement que não aceita bind parameters ($1). set_config() é uma
      // função SQL normal e aceita $1 corretamente.
      // Ativa a RLS (migração 0003) nas tabelas-base do ledger. O filtro WHERE explícito
      // por current_setting('adserver.tenant_id') é defense-in-depth (CA-1/TX-3).
      // tenantId vem EXCLUSIVAMENTE do ctx (sessão autenticada), nunca do cliente.
      await client.query(
        "SELECT set_config('adserver.tenant_id', $1, true)",
        [tenantId]
      );

      // Parâmetros da query — construção defensiva, sem interpolação de string.
      const params: (string | number)[] = [];
      const whereExtra: string[] = [];

      // Filtro por rail: mapeia PaymentRail → padrão de idempotency_key.
      if (input.rail !== undefined) {
        params.push(railToPrefix(input.rail));
        whereExtra.push(`je.idempotency_key LIKE $${params.length}`);
      }

      // Filtro por status.
      if (input.status !== undefined) {
        params.push(input.status);
        whereExtra.push(`je.status = $${params.length}`);
      }

      // Filtros de data (ISO-8601 → TIMESTAMPTZ).
      if (input.from !== undefined) {
        params.push(input.from);
        whereExtra.push(`je.created_at >= $${params.length}::timestamptz`);
      }
      if (input.to !== undefined) {
        params.push(input.to);
        whereExtra.push(`je.created_at <= $${params.length}::timestamptz`);
      }

      const extraSql =
        whereExtra.length > 0
          ? " AND " + whereExtra.join(" AND ")
          : "";

      // Query principal: entries de pagamento (ref_type = 'payment_event' OU
      // idempotency_key LIKE 'deposit:%') com o debit_amount agregado.
      // Fix-2: SET LOCAL DENTRO de transação ativa a RLS (migração 0003).
      // Filtro explícito de tenant_id na WHERE é defense-in-depth (CA-1/TX-3).
      // NUNCA varra página sem tenant — ambos os filtros devem coincidir.
      const baseWhere = `
        (je.ref_type = 'payment_event' OR je.idempotency_key LIKE 'deposit:%')
        AND je.tenant_id = NULLIF(current_setting('adserver.tenant_id', true), '')::uuid
        ${extraSql}
      `;

      // Conta total para paginação (sem OFFSET/LIMIT).
      const countResult = await client.query<{ total: string }>(
        `SELECT COUNT(*)::text AS total
         FROM ledger.journal_entries je
         WHERE ${baseWhere}`,
        params
      );
      const total = parseInt(countResult.rows[0]?.total ?? "0", 10);

      // Paginação.
      const offset = (input.page - 1) * input.pageSize;
      const paginatedParams = [...params, input.pageSize, offset];

      // Query principal com amount agregado por entry.
      // Pega o debit_amount (lado que a plataforma recebeu) e o asset_code.
      // Usa MAX(scale) do asset_registry para a conversão (invariante: scale é
      // imutável por ativo habilitado — DA-10).
      // pix_e2e_id e tx_hash extraídos do JSONB metadata sem PII.
      //
      // Fix-4 (Money MEDIUM): premissa do modelo K4/K5 — cada journal_entry de
      // pagamento tem exatamente UM posting de débito (par único de postings).
      // Se uma entry tiver múltiplos débitos (split/multi-leg), SUM(debit_amount)
      // inflaria o amount silenciosamente. O HAVING exclui essas entries
      // da listagem — preferimos ausência a valor inflado (DA-10/TX-2).
      const dataResult = await client.query<{
        payment_id: string;
        idempotency_key: string;
        status: string;
        asset_code: string;
        amount_minor: string;   // SUM(debit_amount)::text — NUMERIC como string
        scale: number;          // COALESCE(ar.scale, 2) — SMALLINT
        created_at: string;     // TIMESTAMPTZ como string ISO-8601
        pix_e2e_id: string | null;
        tx_hash: string | null;
      }>(
        `SELECT
           je.id::text                              AS payment_id,
           je.idempotency_key,
           je.status::text,
           -- asset_code do primeiro posting (todos os postings de uma entry
           -- são do mesmo ativo em uma entry de pagamento fiat/cripto simples).
           MIN(p.asset_code)                        AS asset_code,
           -- Soma debit_amount (lado plataforma recebeu) — NUMERIC::text (TX-2).
           SUM(p.debit_amount)::text                AS amount_minor,
           -- Scale autoritativo do ativo (asset_registry — DA-10).
           COALESCE(MAX(ar.scale)::int, 2)          AS scale,
           je.created_at::text                      AS created_at,
           -- pix_e2e_id: apenas para trilho fiat_asaas — opaco, sem PII.
           je.metadata->>'pix_e2e_id'               AS pix_e2e_id,
           -- tx_hash: apenas para trilho crypto_safe — opaco, sem PII de contraparte.
           je.metadata->>'tx_hash'                  AS tx_hash
         FROM ledger.journal_entries je
         JOIN ledger.postings p ON p.journal_entry_id = je.id
         LEFT JOIN asset_registry.assets ar ON ar.code = p.asset_code
         WHERE ${baseWhere}
           AND p.debit_amount > 0
         GROUP BY je.id, je.idempotency_key, je.status, je.created_at,
                  je.metadata->>'pix_e2e_id', je.metadata->>'tx_hash'
         HAVING COUNT(*) FILTER (WHERE p.debit_amount > 0) = 1
         ORDER BY je.created_at DESC
         LIMIT $${paginatedParams.length - 1} OFFSET $${paginatedParams.length}`,
        paginatedParams
      );

      const items: PaymentStatusItem[] = [];
      for (const row of dataResult.rows) {
        const rail = railFromIdempotencyKey(row.idempotency_key);
        if (rail === undefined) {
          // Entry de tipo desconhecido — ignorar (não expor ao cliente).
          continue;
        }

        const scale = row.scale as number;
        const decimalStr = minorUnitsToDecimalStr(row.amount_minor, scale);
        const wire = pgNumericToWire(decimalStr, row.asset_code);

        const item: PaymentStatusItem = {
          payment_id: row.payment_id,
          rail,
          // O status do ledger espelha o enum do schema (pending/posted/void).
          status: row.status as "pending" | "posted" | "void",
          amount: wire,
          created_at: normalizeIso(row.created_at),
        };

        // Campos opcionais — apenas se presentes e não-nulos.
        // DA-11: pix_e2e_id e tx_hash são opacos; sem PII de contraparte.
        if (row.pix_e2e_id !== null && row.pix_e2e_id !== "") {
          item.pix_end_to_end_id = row.pix_e2e_id;
        }
        if (row.tx_hash !== null && row.tx_hash !== "") {
          item.tx_hash = row.tx_hash;
        }

        items.push(item);
      }

      await client.query("COMMIT");
      return { items, total };
    } catch (err) {
      await client.query("ROLLBACK").catch(() => undefined);
      throw err;
    } finally {
      client.release();
    }
  }

  // -------------------------------------------------------------------------
  // getPaymentDetail
  //
  // Retorna detalhe de UM pagamento, filtrado por tenant_id (RLS + WHERE explícito)
  // E id. Retorna null se não existir NO ESCOPO do tenant — sem distinguir
  // "não existe" de "é de outro tenant" (sem IDOR; CA-1/TX-3).
  //
  // TX-3: query filtra por tenant_id via RLS (SET LOCAL) E id explícito na
  // cláusula WHERE — nunca varre página inteira, nunca expõe IDs de outros tenants.
  // -------------------------------------------------------------------------
  async getPaymentDetail(
    tenantId: string,
    paymentId: string
  ): Promise<PaymentStatusItem | null> {
    const client = await this.pool.connect();
    try {
      // Fix-1 (CRITICAL-1): transação explícita para que SET LOCAL persista ao SELECT.
      await client.query("BEGIN");

      // Fix-2 (defesa em camadas): set_config('adserver.tenant_id', $1, true) é o
      // equivalente parametrizável de SET LOCAL — escopo de transação (is_local=true).
      // SET LOCAL adserver.tenant_id = $1 é inválido no Postgres: SET/SET LOCAL é um
      // utility statement que não aceita bind parameters ($1). set_config() é uma
      // função SQL normal e aceita $1 corretamente.
      // Ativa a RLS (migração 0003) nas tabelas-base do ledger. O filtro WHERE explícito
      // por current_setting('adserver.tenant_id') é defense-in-depth (CA-1/TX-3).
      // tenantId vem EXCLUSIVAMENTE do ctx (sessão autenticada), nunca do cliente.
      await client.query(
        "SELECT set_config('adserver.tenant_id', $1, true)",
        [tenantId]
      );

      // Filtra por tenant_id (RLS + explícito) E id — sem IDOR.
      // Usa CAST de paymentId para BIGINT (os IDs do ledger são BIGSERIAL/int8).
      // Se o cast falhar, o Postgres retorna erro → capturamos e retornamos null.
      let numericId: number;
      try {
        numericId = parseInt(paymentId, 10);
        if (!Number.isInteger(numericId) || numericId <= 0 || String(numericId) !== paymentId) {
          // paymentId não é um inteiro positivo válido — retorna null sem IDOR.
          await client.query("ROLLBACK").catch(() => undefined);
          return null;
        }
      } catch {
        await client.query("ROLLBACK").catch(() => undefined);
        return null;
      }

      // Fix-4 (Money MEDIUM): premissa do modelo K4/K5 — cada journal_entry de
      // pagamento tem exatamente UM posting de débito (par único de postings).
      // HAVING COUNT(*) FILTER (WHERE p.debit_amount > 0) = 1 exclui entries com
      // múltiplos débitos (split/multi-leg) — preferimos null a valor inflado (TX-2).
      const result = await client.query<{
        payment_id: string;
        idempotency_key: string;
        status: string;
        asset_code: string;
        amount_minor: string;
        scale: number;
        created_at: string;
        pix_e2e_id: string | null;
        tx_hash: string | null;
      }>(
        `SELECT
           je.id::text                              AS payment_id,
           je.idempotency_key,
           je.status::text,
           MIN(p.asset_code)                        AS asset_code,
           SUM(p.debit_amount)::text                AS amount_minor,
           COALESCE(MAX(ar.scale)::int, 2)          AS scale,
           je.created_at::text                      AS created_at,
           je.metadata->>'pix_e2e_id'               AS pix_e2e_id,
           je.metadata->>'tx_hash'                  AS tx_hash
         FROM ledger.journal_entries je
         JOIN ledger.postings p ON p.journal_entry_id = je.id
         LEFT JOIN asset_registry.assets ar ON ar.code = p.asset_code
         WHERE (je.ref_type = 'payment_event' OR je.idempotency_key LIKE 'deposit:%')
           AND je.tenant_id = NULLIF(current_setting('adserver.tenant_id', true), '')::uuid
           AND je.id = $1
           AND p.debit_amount > 0
         GROUP BY je.id, je.idempotency_key, je.status, je.created_at,
                  je.metadata->>'pix_e2e_id', je.metadata->>'tx_hash'
         HAVING COUNT(*) FILTER (WHERE p.debit_amount > 0) = 1
         LIMIT 1`,
        [numericId]
      );

      await client.query("COMMIT");

      if (result.rows.length === 0) {
        return null;
      }

      const row = result.rows[0];
      if (!row) return null;

      const rail = railFromIdempotencyKey(row.idempotency_key);
      if (rail === undefined) {
        // Entry de tipo desconhecido — retorna null sem IDOR.
        return null;
      }

      const scale = row.scale as number;
      const decimalStr = minorUnitsToDecimalStr(row.amount_minor, scale);
      const wire = pgNumericToWire(decimalStr, row.asset_code);

      const item: PaymentStatusItem = {
        payment_id: row.payment_id,
        rail,
        status: row.status as "pending" | "posted" | "void",
        amount: wire,
        created_at: normalizeIso(row.created_at),
      };

      if (row.pix_e2e_id !== null && row.pix_e2e_id !== "") {
        item.pix_end_to_end_id = row.pix_e2e_id;
      }
      if (row.tx_hash !== null && row.tx_hash !== "") {
        item.tx_hash = row.tx_hash;
      }

      return item;
    } catch (err) {
      await client.query("ROLLBACK").catch(() => undefined);
      throw err;
    } finally {
      client.release();
    }
  }
}

// ---------------------------------------------------------------------------
// Helpers internos
// ---------------------------------------------------------------------------

/**
 * Mapeia PaymentRail para prefixo de LIKE na idempotency_key.
 * DA-10: sem conversão automática — apenas seleção de padrão.
 */
function railToPrefix(rail: PaymentRail): string {
  switch (rail) {
    case "fiat_stripe":  return "stripe:%";
    case "fiat_asaas":   return "asaas:%";
    case "fiat_mp":      return "mp:%";
    case "crypto_safe":  return "deposit:%";
  }
}

/**
 * Normaliza a representação de timestamp do Postgres para ISO-8601 com 'Z'.
 * O Postgres pode retornar TIMESTAMPTZ com offset (ex.: "2026-06-01 12:00:00+00").
 * O schema Zod exige z.string().datetime() — aceita ISO-8601 com 'T' e 'Z'.
 */
function normalizeIso(pgTimestamp: string): string {
  // Se já tem 'T', está em formato ISO — garante 'Z' se offset for +00.
  if (pgTimestamp.includes("T")) {
    return pgTimestamp.replace("+00:00", "Z").replace("+00", "Z");
  }
  // Formato "YYYY-MM-DD HH:MM:SS+00" → "YYYY-MM-DDTHH:MM:SS.000Z"
  const normalized = pgTimestamp
    .replace(" ", "T")
    .replace(/\+00(:?00)?$/, "Z")
    .replace(/\.\d+Z$/, "Z");
  // Se ainda não tem Z, adiciona (assume UTC — o ledger armazena em UTC).
  return normalized.endsWith("Z") ? normalized : normalized + "Z";
}
