/**
 * Testes de wiring de tenantProcedure/ensureTenant (CA-1 / TX-3).
 *
 * O predicado "tenant_id ausente => UNAUTHORIZED" (context.ts) já é coberto
 * em context.test.ts. Este arquivo cobre a segunda camada da fronteira de
 * ACL: o middleware ensureTenant aplicado a QUALQUER procedimento de dados
 * via tenantProcedure — e o fato de que o tenant_id usado pelos resolvers
 * é SEMPRE ctx.tenantId (sessão), nunca um campo equivalente injetado no
 * input do cliente.
 *
 * Sem estes testes, remover o `if (!ctx.tenantId) throw ...` de ensureTenant
 * (ou trocar tenantProcedure por publicProcedure em um router de dados)
 * deixa o bff-ci verde (achado #26).
 *
 * COBERTURA (wave 30, achado bff-acl-guard-scoped-to-config-router-only):
 * os testes originais abaixo (describe "tenantProcedure — fronteira de ACL
 * server-side (CA-1)") só instanciam createConfigRouter — trocar
 * tenantProcedure por publicProcedure em createPaymentsRouter ou
 * createStatsRouter (routers de dados de superfície financeira/estatística)
 * não quebrava NADA aqui. O describe "ACL default-deny" no final do arquivo
 * fecha esse buraco: monta o MESMO appRouter de produção (bff/src/index.ts,
 * cfg+stats+copilot+payments) por reflexão sobre `_def.procedures` — sem
 * lista manual — e afirma UNAUTHORIZED sem tenant_id para TODO procedimento.
 * Um router NOVO ou uma troca de procedure em QUALQUER router quebra o
 * teste automaticamente.
 */

import { z } from "zod";
import { TRPCError } from "@trpc/server";
import { router, publicProcedure } from "./trpc.js";
import { createConfigRouter } from "../routers/config.js";
import { createStatsRouter } from "../routers/stats.js";
import { createCopilotRouter } from "../routers/copilot.js";
import { createPaymentsRouter } from "../routers/payments.js";
import { InMemoryConfigAdapter } from "../adapters/in-memory-config.js";
import { InMemoryStatsAdapter } from "../adapters/in-memory-stats.js";
import { InMemoryPaymentsAdapter } from "../adapters/in-memory-payments.js";
import type { TrpcContext } from "./context.js";
import type { CreateAdvertiserInput } from "../schemas/config.js";

function ctxFor(tenantId: string): TrpcContext {
  return { tenantId, userId: "test-user" };
}

// NOTA: InMemoryConfigAdapter usa um store global keyed por tenantId (não
// por instância — ver adapters/in-memory-config.ts). Por isso cada teste
// abaixo usa UUIDs de tenant EXCLUSIVOS (nunca reaproveitados entre testes
// deste arquivo), do mesmo jeito que in-memory-config.test.ts já faz —
// evita contaminação cross-test, não cross-tenant (isso é o que os testes
// de isolamento verificam).

