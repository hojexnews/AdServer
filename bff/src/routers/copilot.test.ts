/**
 * Testes de makeInternalHeaders — assinatura HMAC BFF→copiloto (TX-3).
 *
 * O BFF assina HMAC-SHA256(`${tenantId}:${timestamp}`, COPILOT_INTERNAL_SECRET)
 * para que o copiloto Python só aceite requests do BFF autenticado
 * (services/copilot/tests/test_security.py recomputa a MESMA fórmula do
 * lado do copiloto). Sem teste no lado do BFF, uma mutação que quebra o
 * formato da mensagem assinada (ex.: remover o `:${timestamp}`, deixando
 * de vincular o anti-replay) não é pega por nenhum gate (achado #15).
 *
 * COPILOT_INTERNAL_SECRET é lido uma única vez no TOPO do módulo
 * copilot.ts — por isso cada cenário abaixo faz `jest.resetModules()`
 * seguido de um import() dinâmico DEPOIS de ajustar process.env, para
 * garantir uma instância nova do módulo capturando o valor certo.
 */

import { createHmac } from "crypto";
import { jest } from "@jest/globals";

/**
 * Achado #14 (false-red, HIGH): sob o preset ESM do ts-jest, o global `jest`
 * NÃO é injetado automaticamente no escopo do módulo — só o runner CJS legado
 * injeta esse global implicitamente. Sem este import, `jest.resetModules()`
 * abaixo lança `ReferenceError: jest is not defined`, e a suite inteira
 * aborta por esse erro ANTES de rodar qualquer assert (bff-ci fica vermelho
 * de forma não-discriminante: falha por erro de ambiente, não por regressão
 * real na assinatura HMAC). `@jest/globals` expõe o mesmo objeto `jest` de
 * forma explícita e resolvível pelo resolver ESM.
 */

async function freshMakeInternalHeaders(): Promise<
  typeof import("./copilot.js")["makeInternalHeaders"]
> {
  // Reseta o registro de módulos do jest e reimporta copilot.ts, que lê
  // COPILOT_INTERNAL_SECRET no topo do módulo. (O cache-busting por query
  // string — "./copilot.js?x=N" — não é resolvível pelo resolver do jest.)
  jest.resetModules();
  const mod = await import("./copilot.js");
  return mod.makeInternalHeaders;
}

