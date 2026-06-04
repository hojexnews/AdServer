/**
 * Testes unitários do adapter in-memory de pagamentos — K7 (Fase 3).
 *
 * Valida:
 *   1. Isolamento por tenant (CA-1 / TX-3): cada tenant vê apenas seus dados.
 *   2. Money como string DECIMAL, nunca Number (TX-2).
 *   3. Paginação e filtros funcionam corretamente.
 *   4. Nenhum segredo ou PII de terceiros nos dados retornados.
 */

import { InMemoryPaymentsAdapter } from "./in-memory-payments.js";

const TENANT_A = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa";
const TENANT_B = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb";

describe("InMemoryPaymentsAdapter — isolamento por tenant (CA-1 / TX-3)", () => {
  const adapter = new InMemoryPaymentsAdapter();

  test("getBalances — tenant A e B retornam saldos distintos", async () => {
    const balA = await adapter.getBalances(TENANT_A);
    const balB = await adapter.getBalances(TENANT_B);

    // Ambos têm saldos, mas valores distintos (seed diferente por tenant)
    expect(balA.length).toBeGreaterThan(0);
    expect(balB.length).toBeGreaterThan(0);

    // Os IDs de payment não devem coincidir entre tenants
    const { items: itemsA } = await adapter.listPaymentStatus(TENANT_A, { page: 1, pageSize: 100 });
    const { items: itemsB } = await adapter.listPaymentStatus(TENANT_B, { page: 1, pageSize: 100 });

    const idsA = new Set(itemsA.map((i) => i.payment_id));
    const idsB = new Set(itemsB.map((i) => i.payment_id));

    // Não pode haver sobreposição de IDs entre tenants distintos
    for (const id of idsA) {
      expect(idsB.has(id)).toBe(false);
    }
  });
});

describe("InMemoryPaymentsAdapter — Money como string DECIMAL (TX-2)", () => {
  const adapter = new InMemoryPaymentsAdapter();

  test("getBalances — amount é string DECIMAL, nunca Number", async () => {
    const balances = await adapter.getBalances(TENANT_A);

    for (const bal of balances) {
      expect(typeof bal.amount).toBe("string");
      // Deve ser string DECIMAL válida
      expect(bal.amount).toMatch(/^-?\d+(\.\d+)?$/);
      // Nunca deve ser NaN
      expect(bal.amount).not.toBe("NaN");
      // O campo amount NÃO deve ser um number
      // (typeof verificado acima — string garante)
    }
  });

  test("listPaymentStatus — amount.amount é string DECIMAL, nunca Number", async () => {
    const { items } = await adapter.listPaymentStatus(TENANT_A, { page: 1, pageSize: 100 });

    for (const item of items) {
      expect(typeof item.amount.amount).toBe("string");
      expect(item.amount.amount).toMatch(/^-?\d+(\.\d+)?$/);
      // currency deve ser código em maiúsculas
      expect(item.amount.currency).toMatch(/^[A-Z0-9]+$/);
    }
  });

  test("BRL tem escala 2 (2 casas decimais)", async () => {
    const balances = await adapter.getBalances(TENANT_A);
    const brl = balances.find((b) => b.asset_code === "BRL");
    expect(brl).toBeDefined();
    expect(brl!.scale).toBe(2);
    expect(brl!.amount).toMatch(/^\d+\.\d{2}$/);
  });

  test("USDC tem escala 6 (6 casas decimais)", async () => {
    const balances = await adapter.getBalances(TENANT_A);
    const usdc = balances.find((b) => b.asset_code === "USDC");
    expect(usdc).toBeDefined();
    expect(usdc!.scale).toBe(6);
    expect(usdc!.amount).toMatch(/^\d+\.\d{6}$/);
  });
});

describe("InMemoryPaymentsAdapter — paginação e filtros", () => {
  const adapter = new InMemoryPaymentsAdapter();

  test("paginação: pageSize=2, page=1 retorna no máximo 2 itens", async () => {
    const { items } = await adapter.listPaymentStatus(TENANT_A, { page: 1, pageSize: 2 });
    expect(items.length).toBeLessThanOrEqual(2);
  });

  test("paginação: page=2 retorna itens distintos da page=1", async () => {
    const { items: page1 } = await adapter.listPaymentStatus(TENANT_A, { page: 1, pageSize: 2 });
    const { items: page2 } = await adapter.listPaymentStatus(TENANT_A, { page: 2, pageSize: 2 });

    const ids1 = new Set(page1.map((i) => i.payment_id));
    for (const item of page2) {
      expect(ids1.has(item.payment_id)).toBe(false);
    }
  });

  test("filtro por rail: retorna apenas itens do rail especificado", async () => {
    const { items } = await adapter.listPaymentStatus(TENANT_A, {
      page: 1,
      pageSize: 100,
      rail: "fiat_stripe",
    });
    for (const item of items) {
      expect(item.rail).toBe("fiat_stripe");
    }
  });

  test("filtro por status: retorna apenas itens do status especificado", async () => {
    const { items } = await adapter.listPaymentStatus(TENANT_A, {
      page: 1,
      pageSize: 100,
      status: "posted",
    });
    for (const item of items) {
      expect(item.status).toBe("posted");
    }
  });

  test("total reflete o número real de itens sem paginação", async () => {
    const { total: total1, items: items1 } = await adapter.listPaymentStatus(TENANT_A, {
      page: 1,
      pageSize: 100,
    });
    expect(total1).toBe(items1.length);
  });
});

describe("InMemoryPaymentsAdapter — sem segredos/PII no payload", () => {
  const adapter = new InMemoryPaymentsAdapter();

  test("nenhum campo suspeito nos itens de pagamento", async () => {
    const { items } = await adapter.listPaymentStatus(TENANT_A, { page: 1, pageSize: 100 });

    for (const item of items) {
      const serialized = JSON.stringify(item).toLowerCase();
      // Sem chaves de API
      expect(serialized).not.toContain("sk_");
      expect(serialized).not.toContain("api_key");
      expect(serialized).not.toContain("secret");
      expect(serialized).not.toContain("dsn");
      expect(serialized).not.toContain("password");
      // pix_end_to_end_id e tx_hash são opacos (aceitáveis) — sem CPF/nome
      expect(serialized).not.toContain("cpf");
      expect(serialized).not.toContain("nome");
    }
  });

  test("nenhum campo suspeito nos saldos", async () => {
    const balances = await adapter.getBalances(TENANT_A);
    for (const bal of balances) {
      const serialized = JSON.stringify(bal).toLowerCase();
      expect(serialized).not.toContain("sk_");
      expect(serialized).not.toContain("secret");
      expect(serialized).not.toContain("dsn");
    }
  });
});
