/**
 * Testes do PostgresStatsAdapter — onda "perfil beta" (addon §3.6 front/BFF).
 *
 * Sem `jest.fn()` (mesma razão de postgres-config.test.ts/postgres-
 * payments.test.ts: compatibilidade com ESM isolatedModules do preset
 * `ts-jest/presets/default-esm` — o global `jest` só é injetado
 * automaticamente pelo runner CJS legado; sem `@jest/globals`, qualquer uso
 * de `jest.fn()` aqui explodiria com "jest is not defined", exatamente o
 * achado #14 da 27ª onda em copilot.test.ts). `describe`/`test`/`expect` SÃO
 * injetados como globals pelo preset ESM — não precisam de import.
 *
 * Cobre:
 *   1. queryLive: mapeamento de linhas (impressions/clicks/conversions/ctr/
 *      inventoryLoss/totalCost) a partir de uma linha agregada do Postgres.
 *   2. queryLive: filtro por tenant — SET_CONFIG(tenantId) + WHERE explícito
 *      de tenant_id na query SQL (defesa em profundidade, TX-3/CA-1).
 *   3. queryLive: filtro por advertiserId (JOIN com config.campaigns).
 *   4. queryLive: nenhum dado ao vivo -> { rows: [], asOf: null } (nunca
 *      fabrica uma linha zerada).
 *   5. queryLive: moedas divergentes entre campanhas -> PRECONDITION_FAILED
 *      (DA-10 — nunca converte moeda automaticamente).
 *   6. queryConsolidated: sempre rejeita com PRECONDITION_FAILED — MESMA
 *      instância/mensagem de UnconfiguredStatsAdapter (reuso, não cópia).
 *   7. Prova de mutação (ao vivo, via cp/mv — nunca `git checkout`): remover
 *      o filtro de tenant da query faz o teste de isolamento ficar VERMELHO.
 *   8. Seleção do adapter em bff/src/index.ts (fiação): prova por leitura do
 *      código-fonte, NÃO por import — importar index.ts executaria
 *      `server.listen(PORT)` como efeito colateral do módulo (nenhum teste
 *      no repo importa index.ts hoje, de propósito).
 *
 * PENDÊNCIA (documentada no relatório da onda): o schema `stats` ainda não
 * existe no Postgres local (adserver_dev) no momento em que esta suíte foi
 * escrita — verificado com `\dn`/`\dv stats.*` via psql real. A prova
 * ponta-a-ponta contra `stats.live_kpis` fica pendente da migration do
 * data-platform-engineer; esta suíte prova o adapter via mock de Pool/Client
 * (mesmo padrão de postgres-config.test.ts/postgres-payments.test.ts).
 */

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";
import { TRPCError } from "@trpc/server";
import { PostgresStatsAdapter } from "./postgres-stats.js";
import { UnconfiguredStatsAdapter } from "./unconfigured-stats.js";

// ---------------------------------------------------------------------------
// Mock de Pool/Client — espelha postgres-payments.test.ts (sem jest.fn()).
// ---------------------------------------------------------------------------

type QueryRecord = { text: string; values: unknown[] } | { text: string };

type MockQueryFn = {
  (text: string, values?: unknown[]): Promise<{ rows: unknown[] }>;
  mockImpl:
    | ((text: string, values?: unknown[]) => Promise<{ rows: unknown[] }>)
    | undefined;
};

type MockClient = { query: MockQueryFn; release: () => void };

function makeMockClient(rowsForSelect: unknown[]) {
  const queries: QueryRecord[] = [];

  const queryFn = (async (text: string, values?: unknown[]) => {
    if (queryFn.mockImpl !== undefined) return queryFn.mockImpl(text, values);
    if (values !== undefined) queries.push({ text, values });
    else queries.push({ text });

    if (text === "BEGIN" || text === "COMMIT" || text === "ROLLBACK") {
      return { rows: [] };
    }
    if (text.startsWith("SELECT set_config")) {
      return { rows: [] };
    }
    // A única query de dados deste adapter — retorna a linha controlada.
    return { rows: rowsForSelect };
  }) as MockQueryFn;
  queryFn.mockImpl = undefined;

  const client: MockClient = { query: queryFn, release: () => undefined };
  return { client, queries };
}

function makeMockPool(client: MockClient) {
  const connectCalls: number[] = [];
  const pool = {
    connect: async () => {
      connectCalls.push(1);
      return client;
    },
    _connectCalls: connectCalls,
  } as unknown as import("pg").Pool & { _connectCalls: number[] };
  return pool;
}

