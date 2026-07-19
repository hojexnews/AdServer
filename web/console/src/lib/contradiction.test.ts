/**
 * Testes known-answer de detectContradictions (CA-4 / §4.6).
 *
 * "Nunca salvar regra AND mutuamente exclusiva sem alerta anti-contradição"
 * (mandato do frontend-bff-engineer). Sem estes testes, uma mutação que
 * desativa a detecção discreta (ex.: `DISCRETE_EXCLUSIVE_VECTORS.has(...)`
 * -> `false`, ou inverter `a.value !== b.value`) deixa web-ci verde
 * (achado #27) — o builder de segmentação silenciaria o banner sem avisar
 * o usuário, exatamente o cenário que CA-4 proíbe.
 */

import { test } from "node:test";
import assert from "node:assert/strict";
import { detectContradictions, type RuleCandidate } from "./contradiction.ts";

function rule(partial: Partial<RuleCandidate>): RuleCandidate {
  return {
    vector: "Geo - Country",
    operator: "is",
    value: "BR",
    logicalOp: "AND",
    ...partial,
  };
}

test("par contraditório (mesmo vetor discreto, operador is, valores diferentes) -> detectado", () => {
  const rules: RuleCandidate[] = [
    rule({ vector: "Geo - Country", operator: "is", value: "BR" }),
    rule({ vector: "Geo - Country", operator: "is", value: "US" }),
  ];

  const result = detectContradictions(rules);

  assert.equal(result.hasContradiction, true);
  assert.equal(result.conflicts.length, 1);
  assert.match(result.conflicts[0]!.reason, /Contradição AND/);
});

test("par contraditório Time - Day of Week (segunda E terça simultaneamente) -> detectado", () => {
  const rules: RuleCandidate[] = [
    rule({ vector: "Time - Day of Week", operator: "is", value: "monday" }),
    rule({ vector: "Time - Day of Week", operator: "is", value: "tuesday" }),
  ];

  const result = detectContradictions(rules);

  assert.equal(result.hasContradiction, true);
  assert.equal(result.conflicts.length, 1);
});

test("par contraditório is vs is not no mesmo valor -> detectado", () => {
  const rules: RuleCandidate[] = [
    rule({ vector: "Geo - Country", operator: "is", value: "BR" }),
    rule({ vector: "Geo - Country", operator: "is not", value: "BR" }),
  ];

  const result = detectContradictions(rules);

  assert.equal(result.hasContradiction, true);
  assert.equal(result.conflicts.length, 1);
  assert.match(result.conflicts[0]!.reason, /mutuamente exclusivos/);
});

test("par válido (vetores diferentes) -> nenhuma contradição", () => {
  const rules: RuleCandidate[] = [
    rule({ vector: "Geo - Country", operator: "is", value: "BR" }),
    rule({ vector: "Time - Day of Week", operator: "is", value: "monday" }),
  ];

  const result = detectContradictions(rules);

  assert.equal(result.hasContradiction, false);
  assert.equal(result.conflicts.length, 0);
});

test("par válido (mesmo vetor, mesmo valor, mesmo operador — duplicata não é contradição) -> nenhuma contradição", () => {
  const rules: RuleCandidate[] = [
    rule({ vector: "Geo - Country", operator: "is", value: "BR" }),
    rule({ vector: "Geo - Country", operator: "is", value: "BR" }),
  ];

  const result = detectContradictions(rules);

  assert.equal(result.hasContradiction, false);
});

test("par válido (OR não forma contradição, mesmo com valores discretos diferentes)", () => {
  const rules: RuleCandidate[] = [
    rule({ vector: "Geo - Country", operator: "is", value: "BR", logicalOp: "OR" }),
    rule({ vector: "Geo - Country", operator: "is", value: "US", logicalOp: "OR" }),
  ];

  const result = detectContradictions(rules);

  assert.equal(result.hasContradiction, false);
});

test("par válido (vetor não-discreto, ex. Site - URL, com operador is e valores diferentes) -> nenhuma contradição", () => {
  const rules: RuleCandidate[] = [
    rule({ vector: "Site - URL", operator: "is", value: "/a" }),
    rule({ vector: "Site - URL", operator: "is", value: "/b" }),
  ];

  const result = detectContradictions(rules);

  assert.equal(result.hasContradiction, false);
});

test("par válido (mesmo vetor discreto, operador contains ao invés de is) -> nenhuma contradição", () => {
  const rules: RuleCandidate[] = [
    rule({ vector: "Client - Useragent", operator: "contains", value: "Chrome" }),
    rule({ vector: "Client - Useragent", operator: "contains", value: "Firefox" }),
  ];

  const result = detectContradictions(rules);

  assert.equal(result.hasContradiction, false);
});

test("conjunto com um par contraditório entre três regras -> detecta exatamente o par exclusivo", () => {
  const rules: RuleCandidate[] = [
    rule({ vector: "Geo - Country", operator: "is", value: "BR" }),
    rule({ vector: "Geo - Country", operator: "is", value: "US" }),
    rule({ vector: "Site - URL", operator: "is", value: "/promo" }),
  ];

  const result = detectContradictions(rules);

  assert.equal(result.hasContradiction, true);
  assert.equal(result.conflicts.length, 1);
  assert.equal(result.conflicts[0]!.ruleA.value, "BR");
  assert.equal(result.conflicts[0]!.ruleB.value, "US");
});

test("lista vazia -> nenhuma contradição", () => {
  const result = detectContradictions([]);
  assert.equal(result.hasContradiction, false);
  assert.equal(result.conflicts.length, 0);
});
