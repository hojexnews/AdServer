/**
 * Testes unitários do adapter in-memory de configuração.
 * Valida isolamento por tenant (CA-1) e invariantes de Money (TX-2).
 */

import { InMemoryConfigAdapter } from "./in-memory-config.js";

describe("InMemoryConfigAdapter — isolamento por tenant (CA-1)", () => {
  const adapterA = new InMemoryConfigAdapter();
  const tenantA = "11111111-1111-1111-1111-111111111111";
  const tenantB = "22222222-2222-2222-2222-222222222222";

  test("anunciante criado no tenantA não aparece no tenantB", async () => {
    await adapterA.createAdvertiser(tenantA, {
      name: "Anunciante Alpha",
      loginUsername: "alpha@example.com",
      loginPassword: "senha12345",
      isNetwork: false,
    });

    const listA = await adapterA.listAdvertisers(tenantA);
    const listB = await adapterA.listAdvertisers(tenantB);

    expect(listA.length).toBe(1);
    expect(listB.length).toBe(0);
  });

  test("campanha criada no tenantA não aparece no tenantB", async () => {
    const start = new Date();
    const end = new Date(start.getTime() + 86400_000);

    await adapterA.createCampaign(tenantA, {
      advertiserId: "1",
      name: "Campanha Alpha",
      type: "remnant",
      priority: 5,
      goalTarget: 10000,
      goalMetric: "impressions",
      startAt: start.toISOString(),
      endAt: end.toISOString(),
      pricingModel: "cpm",
      // TX-2: rate como string DECIMAL, nunca Number
      rateAmount: "12.50",
      rateCurrency: "BRL",
    });

    const listA = await adapterA.listCampaigns(tenantA);
    const listB = await adapterA.listCampaigns(tenantB);

    expect(listA.length).toBeGreaterThan(0);
    expect(listB.length).toBe(0);
  });
});

describe("InMemoryConfigAdapter — Money (TX-2)", () => {
  const adapter = new InMemoryConfigAdapter();
  const tenant = "33333333-3333-3333-3333-333333333333";

  test("rate de campanha é string DECIMAL, nunca Number", async () => {
    const start = new Date();
    const end = new Date(start.getTime() + 86400_000);

    const campaign = await adapter.createCampaign(tenant, {
      advertiserId: "1",
      name: "Campanha CPM",
      type: "contract",
      priority: 3,
      goalTarget: 50000,
      goalMetric: "impressions",
      startAt: start.toISOString(),
      endAt: end.toISOString(),
      pricingModel: "cpm",
      rateAmount: "5.000000",
      rateCurrency: "USDC",
    });

    expect(typeof campaign.rate.amount).toBe("string");
    expect(typeof campaign.rate.currency).toBe("string");
    expect(campaign.rate.amount).toBe("5.000000");
    expect(campaign.rate.currency).toBe("USDC");
    // Nunca deve ser Number
    expect(campaign.rate.amount).not.toBeNaN();
    expect(typeof Number(campaign.rate.amount)).toBe("number"); // ok como verificação, mas o campo PERMANECE string
  });
});

describe("InMemoryConfigAdapter — CampaignZone N:N (DA-2)", () => {
  const adapter = new InMemoryConfigAdapter();
  const tenant = "44444444-4444-4444-4444-444444444444";

  test("link e unlink de zonas a campanha", async () => {
    const linked = await adapter.linkCampaignZones(tenant, "10", [
      "20",
      "21",
      "22",
    ]);
    expect(linked.length).toBe(3);

    await adapter.unlinkCampaignZones(tenant, "10", ["21"]);
    const remaining = await adapter.listCampaignZones(tenant, "10");
    expect(remaining.length).toBe(2);
    expect(remaining.map((cz) => cz.zoneId)).not.toContain("21");
  });
});