describe("tenantProcedure — fronteira de ACL server-side (CA-1)", () => {
  test("procedimento de dados sem tenant_id no contexto -> UNAUTHORIZED (nunca executa o adapter)", async () => {
    const adapter = new InMemoryConfigAdapter();
    const router = createConfigRouter(adapter);
    // Simula um contexto quebrado (ex.: bug upstream em createContext) chegando
    // ao resolver sem tenant_id resolvido — ensureTenant deve barrar aqui.
    const caller = router.createCaller(ctxFor(""));

    await expect(caller.advertiser.list()).rejects.toMatchObject({
      code: "UNAUTHORIZED",
    });
  });

  test("procedimento de mutação sem tenant_id no contexto -> UNAUTHORIZED", async () => {
    const adapter = new InMemoryConfigAdapter();
    const router = createConfigRouter(adapter);
    const caller = router.createCaller(ctxFor(""));

    await expect(
      caller.advertiser.create({
        name: "Nunca deveria persistir",
        loginUsername: "x@example.com",
        loginPassword: "senha12345",
        isNetwork: false,
      })
    ).rejects.toMatchObject({ code: "UNAUTHORIZED" });
  });

  test("tenant_id vem exclusivamente de ctx (sessão) — um campo tenantId contrabandeado no input é ignorado", async () => {
    const tenantA = "33333333-3333-3333-3333-333333333333";
    const tenantB = "44444444-4444-4444-4444-444444444444";
    const adapter = new InMemoryConfigAdapter();
    const router = createConfigRouter(adapter);
    const callerA = router.createCaller(ctxFor(tenantA));
    const callerB = router.createCaller(ctxFor(tenantB));

    // Payload malicioso: cliente tenta sobrescrever o tenant via um campo
    // extra que não existe no schema. Se algum resolver algum dia ler
    // input.tenantId em vez de ctx.tenantId, este teste falha.
    const maliciousInput: CreateAdvertiserInput & { tenantId: string } = {
      name: "Anunciante Malicioso",
      loginUsername: "attacker@example.com",
      loginPassword: "senha12345",
      isNetwork: false,
      tenantId: tenantB,
    };

    await callerA.advertiser.create(maliciousInput);

    const listA = await callerA.advertiser.list();
    const listB = await callerB.advertiser.list();

    expect(listA.length).toBe(1);
    expect(listB.length).toBe(0);
  });

  test("dois tenants distintos com contexto válido não vazam dados um do outro (fronteira intacta com o guard ativo)", async () => {
    const tenantA = "55555555-5555-5555-5555-555555555555";
    const tenantB = "66666666-6666-6666-6666-666666666666";
    const adapter = new InMemoryConfigAdapter();
    const router = createConfigRouter(adapter);
    const callerA = router.createCaller(ctxFor(tenantA));
    const callerB = router.createCaller(ctxFor(tenantB));

    await callerA.advertiser.create({
      name: "Alpha",
      loginUsername: "alpha@example.com",
      loginPassword: "senha12345",
      isNetwork: false,
    });
    await callerB.advertiser.create({
      name: "Beta",
      loginUsername: "beta@example.com",
      loginPassword: "senha12345",
      isNetwork: false,
    });

    const listA = await callerA.advertiser.list();
    const listB = await callerB.advertiser.list();

    expect(listA.map((a) => a.name)).toEqual(["Alpha"]);
    expect(listB.map((b) => b.name)).toEqual(["Beta"]);
  });
});

// ---------------------------------------------------------------------------
// ACL default-deny — TODOS os routers do appRouter, por reflexão
// (achado wave 30: bff-acl-guard-scoped-to-config-router-only)
// ---------------------------------------------------------------------------

/**
 * Navega um caller tRPC por um caminho com pontos (ex.: "payments.balances")
 * e devolve a função de procedimento correspondente.
 */
function getProcedureAtPath(
  caller: unknown,
  path: string
): (input?: unknown) => Promise<unknown> {
  const segments = path.split(".");
  let cur: unknown = caller;
  for (const seg of segments) {
    // O caller tRPC (createRecursiveProxy) é um Proxy sobre uma FUNÇÃO, não
    // um objeto plano — sub-roteadores/procedimentos aparecem como
    // propriedades desse proxy função. Aceita ambos "object" e "function".
    if (cur === null || (typeof cur !== "object" && typeof cur !== "function")) {
      throw new Error(`Caminho de procedimento inválido: ${path}`);
    }
    cur = (cur as Record<string, unknown>)[seg];
  }
  if (typeof cur !== "function") {
    throw new Error(`Procedimento não encontrado no caller: ${path}`);
  }
  return cur as (input?: unknown) => Promise<unknown>;
}