describe("makeInternalHeaders — assinatura HMAC BFF→copilot (TX-3)", () => {
  const tenantId = "11111111-1111-1111-1111-111111111111";

  test("com COPILOT_INTERNAL_SECRET definido: assina HMAC-SHA256(`${tenantId}:${timestamp}`) — determinístico e verificável", async () => {
    const secret = "unit-test-copilot-internal-secret-0123456789";
    const prev = process.env["COPILOT_INTERNAL_SECRET"];
    process.env["COPILOT_INTERNAL_SECRET"] = secret;

    const makeInternalHeaders = await freshMakeInternalHeaders();
    const headers = makeInternalHeaders(tenantId);

    if (prev === undefined) delete process.env["COPILOT_INTERNAL_SECRET"];
    else process.env["COPILOT_INTERNAL_SECRET"] = prev;

    expect(headers["X-Tenant-ID"]).toBe(tenantId);
    expect(headers["X-Internal-Timestamp"]).toMatch(/^\d+$/);

    // Recomputa a assinatura de forma independente (mesma fórmula usada
    // por services/copilot/tests/test_security.py do lado do copiloto:
    // f"{tenant_id}:{timestamp}") e compara — prova que a assinatura
    // emitida pelo BFF é a HMAC real, não um placeholder.
    const expectedMessage = `${tenantId}:${headers["X-Internal-Timestamp"]}`;
    const expectedSignature = createHmac("sha256", secret)
      .update(expectedMessage)
      .digest("hex");

    expect(headers["X-Internal-Signature"]).toBe(expectedSignature);
    // sha256 hex digest tem sempre 64 caracteres — nunca "dev-skip" quando
    // o segredo está configurado.
    expect(headers["X-Internal-Signature"]).toHaveLength(64);
    expect(headers["X-Internal-Signature"]).not.toBe("dev-skip");
  });

  test("assinatura é determinística: mesmo tenantId no mesmo segundo produz a mesma assinatura", async () => {
    const secret = "unit-test-copilot-internal-secret-determinism";
    const prev = process.env["COPILOT_INTERNAL_SECRET"];
    process.env["COPILOT_INTERNAL_SECRET"] = secret;

    const makeInternalHeaders = await freshMakeInternalHeaders();

    // Recalcula manualmente para o MESMO timestamp que o header emitido —
    // garante que chamadas repetidas dentro do mesmo segundo Unix
    // produzem headers idênticos (a assinatura depende só de tenantId e
    // timestamp, nunca de estado aleatório).
    const h1 = makeInternalHeaders(tenantId);
    const h2 = makeInternalHeaders(tenantId);

    if (prev === undefined) delete process.env["COPILOT_INTERNAL_SECRET"];
    else process.env["COPILOT_INTERNAL_SECRET"] = prev;

    if (h1["X-Internal-Timestamp"] === h2["X-Internal-Timestamp"]) {
      expect(h1["X-Internal-Signature"]).toBe(h2["X-Internal-Signature"]);
    } else {
      // Cruzou a fronteira de segundo Unix (raro, mas possível) — ainda
      // assim cada assinatura deve bater com a HMAC recomputada.
      expect(h1["X-Internal-Signature"]).toBe(
        createHmac("sha256", secret)
          .update(`${tenantId}:${h1["X-Internal-Timestamp"]}`)
          .digest("hex")
      );
    }
  });

  test("tenants diferentes no mesmo instante produzem assinaturas diferentes (sem colisão trivial)", async () => {
    const secret = "unit-test-copilot-internal-secret-distinct";
    const prev = process.env["COPILOT_INTERNAL_SECRET"];
    process.env["COPILOT_INTERNAL_SECRET"] = secret;

    const makeInternalHeaders = await freshMakeInternalHeaders();
    const tenantA = "11111111-1111-1111-1111-111111111111";
    const tenantB = "22222222-2222-2222-2222-222222222222";
    const headersA = makeInternalHeaders(tenantA);
    const headersB = makeInternalHeaders(tenantB);

    if (prev === undefined) delete process.env["COPILOT_INTERNAL_SECRET"];
    else process.env["COPILOT_INTERNAL_SECRET"] = prev;

    expect(headersA["X-Internal-Signature"]).not.toBe(
      headersB["X-Internal-Signature"]
    );
  });

  test("sem COPILOT_INTERNAL_SECRET (dev/CI): usa fallback 'dev-skip' documentado, nunca uma HMAC vazia/forjável", async () => {
    const prev = process.env["COPILOT_INTERNAL_SECRET"];
    delete process.env["COPILOT_INTERNAL_SECRET"];

    const makeInternalHeaders = await freshMakeInternalHeaders();
    const headers = makeInternalHeaders(tenantId);

    if (prev !== undefined) process.env["COPILOT_INTERNAL_SECRET"] = prev;

    expect(headers["X-Tenant-ID"]).toBe(tenantId);
    expect(headers["X-Internal-Signature"]).toBe("dev-skip");
  });
});

/**
 * Achado #15 (weak-assertion, MEDIUM): o router do copiloto
 * (hitlApprove/hitlReject/chat/sessionState) não tinha NENHUM teste
 * funcional no BFF. Duas invariantes de segurança ficavam sem guard:
 *
 *   (a) anti-vazamento de corpo interno — quando o upstream (services/copilot)
 *       responde !ok, o BFF NUNCA deve repassar o corpo da resposta upstream
 *       (que pode conter stack traces, DSNs, credenciais) na TRPCError
 *       devolvida ao cliente. Só um correlation_id (logado no servidor via
 *       console.error, nunca no response) pode aparecer na mensagem.
 *
 *   (b) injeção de tenant do ctx, nunca do input — hitlApprove/hitlReject
 *       SEMPRE devem usar ctx.tenantId (resolvido pela sessão autenticada,
 *       ver lib/context.ts) como X-Tenant-ID encaminhado ao copiloto. Um
 *       cliente que tentasse injetar um tenantId arbitrário no payload da
 *       mutation NUNCA pode fazer esse valor vazar para o header HMAC.
 */

import { createCopilotRouter } from "./copilot.js";
import type { TrpcContext } from "../lib/context.js";
import { TRPCError } from "@trpc/server";

function makeCtx(tenantId: string): TrpcContext {
  return { tenantId, userId: "user-teste" };
}

interface FetchCall {
  url: string;
  headers: Record<string, string>;
}

interface FakeResponseSpec {
  ok: boolean;
  status?: number;
  jsonBody?: unknown;
  textBody?: string;
}

