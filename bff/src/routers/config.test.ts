/**
 * Testes do backstop server-side CA-4 em deliveryRule.create/update (wave 30,
 * achado contradiction-vector-allowlist-gap + remediação subsequente).
 *
 * A UI (web/console/src/app/rules/page.tsx) já roda detectContradictions()
 * antes de enviar a mutação, mas a UI sozinha não é fronteira de confiança.
 * Estes testes provam o comportamento dos PROCEDIMENTOS tRPC reais (não uma
 * reimplementação local): sem confirmação explícita, uma regra AND que
 * contradiz uma regra já existente do mesmo owner é REJEITADA (CONFLICT)
 * antes de qualquer escrita; com acknowledgeContradiction=true, é persistida
 * (o usuário já viu o alerta e confirmou, espelhando o botão "Salvar mesmo
 * assim" do builder).
 *
 * REMEDIAÇÃO (achado #3 — "bypass trivial do controle recém-adicionado"): o
 * backstop original só cobria deliveryRule.create — deliveryRule.update
 * escapava inteiramente, permitindo criar uma regra sem contradição e depois
 * dar update para o estado contraditório. O describe
 * "deliveryRule.update — backstop..." abaixo fecha essa instância; o
 * describe "deliveryRule — TODO mutador..." no final fecha a FORMA do
 * defeito: por reflexão sobre os mutadores REAIS do router (não uma lista
 * manual), garante que um mutador futuro que grave vector/operator/value/
 * logicalOp sem registrar uma prova de cobertura CA-4 quebra a suíte em vez
 * de escapar silenciosamente.
 */

import { createConfigRouter } from "./config.js";
import { InMemoryConfigAdapter } from "../adapters/in-memory-config.js";
import type { TrpcContext } from "../lib/context.js";

function ctxFor(tenantId: string): TrpcContext {
  return { tenantId, userId: "test-user" };
}

