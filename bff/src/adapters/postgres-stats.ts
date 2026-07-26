/**
 * PostgresStatsAdapter — StatsAdapter real para o "perfil beta" (onda
 * "perfil beta", addon §3.6 front/BFF). Lê Postgres diretamente — SEM
 * ClickHouse, que não existe neste repo (item de infra, fora de alcance).
 *
 * ESCOPO DELIBERADAMENTE ASSIMÉTRICO:
 *   queryLive         → dados REAIS de stats.live_kpis.
 *   queryConsolidated → CONTINUA RECUSANDO. Não existe, neste perfil, fonte
 *     consolidada/faturável nenhuma (DA-7: o faturável reconcilia contra
 *     Iceberg, nunca contra streaming/ao-vivo). Em vez de duplicar a lógica
 *     de recusa (e correr o risco de a mensagem divergir com o tempo —
 *     doc-lie por cópia), este adapter DELEGA para UnconfiguredStatsAdapter
 *     (mesma classe, mesma mensagem, um único lugar para mudar).
 *
 * CONTRATO FIXO combinado com o data-platform-engineer (schema é posse dele,
 * não decidido aqui):
 *   view stats.live_kpis(tenant_id uuid, campaign_id bigint, impressions,
 *     clicks, conversions, as_of timestamptz)
 *   alimentada por stats.events_raw (impressão/clique/conversão gravados
 *   pelo collector). É "ao vivo", NUNCA "consolidado/faturável" — toda linha
 *   sai daqui com source="live" (ADR-0001).
 *
 * SEGURANÇA (TX-3/CA-1): mesmo padrão de PostgresConfigAdapter.withTenant —
 *   toda query roda dentro de uma transação que primeiro injeta
 *     SELECT set_config('adserver.tenant_id', $1, true)
 *   O tenantId vem SEMPRE do ctx da sessão (nunca do payload do cliente).
 *   Como defesa em profundidade (espelha postgres-payments.ts), a query
 *   TAMBÉM filtra explicitamente por
 *     lk.tenant_id = NULLIF(current_setting('adserver.tenant_id', true), '')::uuid
 *   em vez de depender só da RLS de um schema novo, ainda não auditado por
 *   um teste rls_isolation_test.sql equivalente ao de db/config e db/ledger.
 *   Único método público (queryLive) → único caminho de query → nenhum
 *   método "esquece" o GUC (o ponto-cego da 28ª onda, "2 de 45 métodos",
 *   não se repete aqui porque só existe um método que de fato consulta o
 *   banco).
 *
 * advertiserId: stats.live_kpis só carrega campaign_id, não advertiser_id.
 *   Filtramos por advertiserId via JOIN com config.campaigns, na MESMA
 *   transação/GUC — o RLS de config.campaigns (0002_config_rls_up.sql)
 *   também está ativo aqui, então o filtro é reforçado duas vezes (RLS de
 *   config.campaigns E o filtro explícito de tenant em stats.live_kpis).
 *
 * LACUNAS DO CONTRATO BETA — TODAS EXPRESSAS COMO `null`, NUNCA COMO NÚMERO.
 *
 * A regra desta fonte: o que não é medido sai `null` ("não disponível nesta
 * fonte"), jamais um valor de fachada. Um número com rótulo de fato é lido
 * como medição — foi por isso que a 31ª onda arrancou o stub de
 * `Math.random()` do dashboard, e o mesmo raciocínio se aplica a um zero fixo.
 *
 *   - `requests` (ad requests, PRÉ-impressão) → `null`. A tabela até os
 *     persiste, mas um ad request ocorre ANTES da escolha de campanha e por
 *     construção tem `campaign_id IS NULL`: ele cai numa linha própria por
 *     tenant, que o INNER JOIN com `config.campaigns` nunca alcança.
 *     Atribuí-lo a um anunciante seria invenção.
 *   - `inventoryLoss` → `null`, por depender de `requests` (o minuendo).
 *   - `totalCost` → `null`. `stats.live_kpis` não tem NENHUMA ligação com o
 *     ledger; não existe custo a reportar aqui. A versão anterior emitia
 *     `"0.00"` na moeda real das campanhas e o `money-ledger-guardian`
 *     BLOQUEOU a onda por isso: renderizado como `R$ 0,00` ao lado de
 *     impressões e cliques REAIS, é indistinguível de "gastei zero" — e o
 *     blast radius é maior que o das outras duas justamente por carregar
 *     símbolo de moeda. (A justificativa antiga, "schemas/stats.ts está fora
 *     do escopo desta onda", era falsa: a própria onda editou esse arquivo.)
 *     Quando `queryConsolidated` tiver um pipeline de custo real, ele reporta
 *     valor; esta fonte não.
 *
 * DA-10 preservado: quando as campanhas do anunciante usam MAIS de uma moeda,
 * o adapter lança `PRECONDITION_FAILED` em vez de escolher uma moeda
 * arbitrariamente. Isso vale mesmo com `totalCost` nulo — a checagem existe
 * para impedir agregação cross-moeda, não para preencher o campo.
 *
 * TX-2: nenhuma aritmética MONETÁRIA acontece neste arquivo — não há sequer
 *   uma quantia para operar, já que `totalCost` sai `null`. Os únicos números
 *   calculados (impressions/clicks/conversions/ctr) são CONTAGENS e RAZÕES:
 *   CTR usa divisão de ponto flutuante deliberadamente (mesmo padrão de
 *   in-memory-stats.ts), o que é seguro porque CTR não é quantia monetária.
 */