describe("ACL default-deny — TODO procedimento de dados do appRouter exige tenantProcedure", () => {
  // Monta a MESMA composição de produção (bff/src/index.ts: cfg+stats+
  // copilot+payments) sem os efeitos colaterais de módulo de index.ts (que
  // abre um HTTP server real ao ser importado — inadequado para um teste).
  const appRouter = router({
    cfg: createConfigRouter(new InMemoryConfigAdapter()),
    stats: createStatsRouter(new InMemoryStatsAdapter()),
    copilot: createCopilotRouter(),
    payments: createPaymentsRouter(new InMemoryPaymentsAdapter()),
  });

  // Allowlist de EXCEÇÕES explícitas e comentadas — procedimentos
  // legitimamente públicos por design (ex.: um futuro healthcheck sem
  // sessão). Vazia hoje: TODO procedimento de dados do console exige
  // tenant_id de sessão. NUNCA vire uma lista de inclusão manual dos
  // procedimentos cobertos — isso reintroduziria o mesmo ponto-cego que
  // este teste corrige (achado #26 / wave 30).
  const PUBLIC_ALLOWLIST = new Set<string>([]);

  const flatProcedures = (
    appRouter._def as unknown as { procedures: Record<string, unknown> }
  ).procedures;
  const allPaths = Object.keys(flatProcedures);
  const guardedPaths = allPaths.filter((p) => !PUBLIC_ALLOWLIST.has(p));

  // Sentinela anti-vazio: se a reflexão sobre `_def.procedures` parar de
  // enumerar (mudança de versão do tRPC, refactor de composição do router),
  // allPaths cai para 0 e os test.each abaixo não geram teste algum —
  // falso-verde silencioso. Esta asserção incondicional pega isso. Hoje:
  // 38 (cfg) + 1 (stats) + 4 (copilot) + 3 (payments) = 46.
  //
  // LOW (wave 30 remediação, achado #4 — "sentinelas com folga larga
  // demais"): o piso era 40 contra 46 procedimentos reais, permitindo
  // remover até 6 procedimentos sem alarme. Apertado para a contagem REAL de
  // hoje — `>=` continua permitindo crescimento legítimo (um router/
  // procedimento novo não quebra o piso), mas QUALQUER encolhimento agora
  // falha.
  test("sentinela: o appRouter expõe pelo menos 46 procedimentos (piso = contagem real hoje: cfg+stats+copilot+payments)", () => {
    expect(allPaths.length).toBeGreaterThanOrEqual(46);
  });

  test.each(guardedPaths)(
    "%s: sem tenant_id no contexto -> UNAUTHORIZED (nenhum router de dados escapa ao guard)",
    async (path) => {
      const caller = appRouter.createCaller(ctxFor(""));
      const fn = getProcedureAtPath(caller, path);
      await expect(fn(undefined)).rejects.toMatchObject({ code: "UNAUTHORIZED" });
    }
  );
});