describe("deliveryRule.create — backstop anti-contradição server-side (CA-4)", () => {
  test("regra AND que contradiz uma regra JÁ EXISTENTE do mesmo owner -> CONFLICT, sem acknowledgeContradiction", async () => {
    const tenant = "tenant-contradiction-1";
    const adapter = new InMemoryConfigAdapter();
    const router = createConfigRouter(adapter);
    const caller = router.createCaller(ctxFor(tenant));

    await caller.deliveryRule.create({
      ownerType: "campaign",
      ownerId: "1",
      vector: "Site - URL",
      operator: "is",
      value: "/a",
      logicalOp: "AND",
      ruleSetId: null,
      orderSeq: 0,
    });

    await expect(
      caller.deliveryRule.create({
        ownerType: "campaign",
        ownerId: "1",
        vector: "Site - URL",
        operator: "is",
        value: "/b",
        logicalOp: "AND",
        ruleSetId: null,
        orderSeq: 1,
      })
    ).rejects.toMatchObject({ code: "CONFLICT" });

    // Nenhum INSERT ocorreu para a 2ª regra: só a 1ª está persistida.
    const rules = await caller.deliveryRule.list({
      ownerType: "campaign",
      ownerId: "1",
    });
    expect(rules).toHaveLength(1);
    expect(rules[0]?.value).toBe("/a");
  });

  test("mesma contradição, MAS acknowledgeContradiction=true -> persiste mesmo assim", async () => {
    const tenant = "tenant-contradiction-2";
    const adapter = new InMemoryConfigAdapter();
    const router = createConfigRouter(adapter);
    const caller = router.createCaller(ctxFor(tenant));

    await caller.deliveryRule.create({
      ownerType: "campaign",
      ownerId: "1",
      vector: "Geo - Country",
      operator: "is",
      value: "BR",
      logicalOp: "AND",
      ruleSetId: null,
      orderSeq: 0,
    });

    const second = await caller.deliveryRule.create({
      ownerType: "campaign",
      ownerId: "1",
      vector: "Geo - Country",
      operator: "is",
      value: "US",
      logicalOp: "AND",
      ruleSetId: null,
      orderSeq: 1,
      acknowledgeContradiction: true,
    });

    expect(second.value).toBe("US");
    const rules = await caller.deliveryRule.list({
      ownerType: "campaign",
      ownerId: "1",
    });
    expect(rules).toHaveLength(2);
  });

  test("regras sem contradição (vetores diferentes) -> persiste sem exigir acknowledgeContradiction", async () => {
    const tenant = "tenant-contradiction-3";
    const adapter = new InMemoryConfigAdapter();
    const router = createConfigRouter(adapter);
    const caller = router.createCaller(ctxFor(tenant));

    await caller.deliveryRule.create({
      ownerType: "campaign",
      ownerId: "1",
      vector: "Geo - Country",
      operator: "is",
      value: "BR",
      logicalOp: "AND",
      ruleSetId: null,
      orderSeq: 0,
    });

    await expect(
      caller.deliveryRule.create({
        ownerType: "campaign",
        ownerId: "1",
        vector: "Site - URL",
        operator: "is",
        value: "/promo",
        logicalOp: "AND",
        ruleSetId: null,
        orderSeq: 1,
      })
    ).resolves.toMatchObject({ value: "/promo" });
  });

  test("contradição contra regra de OUTRO owner NÃO bloqueia (escopo por owner)", async () => {
    const tenant = "tenant-contradiction-4";
    const adapter = new InMemoryConfigAdapter();
    const router = createConfigRouter(adapter);
    const caller = router.createCaller(ctxFor(tenant));

    await caller.deliveryRule.create({
      ownerType: "campaign",
      ownerId: "1",
      vector: "Site - URL",
      operator: "is",
      value: "/a",
      logicalOp: "AND",
      ruleSetId: null,
      orderSeq: 0,
    });

    await expect(
      caller.deliveryRule.create({
        ownerType: "campaign",
        ownerId: "2",
        vector: "Site - URL",
        operator: "is",
        value: "/b",
        logicalOp: "AND",
        ruleSetId: null,
        orderSeq: 0,
      })
    ).resolves.toMatchObject({ value: "/b" });
  });

  test("contradição contra regra INATIVA do mesmo owner NÃO bloqueia", async () => {
    const tenant = "tenant-contradiction-5";
    const adapter = new InMemoryConfigAdapter();
    const router = createConfigRouter(adapter);
    const caller = router.createCaller(ctxFor(tenant));

    const first = await caller.deliveryRule.create({
      ownerType: "campaign",
      ownerId: "1",
      vector: "Site - URL",
      operator: "is",
      value: "/a",
      logicalOp: "AND",
      ruleSetId: null,
      orderSeq: 0,
    });
    await caller.deliveryRule.update({ id: first.id, active: false });

    await expect(
      caller.deliveryRule.create({
        ownerType: "campaign",
        ownerId: "1",
        vector: "Site - URL",
        operator: "is",
        value: "/b",
        logicalOp: "AND",
        ruleSetId: null,
        orderSeq: 1,
      })
    ).resolves.toMatchObject({ value: "/b" });
  });
});

