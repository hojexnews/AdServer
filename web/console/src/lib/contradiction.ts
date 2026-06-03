/**
 * Validação anti-contradição de regras de segmentação (CA-4 / §4.6).
 *
 * Uma regra AND mutuamente exclusiva com outra regra AND no mesmo owner
 * resulta em zero impressões — o sistema silencia o banner sem aviso.
 * Esta função DETECTA contradições e ALERTA antes de salvar.
 *
 * Contradição = duas regras AND com o mesmo vetor, operador "is",
 * valores diferentes e mutuamente exclusivos.
 *
 * Exemplos de contradição:
 *   vector="Time - Day of Week", op="is", value="monday" AND
 *   vector="Time - Day of Week", op="is", value="tuesday"
 *   → Impossível: um request não pode ser segunda E terça.
 *
 *   vector="Geo - Country", op="is", value="BR" AND
 *   vector="Geo - Country", op="is", value="US"
 *   → Impossível: um request não pode ser do Brasil E dos EUA.
 *
 * Esta validação roda:
 *   1. No builder de segmentação antes de salvar (CA-4)
 *   2. Sobre sugestões da IA (Fase 2 — o mesmo código é reutilizado)
 */

export type LogicalOp = "AND" | "OR";

export interface RuleCandidate {
  vector: string;
  operator: string;
  value: string;
  logicalOp: LogicalOp;
}

export interface ContradictionResult {
  hasContradiction: boolean;
  /** Pares de regras mutuamente exclusivas detectados. */
  conflicts: Array<{
    ruleA: RuleCandidate;
    ruleB: RuleCandidate;
    reason: string;
  }>;
}

/**
 * Vetores com valores discretos mutuamente exclusivos (enum-like).
 * Se duas regras AND usam o mesmo vetor com operador "is" e valores
 * diferentes, são mutuamente exclusivas.
 */
const DISCRETE_EXCLUSIVE_VECTORS = new Set([
  "Time - Day of Week",
  "Geo - Country",
  "Geo - City",
  "Client - Useragent",
]);

/**
 * Detecta contradições em um conjunto de regras de segmentação.
 *
 * Algoritmo O(n²) — aceitável pois regras por owner são tipicamente < 50.
 *
 * @param rules - Regras do owner (campaign ou banner), já filtradas por active.
 * @returns ContradictionResult com lista de conflitos.
 */
export function detectContradictions(
  rules: RuleCandidate[]
): ContradictionResult {
  const conflicts: ContradictionResult["conflicts"] = [];

  // Filtra apenas regras AND (OR não formam contradição entre si)
  const andRules = rules.filter((r) => r.logicalOp === "AND");

  for (let i = 0; i < andRules.length; i++) {
    for (let j = i + 1; j < andRules.length; j++) {
      const a = andRules[i];
      const b = andRules[j];

      if (!a || !b) continue;

      // Mesmo vetor, operador "is", valores diferentes
      if (
        a.vector === b.vector &&
        a.operator === "is" &&
        b.operator === "is" &&
        a.value !== b.value &&
        DISCRETE_EXCLUSIVE_VECTORS.has(a.vector)
      ) {
        conflicts.push({
          ruleA: a,
          ruleB: b,
          reason:
            `Contradição AND: "${a.vector}" não pode ser ` +
            `"${a.value}" E "${b.value}" simultaneamente. ` +
            `O banner nunca será exibido com esta combinação.`,
        });
      }

      // Caso especial: "is" vs "is not" no mesmo valor
      if (
        a.vector === b.vector &&
        a.value === b.value &&
        ((a.operator === "is" && b.operator === "is not") ||
          (a.operator === "is not" && b.operator === "is"))
      ) {
        conflicts.push({
          ruleA: a,
          ruleB: b,
          reason:
            `Contradição AND: "${a.vector}" = "${a.value}" e ` +
            `"${a.vector}" != "${a.value}" são mutuamente exclusivos.`,
        });
      }
    }
  }

  return {
    hasContradiction: conflicts.length > 0,
    conflicts,
  };
}
