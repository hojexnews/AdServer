/**
 * Testes de checkDiffForContradictions()/resolveContradictionWarning()
 * (CA-4 — validação anti-contradição sobre sugestões do copiloto).
 *
 * FIX #17 (achado da 27ª onda): a versão anterior de checkDiffForContradictions
 * comparava o candidato sugerido pela IA CONSIGO MESMO
 * (`detectContradictions([candidate, candidate])`) e descartava o resultado
 * (`void selfCheck`) — um self-compare que NUNCA pode detectar nada real
 * (uma contradição exige duas regras DIFERENTES). O aviso realmente exibido
 * vinha de um heurístico textual solto ("AND" + "is not"), não da validação
 * anti-contradição que a doc/mandato prometiam reutilizar de lib/contradiction.ts.
 *
 * Sem estes testes, uma regressão que reintroduza o self-compare (ignorando
 * `existingRules`) deixaria `make web-test` verde mesmo com o gate CA-4 do
 * copiloto sendo pura encenação — exatamente o cenário que o mandato do
 * frontend-bff-engineer proíbe ("nunca salvar regra AND mutuamente exclusiva
 * sem alerta anti-contradição", inclusive sobre sugestões da IA).
 *
 * NOTA DE INFRAESTRUTURA DE TESTE: o console não tem jest/vitest (ver
 * make/web.mk). node:test roda .ts nativamente, mas não resolve, fora do
 * bundler do Next, o alias de path "@/*" usado por use-copilot-session.ts
 * (import de @/lib/trpc e @/lib/copilot-schemas). Reutilizamos o MESMO hook
 * de resolução usado por middleware.test.ts (src/lib/test-node-loader-hook.mjs)
 * — nenhum arquivo de produção muda por causa disso.
 */

import { test } from "node:test";
import assert from "node:assert/strict";
import { register } from "node:module";

register(new URL("./test-node-loader-hook.mjs", import.meta.url));

const {
  checkDiffForContradictions,
  resolveContradictionWarning,
} = (await import("./use-copilot-session.ts")) as typeof import("./use-copilot-session.ts");

type RuleCandidateLike = {
  vector: string;
  operator: string;
  value: string;
  logicalOp: "AND" | "OR";
};

type RuleDiffLike = {
  kind: "rule";
  action: "create" | "update" | "delete";
  id: string | null;
  ownerType: "campaign" | "banner" | null;
  ownerId: string | null;
  vector: string | null;
  operator: string | null;
  value: string | null;
  logicalOp: "AND" | "OR" | null;
};

function ruleDiff(over: Partial<RuleDiffLike> = {}): RuleDiffLike {
  return {
    kind: "rule",
    action: "create",
    id: null,
    ownerType: "campaign",
    ownerId: "42",
    vector: "Geo - Country",
    operator: "is",
    value: "BR",
    logicalOp: "AND",
    ...over,
  };
}

// ---------------------------------------------------------------------------
// checkDiffForContradictions — função pura (diff, existingRules) -> warning
// ---------------------------------------------------------------------------

test("checkDiffForContradictions: candidato CONTRADIZ regra existente (mesmo vetor discreto, is, valor diferente) -> detectado", () => {
  const diff = ruleDiff({ vector: "Geo - Country", operator: "is", value: "BR", logicalOp: "AND" });
  const existingRules: RuleCandidateLike[] = [
    { vector: "Geo - Country", operator: "is", value: "US", logicalOp: "AND" },
  ];

  const warning = checkDiffForContradictions(diff as never, existingRules as never);

  assert.notEqual(warning, null);
  assert.match(warning as string, /CA-4/);
  assert.match(warning as string, /conflito/);
});

test("checkDiffForContradictions: candidato CONTRADIZ regra existente (is vs is not, mesmo valor) -> detectado", () => {
  const diff = ruleDiff({ vector: "Geo - Country", operator: "is not", value: "BR", logicalOp: "AND" });
  const existingRules: RuleCandidateLike[] = [
    { vector: "Geo - Country", operator: "is", value: "BR", logicalOp: "AND" },
  ];

  const warning = checkDiffForContradictions(diff as never, existingRules as never);

  assert.notEqual(warning, null);
  assert.match(warning as string, /CA-4/);
});

test("checkDiffForContradictions: sem regras existentes (contexto vazio) -> NUNCA finge detectar (sem falso-positivo do heurístico antigo)", () => {
  // Este é exatamente o padrão que o heurístico antigo sinalizava
  // (AND + "is not") mesmo sem NENHUMA regra real para conflitar.
  const diff = ruleDiff({ vector: "Geo - Country", operator: "is not", value: "BR", logicalOp: "AND" });

  const warning = checkDiffForContradictions(diff as never, []);

  assert.equal(warning, null);
});