describe("deliveryRule.update — backstop anti-contradição server-side (CA-4) (achado #3, wave 30 remediação)", () => {
  test("update leva o owner a um estado AND contraditório -> CONFLICT, sem acknowledgeContradiction (bypass fechado)", async () => {
    const tenant = "tenant-update-contradiction-1";
    const adapter = new InMemoryConfigAdapter();
    const router = createConfigRouter(adapter);
    const caller = router.createCaller(ctxFor(tenant));

    await caller.deliveryRule.create({
      ownerType: "campaign",
      ownerId: "1",
      vector: "Geo - Country",
      operator: "is",
      value: "BR",
      logicalOp: "AND",
      ruleSetId: null,
      orderSeq: 0,
    });
    // Criada SEM contradição (vetor diferente) — o backstop de create não
    // tem nada a rejeitar aqui.
    const second = await caller.deliveryRule.create({
      ownerType: "campaign",
      ownerId: "1",
      vector: "Site - URL",
      operator: "is",
      value: "/a",
      logicalOp: "AND",
      ruleSetId: null,
      orderSeq: 1,
    });

    // O ataque: update muda o vetor da 2ª regra para "Geo - Country" com um
    // valor diferente de "BR" — agora contraditória com a 1ª regra. Antes da
    // remediação, isto persistia sem checagem alguma.
    await expect(
      caller.deliveryRule.update({
        id: second.id,
        vector: "Geo - Country",
        value: "US",
      })
    ).rejects.toMatchObject({ code: "CONFLICT" });

    // Nenhuma escrita ocorreu: a 2ª regra continua com o vetor/valor original.
    const rules = await caller.deliveryRule.list({
      ownerType: "campaign",
      ownerId: "1",
    });
    const stillOriginal = rules.find((r) => r.id === second.id);
    expect(stillOriginal?.vector).toBe("Site - URL");
    expect(stillOriginal?.value).toBe("/a");
  });

  test("mesmo update contraditório, MAS acknowledgeContradiction=true -> persiste mesmo assim", async () => {
    const tenant = "tenant-update-contradiction-2";
    const adapter = new InMemoryConfigAdapter();
    const router = createConfigRouter(adapter);
    const caller = router.createCaller(ctxFor(tenant));

    await caller.deliveryRule.create({
      ownerType: "campaign",
      ownerId: "1",
      vector: "Geo - Country",
      operator: "is",
      value: "BR",
      logicalOp: "AND",
      ruleSetId: null,
      orderSeq: 0,
    });
    const second = await caller.deliveryRule.create({
      ownerType: "campaign",
      ownerId: "1",
      vector: "Site - URL",
      operator: "is",
      value: "/a",
      logicalOp: "AND",
      ruleSetId: null,
      orderSeq: 1,
    });

    const updated = await caller.deliveryRule.update({
      id: second.id,
      vector: "Geo - Country",
      value: "US",
      acknowledgeContradiction: true,
    });

    expect(updated.vector).toBe("Geo - Country");
    expect(updated.value).toBe("US");
  });

  test("update que NÃO toca vector/operator/value/logicalOp (ex.: só orderSeq) -> nunca bloqueia", async () => {
    const tenant = "tenant-update-contradiction-3";
    const adapter = new InMemoryConfigAdapter();
    const router = createConfigRouter(adapter);
    const caller = router.createCaller(ctxFor(tenant));

    await caller.deliveryRule.create({
      ownerType: "campaign",
      ownerId: "1",
      vector: "Geo - Country",
      operator: "is",
      value: "BR",
      logicalOp: "AND",
      ruleSetId: null,
      orderSeq: 0,
    });
    // Vetor DIFERENTE da 1ª regra — sem contradição alguma, nem antes nem
    // depois do update de orderSeq abaixo (isola o comportamento sob teste:
    // um update que não toca campos de segmentação nunca deveria bloquear).
    const second = await caller.deliveryRule.create({
      ownerType: "campaign",
      ownerId: "1",
      vector: "Site - URL",
      operator: "is",
      value: "/a",
      logicalOp: "AND",
      ruleSetId: null,
      orderSeq: 1,
    });

    await expect(
      caller.deliveryRule.update({ id: second.id, orderSeq: 5 })
    ).resolves.toMatchObject({ orderSeq: 5 });
  });

  test("update que DESATIVA a regra (active:false) -> nunca bloqueia, mesmo se o estado resultante contradiria uma irmã", async () => {
    const tenant = "tenant-update-contradiction-4";
    const adapter = new InMemoryConfigAdapter();
    const router = createConfigRouter(adapter);
    const caller = router.createCaller(ctxFor(tenant));

    await caller.deliveryRule.create({
      ownerType: "campaign",
      ownerId: "1",
      vector: "Geo - Country",
      operator: "is",
      value: "BR",
      logicalOp: "AND",
      ruleSetId: null,
      orderSeq: 0,
    });
    const second = await caller.deliveryRule.create({
      ownerType: "campaign",
      ownerId: "1",
      vector: "Site - URL",
      operator: "is",
      value: "/a",
      logicalOp: "AND",
      ruleSetId: null,
      orderSeq: 1,
    });

    await expect(
      caller.deliveryRule.update({
        id: second.id,
        vector: "Geo - Country",
        value: "US",
        active: false,
      })
    ).resolves.toMatchObject({ active: false, vector: "Geo - Country" });
  });

  test("update NÃO se compara contra si mesma (excludeRuleId) — reenviar o MESMO valor não é contradição", async () => {
    const tenant = "tenant-update-contradiction-5";
    const adapter = new InMemoryConfigAdapter();
    const router = createConfigRouter(adapter);
    const caller = router.createCaller(ctxFor(tenant));

    const rule = await caller.deliveryRule.create({
      ownerType: "campaign",
      ownerId: "1",
      vector: "Geo - Country",
      operator: "is",
      value: "BR",
      logicalOp: "AND",
      ruleSetId: null,
      orderSeq: 0,
    });

    await expect(
      caller.deliveryRule.update({ id: rule.id, value: "BR" })
    ).resolves.toMatchObject({ value: "BR" });
  });

  test("update de id inexistente -> NOT_FOUND (comportamento preservado, sem crash na busca de contexto)", async () => {
    const tenant = "tenant-update-contradiction-6";
    const adapter = new InMemoryConfigAdapter();
    const router = createConfigRouter(adapter);
    const caller = router.createCaller(ctxFor(tenant));

    await expect(
      caller.deliveryRule.update({ id: "999999", value: "x" })
    ).rejects.toMatchObject({ code: "NOT_FOUND" });
  });
});