// ---------------------------------------------------------------------------
// errorFormatter — redação fail-closed de erros internos (31ª onda, TX-5/DA-11)
//
// Achado bff-trpc-error-shape-leak: `initTRPC...create()` era chamado SEM
// errorFormatter. Um erro que não é TRPCError (ex.: driver `pg`) é embrulhado
// pelo tRPC em INTERNAL_SERVER_ERROR PRESERVANDO a mensagem original, que
// chegava ao browser — publicando nomes de tabela/constraint e fragmentos de
// valor citados pelo Postgres.
//
// Estes testes são de MUTAÇÃO: remover o errorFormatter de lib/trpc.ts faz o
// primeiro teste FALHAR (a mensagem crua volta a vazar).
// ---------------------------------------------------------------------------
describe("errorFormatter — nenhum detalhe interno chega ao cliente", () => {
  const SEGREDO = 'relation "config.campaigns" does not exist';

  /** Router mínimo que lança um erro NÃO-TRPCError (simula falha do driver pg). */
  const leakyRouter = router({
    boom: publicProcedure.query(() => {
      throw new Error(SEGREDO);
    }),
    /** TRPCError deliberado: mensagem escrita para o usuário, deve passar. */
    denied: publicProcedure.query(() => {
      throw new TRPCError({
        code: "FORBIDDEN",
        message: "Acesso negado a esta campanha.",
      });
    }),
    /** Validação Zod: BAD_REQUEST com cause=ZodError (NÃO pode ser redigido). */
    validated: publicProcedure
      .input(z.object({ dia: z.number().int() }))
      .query(() => "ok"),
    /** TRPCError deliberado COM código interno (padrão de routers/copilot.ts). */
    upstream: publicProcedure.query(() => {
      throw new TRPCError({
        code: "INTERNAL_SERVER_ERROR",
        message: "Falha ao processar aprovação HITL. ID: abc-123",
      });
    }),
  });

  /** Extrai a shape serializada que o cliente REALMENTE receberia. */
  async function shapeOf(
    path: "boom" | "denied" | "upstream" | "validated",
    input?: unknown,
  ) {
    const caller = leakyRouter.createCaller({ tenantId: "t", userId: "u" });
    try {
      await (caller[path] as (i?: unknown) => Promise<unknown>)(input);
      throw new Error("esperava rejeição");
    } catch (err) {
      const e = err as TRPCError;
      return leakyRouter._def._config.errorFormatter({
        error: e,
        type: "query",
        path,
        input: undefined,
        ctx: undefined,
        shape: {
          message: e.message,
          code: -32603,
          data: { code: e.code, httpStatus: 500, path, stack: e.stack },
        },
      } as never) as { message: string; data?: Record<string, unknown> };
    }
  }

  test("erro NÃO-TRPCError (driver pg) -> mensagem crua NUNCA chega ao cliente", async () => {
    const shape = await shapeOf("boom");

    expect(shape.message).not.toContain(SEGREDO);
    expect(shape.message).not.toContain("config.campaigns");
    expect(JSON.stringify(shape)).not.toContain("config.campaigns");
    expect(shape.message).toMatch(/Erro interno\. ID de correlação: /);
  });

  test("erro NÃO-TRPCError -> stack nunca é serializada para o cliente", async () => {
    const shape = await shapeOf("boom");
    expect(shape.data?.["stack"]).toBeUndefined();
  });

  test("erro NÃO-TRPCError -> correlationId presente para rastreio no log do servidor", async () => {
    const shape = await shapeOf("boom");
    expect(typeof shape.data?.["correlationId"]).toBe("string");
    expect(shape.message).toContain(String(shape.data?.["correlationId"]));
  });

  test("TRPCError deliberado (FORBIDDEN) -> mensagem para o usuário é PRESERVADA", async () => {
    const shape = await shapeOf("denied");
    expect(shape.message).toBe("Acesso negado a esta campanha.");
  });

  test("TRPCError deliberado com código INTERNAL_SERVER_ERROR -> mensagem própria preservada (padrão do copiloto)", async () => {
    const shape = await shapeOf("upstream");
    expect(shape.message).toBe("Falha ao processar aprovação HITL. ID: abc-123");
  });

  // -------------------------------------------------------------------------
  // REGRESSÃO pega pela revisão adversarial do próprio diff da 31ª onda.
  //
  // A primeira versão do errorFormatter discriminava por `cause === undefined`.
  // Erro de validação Zod chega como BAD_REQUEST *com* cause=ZodError
  // (verificado contra @trpc/server 11.0.0), então era REDIGIDO: o anunciante
  // digitava uma data inválida e recebia "Erro interno. ID de correlação: ..."
  // em vez da mensagem de campo. O discriminador passou a exigir
  // code === INTERNAL_SERVER_ERROR **E** cause !== undefined.
  //
  // MUTAÇÃO: voltar o discriminador para `error.cause === undefined` faz este
  // teste FALHAR.
  // -------------------------------------------------------------------------
  test("erro de validação Zod (BAD_REQUEST com cause) -> mensagem de campo PRESERVADA, nunca redigida", async () => {
    const shape = await shapeOf("validated", { dia: "nao-e-numero" });

    expect(shape.message).not.toMatch(/Erro interno\. ID de correlação/);
    expect(shape.data?.["correlationId"]).toBeUndefined();
    // A mensagem do Zod tem de sobreviver para o formulário mapear no campo.
    expect(shape.message).toContain("dia");
  });
});
