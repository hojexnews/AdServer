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