// ---------------------------------------------------------------------------
// default-deny — TODO mutador de DeliveryRule que possa produzir um estado
// ativo novo passa pelo backstop CA-4, por REFLEXÃO sobre os mutadores REAIS
// do router (achado #3, wave 30 remediação — "a forma do defeito", não só a
// instância concreta do gap em update).
// ---------------------------------------------------------------------------

describe("deliveryRule — TODO mutador não isento aplica CA-4 (default-deny por reflexão)", () => {
  // Allowlist de EXCLUSÃO explícita e comentada — NUNCA uma lista de
  // inclusão manual dos mutadores cobertos (isso reintroduziria o mesmo
  // ponto-cego do achado #3).
  const EXEMPT_MUTATORS: Record<string, string> = {
    "deliveryRule.delete":
      "remover uma regra só pode ELIMINAR uma contradição existente, nunca " +
      "introduzir uma nova — não há candidato pós-mutação para checar.",
  };

  // Reflexão sobre os mutadores REAIS do sub-router deliveryRule — não uma
  // lista manual. Um mutador novo (reorder, bulkUpdate, etc.) aparece aqui
  // automaticamente.
  const router = createConfigRouter(new InMemoryConfigAdapter());
  const flatProcedures = (
    router._def as unknown as {
      procedures: Record<string, { _def: { type: string } }>;
    }
  ).procedures;
  const deliveryRuleMutationPaths = Object.entries(flatProcedures)
    .filter(
      ([path, proc]) =>
        path.startsWith("deliveryRule.") && proc._def.type === "mutation"
    )
    .map(([path]) => path);

  // Sentinela anti-vazio: se a reflexão parar de enumerar (refactor do
  // router, mudança de versão do tRPC), deliveryRuleMutationPaths cai para 0
  // e os test.each abaixo não geram teste algum — falso-verde silencioso.
  test("sentinela: a reflexão enumerou os 3 mutadores esperados de deliveryRule", () => {
    expect(deliveryRuleMutationPaths.length).toBe(3);
    expect(deliveryRuleMutationPaths.sort()).toEqual(
      ["deliveryRule.create", "deliveryRule.delete", "deliveryRule.update"].sort()
    );
  });

  const guardedPaths = deliveryRuleMutationPaths.filter(
    (p) => !(p in EXEMPT_MUTATORS)
  );

  // Provas de cobertura por mutador — cada entrada monta o cenário
  // (uma regra existente + um candidato que a contradiz) e chama o mutador
  // SEM acknowledgeContradiction (deve rejeitar) e COM (deve persistir). Um
  // mutador guardado sem entrada aqui falha no teste seguinte, não escapa
  // silenciosamente.
  const MUTATION_PROBES: Record<
    string,
    (caller: ReturnType<typeof router.createCaller>) => Promise<{
      withoutAck: () => Promise<unknown>;
      withAck: () => Promise<unknown>;
    }>
  > = {
    "deliveryRule.create": async (caller) => {
      await caller.deliveryRule.create({
        ownerType: "campaign",
        ownerId: "1",
        vector: "Geo - Country",
        operator: "is",
        value: "BR",
        logicalOp: "AND",
        ruleSetId: null,
        orderSeq: 0,
      });
      const conflicting = {
        ownerType: "campaign" as const,
        ownerId: "1",
        vector: "Geo - Country" as const,
        operator: "is" as const,
        value: "US",
        logicalOp: "AND" as const,
        ruleSetId: null,
        orderSeq: 1,
      };
      return {
        withoutAck: () => caller.deliveryRule.create(conflicting),
        withAck: () =>
          caller.deliveryRule.create({
            ...conflicting,
            acknowledgeContradiction: true,
          }),
      };
    },
    "deliveryRule.update": async (caller) => {
      await caller.deliveryRule.create({
        ownerType: "campaign",
        ownerId: "1",
        vector: "Geo - Country",
        operator: "is",
        value: "BR",
        logicalOp: "AND",
        ruleSetId: null,
        orderSeq: 0,
      });
      const target = await caller.deliveryRule.create({
        ownerType: "campaign",
        ownerId: "1",
        vector: "Site - URL",
        operator: "is",
        value: "/a",
        logicalOp: "AND",
        ruleSetId: null,
        orderSeq: 1,
      });
      return {
        withoutAck: () =>
          caller.deliveryRule.update({
            id: target.id,
            vector: "Geo - Country",
            value: "US",
          }),
        withAck: () =>
          caller.deliveryRule.update({
            id: target.id,
            vector: "Geo - Country",
            value: "US",
            acknowledgeContradiction: true,
          }),
      };
    },
  };

  test("todo mutador guardado tem um probe de cobertura CA-4 registrado (nenhum escapa por falta de prova)", () => {
    for (const path of guardedPaths) {
      expect(Object.keys(MUTATION_PROBES)).toContain(path);
    }
  });

  test.each(guardedPaths)(
    "%s: rejeita CONFLICT sem acknowledgeContradiction e persiste com o flag",
    async (path) => {
      const probe = MUTATION_PROBES[path];
      if (!probe) {
        throw new Error(
          `Mutador "${path}" não tem probe de cobertura CA-4 registrado em ` +
            `MUTATION_PROBES — veja o teste anterior.`
        );
      }
      const tenant = `tenant-default-deny-${path}`;
      const adapter = new InMemoryConfigAdapter();
      const router2 = createConfigRouter(adapter);
      const caller = router2.createCaller(ctxFor(tenant));

      const { withoutAck, withAck } = await probe(caller);

      await expect(withoutAck()).rejects.toMatchObject({ code: "CONFLICT" });
      await expect(withAck()).resolves.toBeDefined();
    }
  );
});