import { TRPCError } from "@trpc/server";
import type { Pool, PoolClient } from "pg";
import type { StatsAdapter } from "./stats-adapter.js";
import type { KpiRow } from "../schemas/stats.js";
import { UnconfiguredStatsAdapter } from "./unconfigured-stats.js";

// ---------------------------------------------------------------------------
// Row shape (snake_case do Postgres)
// ---------------------------------------------------------------------------

interface LiveKpiAggRow {
  impressions: string; // SUM(...)::text — nunca Number direto de dinheiro, mas aqui é contagem
  clicks: string;
  conversions: string;
  as_of: Date | null; // TIMESTAMPTZ — null quando não há nenhuma linha (SUM/MAX sobre zero linhas)
  currency_count: string; // COUNT(DISTINCT currency)::text
  currency: string | null;
}

/** Contagem (NÃO dinheiro) — BIGINT do driver pg chega como string. */
function toCount(v: string): number {
  return Number(v);
}

// ---------------------------------------------------------------------------
// PostgresStatsAdapter
// ---------------------------------------------------------------------------

export class PostgresStatsAdapter implements StatsAdapter {
  private readonly pool: Pool;
  /**
   * queryConsolidated delega inteiramente para esta instância — reusa a
   * MESMA classe/mensagem da 31ª onda em vez de duplicar texto (evita
   * doc-lie por cópia: se a mensagem mudar lá, muda aqui também).
   */
  private readonly unconfigured = new UnconfiguredStatsAdapter();

  constructor(pool: Pool) {
    this.pool = pool;
  }

  /**
   * Executa `fn` numa transação com o tenant injetado (RLS ativo). Espelha
   * PostgresConfigAdapter.withTenant (postgres-config.ts) — o tenantId
   * NUNCA vem do cliente, sempre do ctx da sessão autenticada.
   */
  private async withTenant<T>(
    tenantId: string,
    fn: (c: PoolClient) => Promise<T>
  ): Promise<T> {
    const client = await this.pool.connect();
    try {
      await client.query("BEGIN");
      await client.query(
        "SELECT set_config('adserver.tenant_id', $1, true)",
        [tenantId]
      );
      const out = await fn(client);
      await client.query("COMMIT");
      return out;
    } catch (err) {
      await client.query("ROLLBACK").catch(() => undefined);
      throw err;
    } finally {
      client.release();
    }
  }

  /**
   * queryConsolidated — SEMPRE recusa neste perfil (nenhuma fonte
   * consolidada/faturável existe ainda; DA-7 exige reconciliação contra
   * Iceberg). Delega para UnconfiguredStatsAdapter — mesma classe/mensagem
   * da 31ª onda, nunca uma cópia do texto.
   */
  queryConsolidated(): Promise<{ rows: KpiRow[]; asOf: Date | null }> {
    return this.unconfigured.queryConsolidated();
  }

