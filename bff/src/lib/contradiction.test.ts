/**
 * Testes known-answer de detectContradictions no BFF (CA-4 / §4.6).
 *
 * Espelha web/console/src/lib/contradiction.test.ts — port da mesma lógica
 * (ver o cabeçalho de contradiction.ts). Prova, de forma independente do
 * console, que o backstop server-side de deliveryRule.create (config.ts)
 * detecta a mesma classe de contradição, incluindo os vetores "Site - URL"
 * e "Site - Variable" fechados na wave 30 (achado
 * contradiction-vector-allowlist-gap).
 */

import {
  detectContradictions,
  SELECTABLE_VECTORS,
  type RuleCandidate,
} from "./contradiction.js";

function rule(partial: Partial<RuleCandidate>): RuleCandidate {
  return {
    vector: "Geo - Country",
    operator: "is",
    value: "BR",
    logicalOp: "AND",
    ...partial,
  };
}

describe("detectContradictions (BFF backstop, CA-4)", () => {
  test("par contraditório (mesmo vetor discreto, is, valores diferentes) -> detectado", () => {
    const result = detectContradictions([
      rule({ vector: "Geo - Country", operator: "is", value: "BR" }),
      rule({ vector: "Geo - Country", operator: "is", value: "US" }),
    ]);
    expect(result.hasContradiction).toBe(true);
    expect(result.conflicts).toHaveLength(1);
  });

  test("par contraditório Site - URL -> detectado (achado wave 30)", () => {
    const result = detectContradictions([
      rule({ vector: "Site - URL", operator: "is", value: "/a" }),
      rule({ vector: "Site - URL", operator: "is", value: "/b" }),
    ]);
    expect(result.hasContradiction).toBe(true);
  });

  test("par contraditório Site - Variable -> detectado (achado wave 30)", () => {
    const result = detectContradictions([
      rule({ vector: "Site - Variable", operator: "is", value: "category=sports" }),
      rule({ vector: "Site - Variable", operator: "is", value: "category=finance" }),
    ]);
    expect(result.hasContradiction).toBe(true);
  });

  test("par is vs is not no mesmo valor -> detectado", () => {
    const result = detectContradictions([
      rule({ vector: "Geo - Country", operator: "is", value: "BR" }),
      rule({ vector: "Geo - Country", operator: "is not", value: "BR" }),
    ]);
    expect(result.hasContradiction).toBe(true);
  });

  test("OR não forma contradição", () => {
    const result = detectContradictions([
      rule({ vector: "Geo - Country", operator: "is", value: "BR", logicalOp: "OR" }),
      rule({ vector: "Geo - Country", operator: "is", value: "US", logicalOp: "OR" }),
    ]);
    expect(result.hasContradiction).toBe(false);
  });

  test("SELECTABLE_VECTORS por inteiro é discreto-exclusivo sob 'is' (default-deny por construção)", () => {
    for (const vector of SELECTABLE_VECTORS) {
      const result = detectContradictions([
        rule({ vector, operator: "is", value: "valor-a" }),
        rule({ vector, operator: "is", value: "valor-b" }),
      ]);
      expect(result.hasContradiction).toBe(true);
    }
  });

  test("lista vazia -> nenhuma contradição", () => {
    expect(detectContradictions([]).hasContradiction).toBe(false);
  });
});