/**
 * Mock de global.fetch que registra cada chamada (URL + headers) e devolve
 * uma resposta canned. Não usa jest.fn() — mesma restrição de
 * postgres-config.test.ts (ESM isolatedModules); um fechamento simples
 * registra as chamadas manualmente.
 */
function installFetchMock(spec: FakeResponseSpec): {
  calls: FetchCall[];
  restore: () => void;
} {
  const originalFetch = globalThis.fetch;
  const calls: FetchCall[] = [];

  const fakeFetch = (async (
    input: string | URL | Request,
    init?: RequestInit
  ): Promise<Response> => {
    const headers = (init?.headers ?? {}) as Record<string, string>;
    calls.push({ url: String(input), headers });

    const fakeResponse: Pick<Response, "ok" | "status" | "text" | "json"> = {
      ok: spec.ok,
      status: spec.status ?? (spec.ok ? 200 : 500),
      text: async () => spec.textBody ?? "",
      json: async () => spec.jsonBody ?? {},
    };
    return fakeResponse as Response;
  }) as typeof fetch;

  globalThis.fetch = fakeFetch;

  return {
    calls,
    restore: () => {
      globalThis.fetch = originalFetch;
    },
  };
}

describe("createCopilotRouter().hitlApprove — anti-vazamento de corpo + tenant do ctx (achado #15)", () => {
  const CTX_TENANT = "11111111-1111-1111-1111-111111111111";
  const ATTACKER_TENANT = "22222222-2222-2222-2222-222222222222";
  const THREAD_ID = "33333333-3333-3333-3333-333333333333";

  test("upstream !ok com corpo sensível -> TRPCError NÃO contém o corpo, só o correlation_id", async () => {
    const SENSITIVE_BODY =
      "postgresql://adserver:s3nh4-super-secreta@db-internal:5432/adserver — stack trace at internal/module.js:42";
    const mock = installFetchMock({ ok: false, status: 500, textBody: SENSITIVE_BODY });

    const router = createCopilotRouter();
    const caller = router.createCaller(makeCtx(CTX_TENANT));

    let thrown: unknown;
    try {
      await caller.hitlApprove({ threadId: THREAD_ID, approved: true, reason: "" });
      throw new Error("esperava que hitlApprove lançasse TRPCError, mas não lançou");
    } catch (err) {
      thrown = err;
    } finally {
      mock.restore();
    }

    expect(thrown).toBeInstanceOf(TRPCError);
    const message = (thrown as TRPCError).message;

    // Só o formato documentado (achado #15): "Falha ... ID: <correlation_id>".
    expect(message).toMatch(/^Falha ao processar aprovação HITL\. ID: .+$/);
    // O corpo upstream (credenciais, stack trace) NUNCA pode vazar na mensagem
    // devolvida ao cliente — só nos logs do servidor (console.error).
    expect(message).not.toContain(SENSITIVE_BODY);
    expect(message).not.toContain("s3nh4-super-secreta");
    expect(message).not.toContain("postgresql://");
    expect(message).not.toContain("stack trace");
  });

  test("ctx.tenantId (não um tenantId injetado no input) é o único X-Tenant-ID encaminhado ao copiloto", async () => {
    const mock = installFetchMock({
      ok: true,
      jsonBody: { status: "approved", thread_id: THREAD_ID, decision: "approved" },
    });

    const router = createCopilotRouter();
    // ctx resolvido pela sessão autenticada (lib/context.ts) — tenant real do usuário.
    const caller = router.createCaller(makeCtx(CTX_TENANT));

    // Input "malicioso": tenta carregar um tenantId de ATACANTE junto ao payload.
    // O schema Zod de hitlApprove (HitlApproveInputSchema) não declara esse
    // campo — mas o guard real é o HANDLER, que só pode ler ctx.tenantId
    // (nunca uma propriedade do input) para montar o header HMAC.
    const maliciousInput = {
      threadId: THREAD_ID,
      approved: true,
      reason: "",
      tenantId: ATTACKER_TENANT,
    };

    const result = await caller.hitlApprove(maliciousInput);

    mock.restore();

    expect(result.status).toBe("approved");
    expect(mock.calls).toHaveLength(1);
    const forwardedHeaders = mock.calls[0]?.headers ?? {};
    expect(forwardedHeaders["X-Tenant-ID"]).toBe(CTX_TENANT);
    expect(forwardedHeaders["X-Tenant-ID"]).not.toBe(ATTACKER_TENANT);
  });
});
