/**
 * Testes do fail-closed de estatísticas (31ª onda).
 *
 * ACHADO: bff/src/index.ts instanciava InMemoryStatsAdapter (que usa
 * Math.random()) INCONDICIONALMENTE, sem o ramo condicional que config e
 * payments já tinham. O /dashboard exibia números inventados rotulados pela UI
 * como "Consolidado ≤1h" / "faturável" — ficção apresentada como base de
 * cobrança, em qualquer ambiente.
 *
 * MUTAÇÃO: trocar `nodeEnv !== "production"` por `true` em
 * syntheticStatsAllowed faz o teste de produção abaixo FALHAR.
 */

import { TRPCError } from "@trpc/server";
import {
  UnconfiguredStatsAdapter,
  syntheticStatsAllowed,
} from "./unconfigured-stats.js";

describe("syntheticStatsAllowed — quem pode servir número sintético", () => {
  test("dev/test declarados -> stub sintético permitido (local e jest preservados)", () => {
    expect(syntheticStatsAllowed("development", undefined)).toBe(true);
    expect(syntheticStatsAllowed("test", undefined)).toBe(true);
  });

  // Regressão pega pela revisão adversarial do próprio diff da 31ª onda
  // (STATS-FAILOPEN-NODE-ENV). A primeira versão usava
  // `nodeEnv !== "production"`, então NODE_ENV ausente PERMITIA dado sintético.
  // Não há Dockerfile nem script de start do BFF que exporte NODE_ENV neste
  // repo: o ambiente real mais provável é justamente o ambíguo.
  //
  // MUTAÇÃO: voltar para `if (nodeEnv !== "production") return true` faz este
  // teste FALHAR.
  test("NODE_ENV ausente ou desconhecido -> stub PROIBIDO (allowlist, não denylist)", () => {
    expect(syntheticStatsAllowed(undefined, undefined)).toBe(false);
    expect(syntheticStatsAllowed("", undefined)).toBe(false);
    expect(syntheticStatsAllowed("staging", undefined)).toBe(false);
    expect(syntheticStatsAllowed("prod", undefined)).toBe(false);
  });

  test("opt-in explícito vence em qualquer ambiente ambíguo", () => {
    expect(syntheticStatsAllowed(undefined, "1")).toBe(true);
    expect(syntheticStatsAllowed("staging", "1")).toBe(true);
  });

  test("produção SEM opt-in -> stub sintético PROIBIDO (nunca fabrica número faturável)", () => {
    expect(syntheticStatsAllowed("production", undefined)).toBe(false);
    expect(syntheticStatsAllowed("production", "")).toBe(false);
    expect(syntheticStatsAllowed("production", "0")).toBe(false);
    expect(syntheticStatsAllowed("production", "true")).toBe(false);
    expect(syntheticStatsAllowed("production", "yes")).toBe(false);
  });

  test("produção COM opt-in explícito ALLOW_SYNTHETIC_STATS=1 -> permitido (demo/staging)", () => {
    expect(syntheticStatsAllowed("production", "1")).toBe(true);
  });
});

describe("UnconfiguredStatsAdapter — falha visível em vez de dado inventado", () => {
  test("queryConsolidated rejeita com PRECONDITION_FAILED (nunca devolve linhas)", async () => {
    const adapter = new UnconfiguredStatsAdapter();
    await expect(adapter.queryConsolidated()).rejects.toBeInstanceOf(TRPCError);
    await expect(adapter.queryConsolidated()).rejects.toMatchObject({
      code: "PRECONDITION_FAILED",
    });
  });

  test("queryLive rejeita com PRECONDITION_FAILED (nunca devolve linhas)", async () => {
    const adapter = new UnconfiguredStatsAdapter();
    await expect(adapter.queryLive()).rejects.toMatchObject({
      code: "PRECONDITION_FAILED",
    });
  });

  test("a mensagem de erro não vaza detalhe interno, mas diz o que fazer", async () => {
    const adapter = new UnconfiguredStatsAdapter();
    // Sem expect.stringContaining: o matcher assimétrico é tipado como `any`
    // e o bff-lint (type-aware, max-warnings 0) rejeita.
    const err = await adapter.queryLive().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(TRPCError);
    expect((err as TRPCError).message).toContain("ALLOW_SYNTHETIC_STATS");
    expect((err as TRPCError).message).toContain("ClickHouse");
  });
});
