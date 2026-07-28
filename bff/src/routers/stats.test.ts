/**
 * Testes de routers/stats.ts — onda "perfil beta", correção de escopo do
 * coordenador (Promise.all -> Promise.allSettled).
 *
 * DEFEITO CORRIGIDO: `dashboard` fazia
 *   Promise.all([queryConsolidated(...), queryLive(...)])
 * No perfil beta, queryConsolidated SEMPRE rejeita (não existe fonte
 * consolidada/faturável — DA-7). Com Promise.all, essa rejeição derrubava
 * o dashboard INTEIRO: o "ao vivo" real (que esta mesma onda implementou em
 * PostgresStatsAdapter) nunca chegava à tela.
 *
 * `router.createCaller(ctx)` invoca o procedimento sem subir HTTP — mesmo
 * padrão de routers/config.test.ts.
 */

import { TRPCError } from "@trpc/server";
import { createStatsRouter } from "./stats.js";
import { UnconfiguredStatsAdapter } from "../adapters/unconfigured-stats.js";
import type { StatsAdapter } from "../adapters/stats-adapter.js";
import type { KpiRow } from "../schemas/stats.js";
import type { TrpcContext } from "../lib/context.js";

function ctxFor(tenantId: string): TrpcContext {
  return { tenantId, userId: "test-user" };
}

function liveRow(): KpiRow {
  return {
    periodStart: "2026-07-25T12:00:00.000Z",
    periodEnd: "2026-07-25T12:00:00.000Z",
    source: "live",
    requests: 100,
    impressions: 100,
    clicks: 5,
    conversions: 1,
    inventoryLoss: 0,
    ctr: "0.05000",
    totalCost: { amount: "0.00", currency: "BRL" },
  };
}

class FakeStatsAdapter implements StatsAdapter {
  constructor(
    private readonly opts: {
      consolidated: () => Promise<{ rows: KpiRow[]; asOf: Date | null }>;
      live: () => Promise<{ rows: KpiRow[]; asOf: Date | null }>;
    }
  ) {}

  queryConsolidated(): Promise<{ rows: KpiRow[]; asOf: Date | null }> {
    return this.opts.consolidated();
  }

  queryLive(): Promise<{ rows: KpiRow[]; asOf: Date | null }> {
    return this.opts.live();
  }
}

const QUERY_INPUT = {
  advertiserId: "1",
  from: "2026-07-24T00:00:00.000Z",
  to: "2026-07-25T00:00:00.000Z",
};

describe("stats.dashboard — consolidada indisponível não derruba o ao vivo (Promise.allSettled)", () => {
  test("1. consolidada indisponível (PRECONDITION_FAILED) + live OK -> live populado, consolidated=[], consolidatedUnavailable preenchido", async () => {
    const unconfigured = new UnconfiguredStatsAdapter();
    const adapter = new FakeStatsAdapter({
      consolidated: () => unconfigured.queryConsolidated(),
      live: () =>
        Promise.resolve({
          rows: [liveRow()],
          asOf: new Date("2026-07-25T12:00:00.000Z"),
        }),
    });
    const router = createStatsRouter(adapter);
    const caller = router.createCaller(ctxFor("tenant-1"));

    const result = await caller.dashboard(QUERY_INPUT);

    // Consolidado indisponível: array vazio, NUNCA linhas inventadas (CA-6).
    expect(result.consolidated).toEqual([]);
    expect(result.consolidatedAsOf).toBeNull();

    // Ao vivo chegou intacto — o defeito corrigido é exatamente este caminho.
    expect(result.live).toHaveLength(1);
    expect(result.live[0]?.source).toBe("live");
    expect(result.liveAsOf).toBe("2026-07-25T12:00:00.000Z");

    // consolidated=[] sozinho é ambíguo (zero eventos no período vs.
    // indisponível); consolidatedUnavailable é o que desambigua.
    expect(result.consolidatedUnavailable).not.toBeNull();

    // Reuso, não cópia: a mensagem é EXATAMENTE a de UnconfiguredStatsAdapter
    // (mesma classe usada pela produção via PostgresStatsAdapter.queryConsolidated).
    const reference = await unconfigured
      .queryConsolidated()
      .catch((e: unknown) => e);
    expect(result.consolidatedUnavailable).toBe(
      (reference as TRPCError).message
    );
  });

  test("2. live falhando de verdade (erro de conexão) -> dashboard AINDA propaga o erro (não mascara falha real como 'indisponível')", async () => {
    const adapter = new FakeStatsAdapter({
      consolidated: () => Promise.resolve({ rows: [], asOf: null }),
      live: () => Promise.reject(new Error("conexão com o Postgres perdida")),
    });
    const router = createStatsRouter(adapter);
    const caller = router.createCaller(ctxFor("tenant-1"));

    await expect(caller.dashboard(QUERY_INPUT)).rejects.toThrow(
      "conexão com o Postgres perdida"
    );
  });

  test("ambas as fontes OK -> consolidatedUnavailable é null (sem falso-positivo de indisponibilidade)", async () => {
    const adapter = new FakeStatsAdapter({
      consolidated: () =>
        Promise.resolve({
          rows: [],
          asOf: new Date("2026-07-25T11:00:00.000Z"),
        }),
      live: () =>
        Promise.resolve({
          rows: [liveRow()],
          asOf: new Date("2026-07-25T12:00:00.000Z"),
        }),
    });
    const router = createStatsRouter(adapter);
    const caller = router.createCaller(ctxFor("tenant-1"));

    const result = await caller.dashboard(QUERY_INPUT);

    expect(result.consolidatedUnavailable).toBeNull();
    expect(result.consolidatedAsOf).toBe("2026-07-25T11:00:00.000Z");
  });
});