test("checkDiffForContradictions: regra existente com vetor diferente -> nenhuma contradição", () => {
  const diff = ruleDiff({ vector: "Geo - Country", operator: "is", value: "BR", logicalOp: "AND" });
  const existingRules: RuleCandidateLike[] = [
    { vector: "Time - Day of Week", operator: "is", value: "monday", logicalOp: "AND" },
  ];

  const warning = checkDiffForContradictions(diff as never, existingRules as never);

  assert.equal(warning, null);
});

test("checkDiffForContradictions: diff.kind !== 'rule' -> null (nunca roda checagem de regra sobre outro tipo de diff)", () => {
  const diff = { kind: "campaign", action: "update" } as const;
  const warning = checkDiffForContradictions(diff as never, [
    { vector: "Geo - Country", operator: "is", value: "US", logicalOp: "AND" },
  ] as never);
  assert.equal(warning, null);
});

test("checkDiffForContradictions: action 'delete' -> null (nada novo sendo afirmado, nada a conflitar)", () => {
  const diff = ruleDiff({ action: "delete", vector: "Geo - Country", operator: "is", value: "BR", logicalOp: "AND" });
  const existingRules: RuleCandidateLike[] = [
    { vector: "Geo - Country", operator: "is", value: "US", logicalOp: "AND" },
  ];
  const warning = checkDiffForContradictions(diff as never, existingRules as never);
  assert.equal(warning, null);
});

// ---------------------------------------------------------------------------
// resolveContradictionWarning — busca as regras existentes (fetcher injetado)
// e chama checkDiffForContradictions com o contexto REAL.
//
// Este é o teste central do FIX #17: antes do fix, não havia NENHUMA busca
// de regras existentes — checkDiffForContradictions(diff) só via o candidato
// isolado e comparava consigo mesmo. Um candidato que contradiz uma regra
// JÁ SALVA do mesmo owner nunca era detectado.
// ---------------------------------------------------------------------------

test("resolveContradictionWarning: candidato CONTRADIZ regra existente do owner (buscada via fetcher injetado) -> detectado (antes do fix: nao detectava)", async () => {
  const diff = ruleDiff({
    ownerType: "campaign",
    ownerId: "42",
    vector: "Geo - Country",
    operator: "is",
    value: "BR",
    logicalOp: "AND",
  });

  let calledWith: [string, string] | null = null;
  const fetcher = async (ownerType: "campaign" | "banner", ownerId: string) => {
    calledWith = [ownerType, ownerId];
    return [
      { vector: "Geo - Country", operator: "is", value: "US", logicalOp: "AND" as const },
    ];
  };

  const warning = await resolveContradictionWarning(diff as never, fetcher);

  assert.deepEqual(calledWith, ["campaign", "42"]);
  assert.notEqual(warning, null);
  assert.match(warning as string, /CA-4/);
  assert.match(warning as string, /BR.*US|US.*BR/);
});

test("resolveContradictionWarning: regra existente do owner sem conflito -> null", async () => {
  const diff = ruleDiff({
    ownerType: "campaign",
    ownerId: "42",
    vector: "Geo - Country",
    operator: "is",
    value: "BR",
    logicalOp: "AND",
  });

  const fetcher = async () => [
    { vector: "Site - URL", operator: "is", value: "/promo", logicalOp: "AND" as const },
  ];

  const warning = await resolveContradictionWarning(diff as never, fetcher);
  assert.equal(warning, null);
});

test("resolveContradictionWarning: fetcher falha (rede) -> nao trava o fluxo HITL, segue conservador (null)", async () => {
  const diff = ruleDiff({ ownerType: "campaign", ownerId: "42" });
  const fetcher = async (): Promise<never> => {
    throw new Error("BFF indisponível");
  };

  const warning = await resolveContradictionWarning(diff as never, fetcher as never);
  assert.equal(warning, null);
});

test("resolveContradictionWarning: diff sem ownerType/ownerId -> fetcher NUNCA é chamado", async () => {
  const diff = ruleDiff({ ownerType: null, ownerId: null });
  let calls = 0;
  const fetcher = async () => {
    calls += 1;
    return [];
  };

  const warning = await resolveContradictionWarning(diff as never, fetcher);

  assert.equal(calls, 0);
  assert.equal(warning, null);
});
