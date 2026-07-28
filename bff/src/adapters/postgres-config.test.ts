/**
 * Testes do PostgresConfigAdapter — wave 14 (Fase 3); seção 7 = wave 30.
 *
 * Espelha postgres-payments.test.ts. Sem Postgres real: um mock de Pool/Client
 * registra a sequência de queries emitidas. Prova, na camada de aplicação, as
 * garantias que o teste de RLS (db/config/tests/rls_isolation_test.sql) prova no
 * banco:
 *
 *   1. TX-3 / CA-1 — isolamento por tenant (amostra detalhada):
 *      toda operação abre uma transação e injeta
 *        SELECT set_config('adserver.tenant_id', $1, true)
 *      na MESMA conexão ANTES da query de dados; COMMIT ao fim; ROLLBACK+rethrow
 *      no erro. O tenantId vem do argumento (ctx da sessão), nunca do payload.
 *      Guarda de regressão: nunca "SET LOCAL adserver.tenant_id = $1" (inválido
 *      no driver pg — utility statement não aceita bind params).
 *
 *   2. TX-2 / DA-10 — dinheiro:
 *      campaign.rate é lido como rate::text (nunca Number); rate + currency são
 *      atualizados em PAR (nunca um sem o outro); o valor no fio é Money
 *      {amount:string, currency}, jamais um número JS.
 *
 *   3. DA-11 — a senha do anunciante é gravada como hash scrypt$, nunca plaintext.
 *
 *   4. buildSet — UPDATE parcial pula `undefined` e preserva `null`.
 *
 *   5. notFound — update/delete de id inexistente (0 linhas) → TRPCError NOT_FOUND.
 *
 *   7. TX-3/CA-1 default-deny (wave 30, achado bff-pg-adapter-set-config-not-
 *      default-deny): a seção 1 acima cobria só 2 dos 39 métodos públicos do
 *      adapter por amostragem — um método NOVO que usasse this.pool.query
 *      diretamente (sem passar por withTenant) perdia o GUC de tenant e
 *      nenhum teste via. A seção 7 enumera TODOS os métodos públicos por
 *      reflexão sobre o prototype (não uma lista manual) e afirma a mesma
 *      propriedade — BEGIN → set_config(tenantId) antes de qualquer dado —
 *      para cada um. Um método novo é coberto automaticamente; um método que
 *      esqueça o GUC quebra o teste. O piso da sentinela (seção 7 abaixo)
 *      acompanha a contagem real — ver achado #4 da remediação wave 30.
 */

import { createConfigPool, PostgresConfigAdapter } from "./postgres-config.js";

const HAS_PG_DSN = Boolean(process.env["BFF_PG_DSN"]);

// ---------------------------------------------------------------------------
// Mock de Pool/Client (sem jest.fn — ESM isolatedModules), espelha o padrão de
// postgres-payments.test.ts.
// ---------------------------------------------------------------------------

type QueryRecord = { text: string; values?: unknown[] };

/** Linha canônica superset: cobre as colunas de TODAS as entidades para que
 *  qualquer mapper produza um objeto válido a partir de um RETURNING/SELECT. */
const CANNED_ROW: Record<string, unknown> = {
  id: "100",
  name: "Entidade X",
  login_username: "adv_x",
  is_network: false,
  active: true,
  advertiser_id: "1",
  type: "remnant",
  priority: 5,
  goal_target: "1000",
  goal_metric: "impressions",
  start_at: new Date("2026-01-01T00:00:00.000Z"),
  end_at: new Date("2026-02-01T00:00:00.000Z"),
  pricing_model: "cpm",
  rate: "5.000000",
  currency: "BRL",
  campaign_id: "1",
  creative_type: "image",
  asset_url: "https://cdn.example.com/x.png",
  dest_url: "https://anunciante.example",
  width: 728,
  height: 90,
  url: "https://site.example",
  site_id: "1",
  owner_type: "campaign",
  owner_id: "1",
  vector: "Geo - Country",
  operator: "is",
  value: "BR",
  logical_op: "AND",
  rule_set_id: null,
  order_seq: 0,
  description: null,
  scope: "campaign_total",
  limit_count: "1000",
  reset_interval: null,
  zone_id: "2",
  created_at: new Date("2026-01-01T00:00:00.000Z"),
  updated_at: new Date("2026-01-01T00:00:00.000Z"),
};

