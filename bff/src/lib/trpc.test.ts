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
 */

import { createConfigRouter } from "../routers/config.js";
import { InMemoryConfigAdapter } from "../adapters/in-memory-config.js";
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