  /**
   * queryLive — dados REAIS de stats.live_kpis (Postgres), agregados por
   * advertiserId via JOIN com config.campaigns. Ver as lacunas documentadas
   * no cabeçalho do módulo (requests/totalCost) antes de consumir o
   * resultado como se fosse um pipeline de custo completo.
   */
  queryLive(opts: {
    tenantId: string;
    advertiserId: string;
  }): Promise<{ rows: KpiRow[]; asOf: Date | null }> {
    return this.withTenant(opts.tenantId, async (c) => {
      const r = await c.query<LiveKpiAggRow>(
        `SELECT
           COALESCE(SUM(lk.impressions), 0)::text AS impressions,
           COALESCE(SUM(lk.clicks), 0)::text       AS clicks,
           COALESCE(SUM(lk.conversions), 0)::text  AS conversions,
           MAX(lk.as_of)                           AS as_of,
           COUNT(DISTINCT c.currency)::text        AS currency_count,
           MIN(c.currency)                         AS currency
         FROM stats.live_kpis lk
         JOIN config.campaigns c ON c.id = lk.campaign_id
         WHERE c.advertiser_id = $1
           AND lk.tenant_id = NULLIF(current_setting('adserver.tenant_id', true), '')::uuid`,
        [opts.advertiserId]
      );

      const row = r.rows[0];
      if (!row || row.as_of === null) {
        // Nenhum evento ao vivo para este anunciante — array vazio, nunca
        // fabricamos uma linha zerada (a UI trata isso como EmptyState,
        // ver web/console/src/app/dashboard/page.tsx).
        return { rows: [], asOf: null };
      }

      const currencyCount = toCount(row.currency_count);
      if (currencyCount > 1) {
        // DA-10: sem conversão automática entre moedas. Um único totalCost
        // não pode representar campanhas em moedas diferentes — falha alto
        // e claro em vez de escolher uma moeda arbitrariamente (mesma
        // disciplina do Fix-6 em postgres-payments.ts: nunca assumir
        // silenciosamente).
        throw new TRPCError({
          code: "PRECONDITION_FAILED",
          message:
            "Não é possível agregar o KPI ao vivo deste anunciante: as " +
            "campanhas usam moedas diferentes e o BFF nunca converte " +
            "moeda automaticamente (DA-10). Desagregue por campanha.",
        });
      }

      const impressions = toCount(row.impressions);
      const clicks = toCount(row.clicks);
      const conversions = toCount(row.conversions);

      // requests / inventoryLoss: NÃO atribuíveis a um anunciante nesta fonte.
      //
      // stats.live_kpis EXPÕE `requests` (a migration passou a persistir
      // event_type='ad_request'), mas um ad request precede a escolha de
      // campanha e por construção tem campaign_id NULL — ele cai numa linha
      // própria por tenant, que o INNER JOIN com config.campaigns abaixo
      // nunca alcança. Atribuir esse total a um anunciante seria invenção.
      //
      // Por isso emitimos `null` (= "não atribuível nesta fonte"), nunca um
      // número. A versão anterior fazia `requests = impressions`, o que zerava
      // inventoryLoss e exibia "Perda inventário: 0" como se fosse medição —
      // fabricação com rótulo de fato (mesma classe que a 31ª onda removeu do
      // dashboard). O contrato KpiRow agora aceita null nos dois campos.
      const requests = null;
      const inventoryLoss = null;
      const ctr =
        impressions > 0 ? (clicks / impressions).toFixed(5) : "0.00000";

      const asOfIso = row.as_of.toISOString();
      // totalCost: null — esta fonte NÃO rastreia custo (sem ligação com o
      // ledger). Ver o cabeçalho do módulo. A checagem de moeda única acima
      // continua valendo (DA-10: nunca agregar cross-moeda), mesmo sem
      // nenhum valor a reportar: ela protege a consulta, não o campo.

      const kpiRow: KpiRow = {
        periodStart: asOfIso,
        periodEnd: asOfIso,
        source: "live",
        requests,
        impressions,
        clicks,
        conversions,
        inventoryLoss,
        ctr,
        totalCost: null,
      };

      return { rows: [kpiRow], asOf: row.as_of };
    });
  }
}