type MockQueryFn = {
  (text: string, values?: unknown[]): Promise<{ rows: unknown[]; rowCount: number }>;
  mockImpl: ((text: string, values?: unknown[]) => Promise<{ rows: unknown[]; rowCount: number }>) | undefined;
};

type MockClient = { query: MockQueryFn; release: () => void };

function isControl(text: string): boolean {
  return (
    text === "BEGIN" ||
    text === "COMMIT" ||
    text === "ROLLBACK" ||
    text.startsWith("SELECT set_config")
  );
}

function makeMockClient(opts?: { empty?: boolean; parentInvisible?: boolean }) {
  const queries: QueryRecord[] = [];
  const dataRows = opts?.empty ? [] : [CANNED_ROW];

  const queryFn = (async (text: string, values?: unknown[]) => {
    if (queryFn.mockImpl !== undefined) return queryFn.mockImpl(text, values);
    if (values !== undefined) queries.push({ text, values });
    else queries.push({ text });
    if (isControl(text)) return { rows: [], rowCount: 0 };
    // Guarda de visibilidade de pai (assertParentVisible): sob RLS, um pai de
    // outro tenant retorna 0 linhas (parentInvisible), senão 1.
    if (text.startsWith("SELECT 1 FROM config.")) {
      return opts?.parentInvisible
        ? { rows: [], rowCount: 0 }
        : { rows: [{ "?column?": 1 }], rowCount: 1 };
    }
    return { rows: dataRows, rowCount: dataRows.length };
  }) as MockQueryFn;
  queryFn.mockImpl = undefined;

  const client: MockClient = { query: queryFn, release: () => undefined };
  return { client, queries, queryFn };
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

function lastIndexWhere<T>(arr: T[], pred: (x: T) => boolean): number {
  for (let i = arr.length - 1; i >= 0; i -= 1) if (pred(arr[i]!)) return i;
  return -1;
}

// ---------------------------------------------------------------------------
// 0. Pool factory
// ---------------------------------------------------------------------------

describe("createConfigPool", () => {
  test("retorna um pg.Pool a partir de um DSN (não conecta na construção)", async () => {
    const pool = createConfigPool("postgres://user:pass@localhost:5432/db?sslmode=disable");
    expect(typeof pool.connect).toBe("function");
    expect(typeof pool.query).toBe("function");
    await pool.end();
  });
});

// ---------------------------------------------------------------------------
// 1. TX-3 / CA-1 — injeção de tenant por transação
// ---------------------------------------------------------------------------

describe("TX-3/CA-1 — tenant injetado por transação (set_config)", () => {
  if (HAS_PG_DSN) {
    test.skip("skipped: ambiente com Postgres real — use o teste de integração", () => {});
  }

  test("REGRESSÃO: statement de tenant usa set_config parametrizado, nunca SET ... = $1", async () => {
    const { client, queries } = makeMockClient();
    const adapter = new PostgresConfigAdapter(makeMockPool(client));

    await adapter.listAdvertisers("tenant-guard");

    const tenant = queries[1];
    expect(tenant?.text).toBe("SELECT set_config('adserver.tenant_id', $1, true)");
    expect(tenant?.text).not.toMatch(/^SET\s/i);
    expect(tenant?.text).not.toContain("SET LOCAL adserver.tenant_id");
    expect(tenant?.values).toEqual(["tenant-guard"]);
  });

  test("listAdvertisers: BEGIN → set_config → SELECT → COMMIT na MESMA conexão", async () => {
    const { client, queries } = makeMockClient();
    const pool = makeMockPool(client);
    const adapter = new PostgresConfigAdapter(pool);

    await adapter.listAdvertisers("tenant-aaa");

    expect(queries[0]?.text).toBe("BEGIN");
    expect(queries[1]?.text).toBe("SELECT set_config('adserver.tenant_id', $1, true)");
    expect(queries[1]?.values).toEqual(["tenant-aaa"]);
    const selectIdx = queries.findIndex((q) => q.text.includes("FROM config.advertisers"));
    expect(selectIdx).toBeGreaterThan(1);
    const commitIdx = lastIndexWhere(queries, (q) => q.text === "COMMIT");
    expect(commitIdx).toBeGreaterThan(selectIdx);
    expect((pool as unknown as { _connectCalls: number[] })._connectCalls.length).toBe(1);
  });

  test("createCampaign: injeta tenant ANTES do INSERT e faz COMMIT", async () => {
    const { client, queries } = makeMockClient();
    const adapter = new PostgresConfigAdapter(makeMockPool(client));

    await adapter.createCampaign("tenant-bbb", {
      advertiserId: "1",
      name: "Camp",
      type: "remnant",
      priority: 5,
      goalTarget: 1000,
      goalMetric: "impressions",
      startAt: "2026-01-01T00:00:00.000Z",
      endAt: "2026-02-01T00:00:00.000Z",
      pricingModel: "cpm",
      rateAmount: "5.000000",
      rateCurrency: "BRL",
    });

    expect(queries[0]?.text).toBe("BEGIN");
    expect(queries[1]?.text).toBe("SELECT set_config('adserver.tenant_id', $1, true)");
    const insertIdx = queries.findIndex((q) => q.text.includes("INSERT INTO config.campaigns"));
    expect(insertIdx).toBeGreaterThan(1);
    // tenant_id inserido é o do ctx (1º valor do INSERT), não do payload.
    expect(queries[insertIdx]?.values?.[0]).toBe("tenant-bbb");
    expect(lastIndexWhere(queries, (q) => q.text === "COMMIT")).toBeGreaterThan(insertIdx);
  });

  test("erro na query de dados → ROLLBACK e re-lança (sem COMMIT)", async () => {
    const { client, queries, queryFn } = makeMockClient();
    const adapter = new PostgresConfigAdapter(makeMockPool(client));
    let n = 0;
    queryFn.mockImpl = async (text: string, values?: unknown[]) => {
      n += 1;
      if (values !== undefined) queries.push({ text, values });
      else queries.push({ text });
      // 3ª chamada = a query de dados (após BEGIN, set_config) → falha.
      if (n === 3) throw new Error("boom");
      return { rows: [], rowCount: 0 };
    };

    await expect(adapter.listSites("tenant-x")).rejects.toThrow("boom");
    expect(lastIndexWhere(queries, (q) => q.text === "ROLLBACK")).toBeGreaterThan(1);
    expect(queries.some((q) => q.text === "COMMIT")).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// 2. TX-2 / DA-10 — dinheiro
// ---------------------------------------------------------------------------

describe("TX-2/DA-10 — rate como string, par rate+currency atômico", () => {
  test("SELECT/RETURNING de campanha lê rate::text (nunca float)", async () => {
    const { client, queries } = makeMockClient();
    const adapter = new PostgresConfigAdapter(makeMockPool(client));

    await adapter.getCampaign("t", "42");
    const sel = queries.find((q) => q.text.includes("FROM config.campaigns"));
    expect(sel?.text).toContain("rate::text AS rate");
  });

  test("rate chega ao fio como Money {amount:string,currency}, não Number", async () => {
    const { client } = makeMockClient();
    const adapter = new PostgresConfigAdapter(makeMockPool(client));

    const camp = await adapter.getCampaign("t", "42");
    expect(camp).not.toBeNull();
    expect(typeof camp!.rate).toBe("object");
    expect(camp!.rate.amount).toBe("5.000000");
    expect(camp!.rate.currency).toBe("BRL");
    expect(typeof camp!.rate.amount).toBe("string");
    // nunca um número JS
    expect(typeof (camp!.rate as unknown)).not.toBe("number");
  });

  test("updateCampaign com AMBOS rate+currency → UPDATE inclui os dois", async () => {
    const { client, queries } = makeMockClient();
    const adapter = new PostgresConfigAdapter(makeMockPool(client));

    await adapter.updateCampaign("t", { id: "7", rateAmount: "9.990000", rateCurrency: "USD" });
    const upd = queries.find((q) => q.text.includes("UPDATE config.campaigns"));
    expect(upd?.text).toContain("rate = $");
    expect(upd?.text).toContain("currency = $");
    expect(upd?.values).toContain("9.990000");
    expect(upd?.values).toContain("USD");
  });

  test("updateCampaign com rateAmount SEM rateCurrency → NÃO toca rate nem currency (par atômico)", async () => {
    const { client, queries } = makeMockClient();
    const adapter = new PostgresConfigAdapter(makeMockPool(client));

    await adapter.updateCampaign("t", { id: "7", rateAmount: "9.990000" });
    const upd = queries.find((q) => q.text.includes("UPDATE config.campaigns"));
    expect(upd?.text).not.toContain("rate = $");
    expect(upd?.text).not.toContain("currency = $");
    expect(upd?.values).not.toContain("9.990000");
  });
});

// ---------------------------------------------------------------------------
// 3. DA-11 — senha nunca em plaintext
// ---------------------------------------------------------------------------

describe("DA-11 — senha do anunciante gravada como hash scrypt$, nunca plaintext", () => {
  test("createAdvertiser: 4º valor do INSERT é hash scrypt$…, diferente do plaintext", async () => {
    const { client, queries } = makeMockClient();
    const adapter = new PostgresConfigAdapter(makeMockPool(client));

    const PLAINTEXT = "supersecret-password";
    await adapter.createAdvertiser("t", {
      name: "Anunciante",
      loginUsername: "anunciante_x",
      loginPassword: PLAINTEXT,
      isNetwork: false,
    });

    const ins = queries.find((q) => q.text.includes("INSERT INTO config.advertisers"));
    const hash = ins?.values?.[3];
    expect(typeof hash).toBe("string");
    expect(hash as string).toMatch(/^scrypt\$[0-9a-f]+\$[0-9a-f]+$/);
    expect(hash).not.toBe(PLAINTEXT);
    // o plaintext não aparece em nenhum valor enviado ao banco
    expect(ins?.values?.some((v) => v === PLAINTEXT)).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// 4. buildSet — pula undefined, preserva null
// ---------------------------------------------------------------------------

describe("buildSet — UPDATE parcial", () => {
  test("updateSite só com name → SET name=$2, updated_at; sem url/active", async () => {
    const { client, queries } = makeMockClient();
    const adapter = new PostgresConfigAdapter(makeMockPool(client));

    await adapter.updateSite("t", { id: "5", name: "Novo Nome" });
    const upd = queries.find((q) => q.text.includes("UPDATE config.sites"));
    expect(upd?.text).toContain("name = $2");
    expect(upd?.text).toContain("updated_at = now()");
    expect(upd?.text).not.toContain("url = $");
    expect(upd?.text).not.toContain("active = $");
    expect(upd?.values).toEqual(["5", "Novo Nome"]);
  });

  test("updateCampaign com goalTarget:null → buildSet PRESERVA null (limpar meta)", async () => {
    const { client, queries } = makeMockClient();
    const adapter = new PostgresConfigAdapter(makeMockPool(client));

    await adapter.updateCampaign("t", { id: "9", goalTarget: null });
    const upd = queries.find((q) => q.text.includes("UPDATE config.campaigns"));
    expect(upd?.text).toContain("goal_target = $2");
    expect(upd?.values).toEqual(["9", null]);
  });

  test("updateBanner só com name → não toca asset_url/dest_url", async () => {
    const { client, queries } = makeMockClient();
    const adapter = new PostgresConfigAdapter(makeMockPool(client));

    await adapter.updateBanner("t", { id: "9", name: "Novo" });
    const upd = queries.find((q) => q.text.includes("UPDATE config.banners"));
    expect(upd?.text).toContain("name = $2");
    // asset_url/dest_url aparecem no RETURNING (BANNER_COLS), mas NÃO no SET.
    expect(upd?.text).not.toContain("asset_url = $");
    expect(upd?.text).not.toContain("dest_url = $");
    expect(upd?.values).toEqual(["9", "Novo"]);
  });
});

// ---------------------------------------------------------------------------
// 6. HIGH — guarda de owner cross-tenant (assertParentVisible)
//    Fecha o vetor: A grava cap/banner/rule apontando p/ pai do tenant B.
// ---------------------------------------------------------------------------

describe("HIGH — guarda de parent/owner cross-tenant", () => {
  test("createCap com ownerId de outro tenant (pai invisível sob RLS) → NOT_FOUND, sem INSERT", async () => {
    const { client, queries } = makeMockClient({ parentInvisible: true });
    const adapter = new PostgresConfigAdapter(makeMockPool(client));

    await expect(
      adapter.createCap("tenant-A", {
        ownerType: "campaign",
        ownerId: "999",
        scope: "campaign_total",
        limitCount: 1,
        resetInterval: null,
      })
    ).rejects.toMatchObject({ code: "NOT_FOUND" });

    // a guarda consultou o pai sob RLS, ANTES de qualquer INSERT
    expect(queries.some((q) => q.text.startsWith("SELECT 1 FROM config.campaigns"))).toBe(true);
    expect(queries.some((q) => q.text.includes("INSERT INTO config.caps"))).toBe(false);
  });

  test("createBanner com campaignId de outro tenant → NOT_FOUND, sem INSERT", async () => {
    const { client, queries } = makeMockClient({ parentInvisible: true });
    const adapter = new PostgresConfigAdapter(makeMockPool(client));

    await expect(
      adapter.createBanner("tenant-A", {
        campaignId: "999",
        name: "x",
        creativeType: "image",
        assetUrl: "https://cdn.example/x.png",
        destUrl: "https://z.example",
        width: 728,
        height: 90,
      })
    ).rejects.toMatchObject({ code: "NOT_FOUND" });
    expect(queries.some((q) => q.text.includes("INSERT INTO config.banners"))).toBe(false);
  });

  test("createCampaign com advertiser VISÍVEL → guarda passa e INSERT ocorre", async () => {
    const { client, queries } = makeMockClient();
    const adapter = new PostgresConfigAdapter(makeMockPool(client));

    await adapter.createCampaign("tenant-A", {
      advertiserId: "1",
      name: "c",
      type: "remnant",
      priority: 5,
      goalTarget: null,
      goalMetric: null,
      startAt: "2026-01-01T00:00:00.000Z",
      endAt: "2026-02-01T00:00:00.000Z",
      pricingModel: "cpm",
      rateAmount: "5.000000",
      rateCurrency: "BRL",
    });

    expect(queries.some((q) => q.text.startsWith("SELECT 1 FROM config.advertisers"))).toBe(true);
    expect(queries.some((q) => q.text.includes("INSERT INTO config.campaigns"))).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// 7. TX-3/CA-1 default-deny — TODOS os métodos públicos, por reflexão
//    (achado wave 30: bff-pg-adapter-set-config-not-default-deny)
// ---------------------------------------------------------------------------

describe("TX-3/CA-1 default-deny — reflexão sobre TODOS os métodos públicos do adapter", () => {
  // Allowlist de EXCLUSÃO explícita e comentada — nunca uma lista de inclusão
  // manual dos métodos cobertos (isso reintroduziria o mesmo ponto-cego).
  // Cada exceção abaixo é um helper PRIVADO de implementação, não uma
  // operação de dados chamável pelos routers tRPC:
  const PRIVATE_HELPER_ALLOWLIST = new Set<string>([
    // withTenant É o próprio mecanismo (BEGIN + set_config + fn + COMMIT).
    // Chamá-lo diretamente não prova nem refuta nada sobre um método público
    // esquecer o guard — ele é o guard sendo testado.
    "withTenant",
    // assertParentVisible roda DENTRO de uma transação já aberta por
    // withTenant — recebe PoolClient como 1º argumento (não tenantId), então
    // não se encaixa no padrão (tenantId, ...) exercido abaixo. Sua cobertura
    // vem da seção 6 (HIGH — guarda de parent/owner cross-tenant).
    "assertParentVisible",
  ]);

  const proto = PostgresConfigAdapter.prototype as unknown as Record<string, unknown>;
  const allMethodNames = Object.getOwnPropertyNames(proto).filter(
    (m) => m !== "constructor" && typeof proto[m] === "function"
  );
  const publicDataMethods = allMethodNames.filter(
    (m) => !PRIVATE_HELPER_ALLOWLIST.has(m)
  );

  // Sentinela anti-vazio: se a reflexão parar de enumerar métodos (refactor
  // da classe, mudança de prototype chain), publicDataMethods cai para 0 e
  // os test.each abaixo simplesmente não geram testes — falso-verde silencioso.
  // Esta asserção incondicional pega isso.
  //
  // LOW (wave 30 remediação, achado #4 — "sentinelas com folga larga
  // demais"): o piso era 35 contra 38 métodos reais, permitindo remover até
  // 3 métodos sem alarme. Apertado para o número REAL de hoje (39, após
  // getDeliveryRule) — `>=` continua permitindo crescimento legítimo (um
  // método novo não quebra o piso), mas QUALQUER encolhimento agora falha.
  test("sentinela: a reflexão enumerou pelo menos 39 métodos públicos de dados (piso = contagem real hoje)", () => {
    expect(publicDataMethods.length).toBeGreaterThanOrEqual(39);
  });

  test.each(publicDataMethods)(
    "%s: abre transação com BEGIN -> set_config(tenantId) ANTES de qualquer outra query",
    async (methodName) => {
      const { client, queries } = makeMockClient();
      const adapter = new PostgresConfigAdapter(makeMockPool(client));
      const TENANT = "default-deny-probe-tenant";

      const method = (
        adapter as unknown as Record<string, (...args: unknown[]) => Promise<unknown>>
      )[methodName]!;

      // Argumentos genéricos: tenantId sempre em 1º lugar; os demais são
      // placeholders permissivos. Todo método público do adapter só acessa
      // os argumentos posteriores DENTRO do callback async passado a
      // withTenant — ou seja, depois que BEGIN/set_config já foram emitidos
      // (ver postgres-config.ts: `return this.withTenant(tenantId, async (c)
      // => { ...usa input/opts/id aqui... })`). Se o método rejeitar depois
      // (ex.: NOT_FOUND por causa do placeholder), não importa — o que este
      // teste prova é que o GUC de tenant é injetado ANTES de qualquer
      // acesso a dados, para TODO método, sempre.
      // .call(adapter, ...): método extraído do prototype por reflexão perde
      // o `this` — sem bind explícito, `this.withTenant(...)` dentro do
      // método quebraria com TypeError, não com o comportamento real testado.
      await method.call(adapter, TENANT, {}, {}).catch(() => undefined);

      expect(queries[0]?.text).toBe("BEGIN");
      expect(queries[1]?.text).toBe("SELECT set_config('adserver.tenant_id', $1, true)");
      expect(queries[1]?.values).toEqual([TENANT]);
    }
  );
});

// ---------------------------------------------------------------------------
// 5. notFound — 0 linhas em update/delete
// ---------------------------------------------------------------------------

describe("notFound — id inexistente (0 linhas) → TRPCError NOT_FOUND", () => {
  async function expectNotFound(p: Promise<unknown>): Promise<void> {
    await expect(p).rejects.toMatchObject({ code: "NOT_FOUND" });
  }

  test("updateAdvertiser inexistente rejeita NOT_FOUND", async () => {
    const { client } = makeMockClient({ empty: true });
    const adapter = new PostgresConfigAdapter(makeMockPool(client));
    await expectNotFound(
      adapter.updateAdvertiser("t", { id: "404", name: "x" })
    );
  });

  test("deleteCampaign inexistente rejeita NOT_FOUND", async () => {
    const { client } = makeMockClient({ empty: true });
    const adapter = new PostgresConfigAdapter(makeMockPool(client));
    await expectNotFound(adapter.deleteCampaign("t", "404"));
  });

  test("deleteZone inexistente rejeita NOT_FOUND", async () => {
    const { client } = makeMockClient({ empty: true });
    const adapter = new PostgresConfigAdapter(makeMockPool(client));
    await expectNotFound(adapter.deleteZone("t", "404"));
  });
});
