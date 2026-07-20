/**
 * Testes known-answer de detectContradictions (CA-4 / §4.6).
 *
 * "Nunca salvar regra AND mutuamente exclusiva sem alerta anti-contradição"
 * (mandato do frontend-bff-engineer). Sem estes testes, uma mutação que
 * desativa a detecção discreta (ex.: `DISCRETE_EXCLUSIVE_VECTORS.has(...)`
 * -> `false`, ou inverter `a.value !== b.value`) deixa web-ci verde
 * (achado #27) — o builder de segmentação silenciaria o banner sem avisar
 * o usuário, exatamente o cenário que CA-4 proíbe.
 *
 * WAVE 30 (achado contradiction-vector-allowlist-gap): até esta correção,
 * DISCRETE_EXCLUSIVE_VECTORS só cobria 4 dos 6 vetores selecionáveis no
 * builder ("Site - URL" e "Site - Variable" ficavam de fora), e um teste
 * ABAIXO codificava esse buraco como comportamento "válido" — o gate de
 * teste era cúmplice do furo em vez de pegá-lo. Corrigido: contradiction.ts
 * agora expõe SELECTABLE_VECTORS como fonte única (consumida também por
 * app/rules/page.tsx), e o teste "SELECTABLE_VECTORS por inteiro..." abaixo
 * prova por construção que nenhum vetor do builder escapa da checagem.
 */

import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import {
  detectContradictions,
  SELECTABLE_VECTORS,
  type RuleCandidate,
} from "./contradiction.ts";

/**
 * MEDIUM (wave 30 remediação, achado #2 — "a lista de vetores agora existe
 * em TRÊS cópias"): lê o TEXTO-FONTE de bff/src/schemas/config.ts em tempo
 * de teste e extrai o array literal de DeliveryRuleVectorSchema — a
 * fonte-única real (bff/src/lib/contradiction.ts DERIVA dela via
 * `.options`, não é uma cópia independente).
 *
 * Por que ler o TEXTO em vez de `import`: bff/ e web/console/ são pacotes
 * npm separados sem workspace comum, e bff/src/schemas/config.ts usa a
 * convenção NodeNext (`import ... from "./money.js"` apontando para um
 * arquivo .ts real) — o runner nativo do Node (`node --test`, sem
 * transpilação) NÃO resolve essa convenção fora do tsc/ts-jest (comprovado:
 * `node money.js` falha com ERR_MODULE_NOT_FOUND quando só existe money.ts).
 * Importar o módulo cross-package quebraria o web-test nativo. Ler o texto e
 * fazer parse do array evita executar o módulo do BFF (e suas dependências
 * server-only — pg, decimal.js) dentro do processo de teste do console,
 * mantendo o isolamento de pacote, e ainda assim é um GATE EXECUTÁVEL real
 * (não um comentário "mantenha sincronizado"): diverge → falha em web-ci.
 */
function readBffSelectableVectors(): string[] {
  const bffSchemaPath = path.resolve(
    path.dirname(fileURLToPath(import.meta.url)),
    "../../../../bff/src/schemas/config.ts"
  );
  const source = readFileSync(bffSchemaPath, "utf8");
  const match = source.match(
    /DeliveryRuleVectorSchema\s*=\s*z\.enum\(\s*\[([\s\S]*?)\]\s*\)/
  );
  if (!match) {
    throw new Error(
      `Não encontrei "DeliveryRuleVectorSchema = z.enum([...])" em ` +
        `${bffSchemaPath} — o parser de sincronismo deste teste ficou ` +
        `desatualizado (renomeou o schema? mudou a forma da declaração?). ` +
        `Atualize a regex acima, não ignore esta falha.`
    );
  }
  const arrayBody = match[1] ?? "";
  const literals = [...arrayBody.matchAll(/"([^"]*)"/g)].map((m) => m[1] as string);
  if (literals.length === 0) {
    throw new Error(
      `DeliveryRuleVectorSchema em ${bffSchemaPath} não tem nenhum literal ` +
        `de string extraído — parser quebrado ou schema vazio (ambos são bug).`
    );
  }
  return literals;
}

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

test("par contraditório Site - URL (mesma URL não pode ser /a E /b) -> detectado (achado wave 30: era tratado como 'não-discreto' e passava sem alerta)", () => {
  const rules: RuleCandidate[] = [
    rule({ vector: "Site - URL", operator: "is", value: "/a" }),
    rule({ vector: "Site - URL", operator: "is", value: "/b" }),
  ];

  const result = detectContradictions(rules);

  assert.equal(result.hasContradiction, true);
  assert.equal(result.conflicts.length, 1);
  assert.match(result.conflicts[0]!.reason, /Contradição AND/);
});

test("par contraditório Site - Variable (mesma comparação chave=valor não pode ter dois valores) -> detectado (achado wave 30)", () => {
  const rules: RuleCandidate[] = [
    rule({ vector: "Site - Variable", operator: "is", value: "category=sports" }),
    rule({ vector: "Site - Variable", operator: "is", value: "category=finance" }),
  ];

  const result = detectContradictions(rules);

  assert.equal(result.hasContradiction, true);
  assert.equal(result.conflicts.length, 1);
});

test("SELECTABLE_VECTORS por inteiro é discreto-exclusivo sob 'is' — nenhum vetor do builder escapa da checagem (default-deny por construção)", () => {
  for (const vector of SELECTABLE_VECTORS) {
    const rules: RuleCandidate[] = [
      rule({ vector, operator: "is", value: "valor-a" }),
      rule({ vector, operator: "is", value: "valor-b" }),
    ];
    const result = detectContradictions(rules);
    assert.equal(
      result.hasContradiction,
      true,
      `vetor "${vector}" (selecionável no builder) não foi tratado como discreto-exclusivo`
    );
  }
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

test("SELECTABLE_VECTORS não diverge de DeliveryRuleVectorSchema (bff/src/schemas/config.ts, fonte-única) — gate executável, não um comentário", () => {
  const bffVectors = readBffSelectableVectors();
  assert.deepEqual(
    [...SELECTABLE_VECTORS].sort(),
    [...bffVectors].sort(),
    "web/console/src/lib/contradiction.ts SELECTABLE_VECTORS divergiu de " +
      "DeliveryRuleVectorSchema em bff/src/schemas/config.ts (a fonte-única " +
      "que bff/src/lib/contradiction.ts também deriva via `.options`) — " +
      "atualize as duas listas juntas."
  );
});
