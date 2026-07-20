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
 *
 * A UI SOZINHA não é fronteira de confiança (CA-4 exige aplicação real, não
 * cosmética): bff/src/lib/contradiction.ts é um port desta MESMA lógica que
 * roda como backstop server-side em deliveryRule.create/update.
 *
 * "Mantenha os dois arquivos sincronizados" NÃO é uma promessa de comentário
 * — é um GATE EXECUTÁVEL. A lista de vetores (SELECTABLE_VECTORS) não pode
 * dessincronizar silenciosamente entre os dois pacotes:
 *   - No lado BFF, SELECTABLE_VECTORS é DERIVADA de DeliveryRuleVectorSchema
 *     (bff/src/schemas/config.ts, o enum Zod que valida o input real do
 *     router) via `.options` — não é uma cópia manual, é o próprio enum.
 *     DISCRETE_EXCLUSIVE_VECTORS lá usa `satisfies Record<SelectableVector,
 *     boolean>`, então esquecer de classificar um vetor novo é erro de
 *     `tsc`, não um buraco silencioso.
 *   - Neste lado (console), SELECTABLE_VECTORS abaixo continua sendo uma
 *     lista própria (bff/ e web/console/ são pacotes npm separados sem
 *     workspace comum — o console não pode importar código server-only do
 *     BFF para o bundle do cliente). O que fecha o risco de divergência é
 *     contradiction.test.ts: o teste "SELECTABLE_VECTORS não diverge de
 *     DeliveryRuleVectorSchema" lê o TEXTO-FONTE de
 *     bff/src/schemas/config.ts em tempo de teste, extrai o array literal
 *     do enum e compara contra SELECTABLE_VECTORS aqui — se alguém editar
 *     um lado sem editar o outro, web-ci fica VERMELHO.
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
 * Vetores selecionáveis no builder de segmentação (app/rules/page.tsx).
 *
 * FONTE ÚNICA DE VERDADE (wave 30, achado contradiction-vector-allowlist-gap):
 * antes desta correção, app/rules/page.tsx mantinha seu PRÓPRIO array
 * `VECTORS` (6 opções) duplicado deste módulo, e DISCRETE_EXCLUSIVE_VECTORS
 * abaixo só cobria 4/6 — "Site - URL" e "Site - Variable" ficavam de fora.
 * Duas regras AND "Site - URL is /a" + "Site - URL is /b" (zero delivery
 * possível sob operador de igualdade) passavam sem alerta algum, e o teste
 * antigo CODIFICAVA esse buraco como comportamento "válido"
 * (contradiction.test.ts, caso "vetor não-discreto, ex. Site - URL").
 *
 * Agora app/rules/page.tsx IMPORTA SELECTABLE_VECTORS (schema Zod do form e
 * <select> de vetor) em vez de manter sua própria lista — um vetor novo
 * adicionado aqui aparece automaticamente no builder E na checagem de
 * exclusividade abaixo. Não há mais duas listas para dessincronizar.
 */
export const SELECTABLE_VECTORS = [
  "Time - Day of Week",
  "Site - URL",
  "Geo - Country",
  "Geo - City",
  "Client - Useragent",
  "Site - Variable",
] as const;

export type SelectableVector = (typeof SELECTABLE_VECTORS)[number];

/**
 * Vetores com valores discretos mutuamente exclusivos (enum-like).
 * Se duas regras AND usam o mesmo vetor com operador "is" e valores
 * diferentes, são mutuamente exclusivas.
 *
 * DEFAULT-DENY: hoje TODOS os vetores selecionáveis no builder são
 * enum-like sob o operador de igualdade "is" (mesmo "Site - Variable",
 * que representa uma comparação exata chave=valor) — por isso o conjunto
 * abaixo é SELECTABLE_VECTORS por inteiro. Se um vetor genuinamente
 * NÃO-discreto for adicionado no futuro (ex.: um contador contínuo onde
 * "is" nunca é exato), remova-o daqui EXPLICITAMENTE com uma justificativa
 * no comentário — nunca por omissão silenciosa. O padrão é incluir
 * (fail-safe: um falso-positivo gera um alerta dispensável; um
 * falso-negativo salva uma regra AND que nunca entrega, violando CA-4).
 */
const DISCRETE_EXCLUSIVE_VECTORS = new Set<string>(SELECTABLE_VECTORS);

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