/** Linha agregada canônica — impressions/clicks/conversions vêm como string do driver pg. */
function canonicalRow(overrides?: Partial<{
  impressions: string;
  clicks: string;
  conversions: string;
  as_of: Date | null;
  currency_count: string;
  currency: string | null;
}>) {
  return {
    impressions: "1000",
    clicks: "35",
    conversions: "4",
    as_of: new Date("2026-07-25T12:00:00.000Z"),
    currency_count: "1",
    currency: "BRL",
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// 1-3. queryLive: mapeamento, filtro de tenant, filtro de advertiserId
// ---------------------------------------------------------------------------

describe("PostgresStatsAdapter.queryLive — mapeamento de linhas (TX-2: contagens, não dinheiro)", () => {
  test("mapeia impressions/clicks/conversions/ctr/inventoryLoss a partir da linha agregada", async () => {
    const { client } = makeMockClient([canonicalRow()]);
    const pool = makeMockPool(client);
    const adapter = new PostgresStatsAdapter(pool);

    const { rows, asOf } = await adapter.queryLive({
      tenantId: "tenant-1",
      advertiserId: "42",
    });

    expect(rows).toHaveLength(1);
    const row = rows[0]!;
    expect(row.source).toBe("live");
    expect(row.impressions).toBe(1000);
    expect(row.clicks).toBe(35);
    expect(row.conversions).toBe(4);
    // requests/inventoryLoss são NULL — "não atribuível nesta fonte", nunca
    // um número. Um ad request precede a escolha de campanha (campaign_id
    // NULL em stats.events_raw), então o INNER JOIN com config.campaigns não
    // o alcança e atribuí-lo a um anunciante seria invenção.
    //
    // Este teste é o guarda contra a regressão para `requests = impressions`,
    // que zerava inventoryLoss e exibia "Perda inventário: 0" como medição.
    expect(row.requests).toBeNull();
    expect(row.inventoryLoss).toBeNull();
    expect(row.ctr).toBe((35 / 1000).toFixed(5));
    expect(asOf).toEqual(new Date("2026-07-25T12:00:00.000Z"));
    expect(row.periodStart).toBe("2026-07-25T12:00:00.000Z");
    expect(row.periodEnd).toBe("2026-07-25T12:00:00.000Z");
  });

  test("totalCost é null — esta fonte não rastreia custo, e zero fixo seria lido como gasto real", async () => {
    const { client } = makeMockClient([canonicalRow({ currency: "USDC" })]);
    const pool = makeMockPool(client);
    const adapter = new PostgresStatsAdapter(pool);

    const { rows } = await adapter.queryLive({
      tenantId: "tenant-1",
      advertiserId: "42",
    });

    // Guarda de regressão para o BLOQUEIO do money-ledger-guardian na onda
    // "perfil BETA": o adapter emitia { amount: "0.00", currency } e a UI
    // renderizava "R$ 0,00" ao lado de impressões e cliques REAIS —
    // indistinguível de "gastei zero" quando a verdade é "não medimos custo".
    // stats.live_kpis não tem ligação alguma com o ledger.
    expect(rows[0]!.totalCost).toBeNull();
  });

  test("impressions=0 -> ctr é '0.00000' (sem divisão por zero)", async () => {
    const { client } = makeMockClient([
      canonicalRow({ impressions: "0", clicks: "0", conversions: "0" }),
    ]);
    const pool = makeMockPool(client);
    const adapter = new PostgresStatsAdapter(pool);

    const { rows } = await adapter.queryLive({
      tenantId: "tenant-1",
      advertiserId: "42",
    });

    expect(rows[0]!.ctr).toBe("0.00000");
  });

  test("emite BEGIN -> set_config(tenantId) -> SELECT -> COMMIT, na mesma conexão", async () => {
    const { client, queries } = makeMockClient([canonicalRow()]);
    const pool = makeMockPool(client);
    const adapter = new PostgresStatsAdapter(pool);

    await adapter.queryLive({ tenantId: "tenant-xyz", advertiserId: "7" });

    expect(queries[0]?.text).toBe("BEGIN");
    expect(queries[1]?.text).toBe(
      "SELECT set_config('adserver.tenant_id', $1, true)"
    );
    const setConfigEntry = queries[1];
    expect(
      setConfigEntry !== undefined && "values" in setConfigEntry
        ? setConfigEntry.values
        : undefined
    ).toEqual(["tenant-xyz"]);

    const selectIdx = queries.findIndex((q) =>
      q.text.includes("FROM stats.live_kpis")
    );
    expect(selectIdx).toBeGreaterThan(1);

    const commitIdx = queries.findIndex((q) => q.text === "COMMIT");
    expect(commitIdx).toBeGreaterThan(selectIdx);

    const poolWithCalls = pool as unknown as { _connectCalls: number[] };
    expect(poolWithCalls._connectCalls.length).toBe(1);
  });

  test("a query filtra explicitamente por tenant_id (defesa em profundidade, TX-3/CA-1)", async () => {
    const { client, queries } = makeMockClient([canonicalRow()]);
    const pool = makeMockPool(client);
    const adapter = new PostgresStatsAdapter(pool);

    await adapter.queryLive({ tenantId: "tenant-xyz", advertiserId: "7" });

    const selectEntry = queries.find((q) =>
      q.text.includes("FROM stats.live_kpis")
    );
    expect(selectEntry?.text).toContain(
      "lk.tenant_id = NULLIF(current_setting('adserver.tenant_id', true), '')::uuid"
    );
  });

  test("a query filtra por advertiserId via JOIN com config.campaigns ($1)", async () => {
    const { client, queries } = makeMockClient([canonicalRow()]);
    const pool = makeMockPool(client);
    const adapter = new PostgresStatsAdapter(pool);

    await adapter.queryLive({ tenantId: "tenant-xyz", advertiserId: "99" });

    const selectEntry = queries.find((q) =>
      q.text.includes("FROM stats.live_kpis")
    );
    expect(selectEntry?.text).toContain("JOIN config.campaigns c ON c.id = lk.campaign_id");
    expect(selectEntry?.text).toContain("WHERE c.advertiser_id = $1");
    expect(
      selectEntry !== undefined && "values" in selectEntry
        ? selectEntry.values
        : undefined
    ).toEqual(["99"]);
  });

  test("ROLLBACK é emitido e o erro é relançado quando a query falha", async () => {
    const { client, queries } = makeMockClient([]);
    let callCount = 0;
    client.query.mockImpl = async (text: string, values?: unknown[]) => {
      if (values !== undefined) queries.push({ text, values });
      else queries.push({ text });
      callCount++;
      if (callCount === 3) throw new Error("db error simulado");
      return { rows: [] };
    };
    const pool = makeMockPool(client);
    const adapter = new PostgresStatsAdapter(pool);

    await expect(
      adapter.queryLive({ tenantId: "tenant-1", advertiserId: "1" })
    ).rejects.toThrow("db error simulado");

    const rollbackIdx = queries.findIndex((q) => q.text === "ROLLBACK");
    expect(rollbackIdx).toBeGreaterThan(-1);
  });
});

// ---------------------------------------------------------------------------
// 4. Nenhum dado ao vivo -> array vazio (nunca fabrica linha zerada)
// ---------------------------------------------------------------------------

describe("PostgresStatsAdapter.queryLive — ausência de dados", () => {
  test("as_of null (agregação sobre zero linhas) -> { rows: [], asOf: null }", async () => {
    const { client } = makeMockClient([
      {
        impressions: "0",
        clicks: "0",
        conversions: "0",
        as_of: null,
        currency_count: "0",
        currency: null,
      },
    ]);
    const pool = makeMockPool(client);
    const adapter = new PostgresStatsAdapter(pool);

    const result = await adapter.queryLive({
      tenantId: "tenant-1",
      advertiserId: "42",
    });

    expect(result).toEqual({ rows: [], asOf: null });
  });
});

// ---------------------------------------------------------------------------
// 5. Moedas divergentes -> PRECONDITION_FAILED (DA-10)
// ---------------------------------------------------------------------------

describe("PostgresStatsAdapter.queryLive — DA-10 (sem conversão automática de moeda)", () => {
  test("currency_count > 1 -> PRECONDITION_FAILED, nunca escolhe uma moeda arbitrariamente", async () => {
    const { client } = makeMockClient([
      canonicalRow({ currency_count: "2", currency: "BRL" }),
    ]);
    const pool = makeMockPool(client);
    const adapter = new PostgresStatsAdapter(pool);

    await expect(
      adapter.queryLive({ tenantId: "tenant-1", advertiserId: "42" })
    ).rejects.toMatchObject({ code: "PRECONDITION_FAILED" });
    await expect(
      adapter.queryLive({ tenantId: "tenant-1", advertiserId: "42" })
    ).rejects.toBeInstanceOf(TRPCError);
  });
});

// ---------------------------------------------------------------------------
// 6. queryConsolidated: reuso (não cópia) de UnconfiguredStatsAdapter
// ---------------------------------------------------------------------------

describe("PostgresStatsAdapter.queryConsolidated — perfil beta continua sem fonte faturável", () => {
  test("rejeita com PRECONDITION_FAILED — mesma mensagem de UnconfiguredStatsAdapter (reuso, não cópia)", async () => {
    const pool = makeMockPool(makeMockClient([canonicalRow()]).client);
    const adapter = new PostgresStatsAdapter(pool);
    const reference = new UnconfiguredStatsAdapter();

    const [adapterErr, referenceErr] = await Promise.all([
      adapter.queryConsolidated().catch((e: unknown) => e),
      reference.queryConsolidated().catch((e: unknown) => e),
    ]);

    expect(adapterErr).toBeInstanceOf(TRPCError);
    expect((adapterErr as TRPCError).code).toBe("PRECONDITION_FAILED");
    // A MESMA mensagem — prova de reuso, não de duplicação de texto.
    expect((adapterErr as TRPCError).message).toBe(
      (referenceErr as TRPCError).message
    );
  });

  test("queryConsolidated NUNCA consulta o pool (nenhuma query emitida)", async () => {
    const { client, queries } = makeMockClient([canonicalRow()]);
    const pool = makeMockPool(client);
    const adapter = new PostgresStatsAdapter(pool);

    await adapter.queryConsolidated().catch(() => undefined);

    expect(queries).toHaveLength(0);
  });
});

// ---------------------------------------------------------------------------
// 8. Seleção do adapter em bff/src/index.ts — prova por leitura de código-
// fonte (nunca por import: importar index.ts chamaria server.listen(PORT)
// como efeito colateral do módulo, subindo um servidor HTTP de verdade
// durante `npm test`).
// ---------------------------------------------------------------------------

describe("Fiação em bff/src/index.ts — seleção do StatsAdapter (prova por código-fonte)", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const indexPath = join(here, "..", "index.ts");
  const source = readFileSync(indexPath, "utf8");

  test("importa PostgresStatsAdapter de ./adapters/postgres-stats.js", () => {
    expect(source).toContain(
      'import { PostgresStatsAdapter } from "./adapters/postgres-stats.js";'
    );
  });

  test("com pgPool presente, o statsAdapter é PostgresStatsAdapter(pgPool) — ANTES de checar synthetic/unconfigured", () => {
    // pgPool deve ser o PRIMEIRO ramo checado (BFF_PG_DSN presente -> real,
    // como pede a tarefa); syntheticStatsAllowed só decide dentro do ramo
    // "sem pgPool" (comportamento anterior intacto).
    const pickerMatch = source.match(
      /function pickStatsAdapter\([\s\S]*?\n\}/
    );
    expect(pickerMatch).not.toBeNull();
    const picker = pickerMatch![0];
    expect(picker).toMatch(/if\s*\(pool\)\s*return new PostgresStatsAdapter\(pool\);/);
    expect(picker).toContain("syntheticStatsAllowed(");
    expect(picker).toContain("new InMemoryStatsAdapter()");
    expect(picker).toContain("new UnconfiguredStatsAdapter()");
    // O ramo pgPool precisa vir ANTES do ramo synthetic no texto da função.
    expect(picker.indexOf("PostgresStatsAdapter")).toBeLessThan(
      picker.indexOf("syntheticStatsAllowed(")
    );
  });

  test("statsAdapter é atribuído a partir de pickStatsAdapter(pgPool, ...) — reusa o MESMO pgPool (não abre um segundo Pool)", () => {
    expect(source).toMatch(
      /const statsAdapter: StatsAdapter = pickStatsAdapter\(\s*pgPool,/
    );
    // Garantia de que não existe um segundo `new pg.Pool(` (ou createPgPool)
    // fora do já existente (config/payments) — só UMA fonte de Pool no módulo.
    const poolConstructions = (source.match(/createPgPool\(\)/g) ?? []).length;
    expect(poolConstructions).toBe(1);
  });
});
