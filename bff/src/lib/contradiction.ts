/**
 * Backstop SERVER-SIDE (CA-4) da validação anti-contradição de regras.
 *
 * PORT de web/console/src/lib/contradiction.ts — a LÓGICA de detecção
 * (detectContradictions) é replicada porque bff/ e web/console/ são pacotes
 * npm separados, sem workspace comum neste repo, e o console não pode
 * importar código de produção do BFF em runtime (isso vazaria dependências
 * server-only — pg, schemas com Money etc. — para o bundle do cliente).
 *
 * O que este arquivo NÃO replica mais por cópia manual: a LISTA de vetores.
 * SELECTABLE_VECTORS abaixo é DERIVADA de DeliveryRuleVectorSchema (o enum
 * Zod que bff/src/schemas/config.ts usa para validar todo input de
 * deliveryRule — a fonte-única-da-verdade real, não uma cópia dela). Um
 * vetor novo adicionado a esse enum aparece aqui automaticamente, por
 * construção — não depende de alguém lembrar de atualizar uma segunda
 * lista (achado wave 30 remediação: "contradiction-vector-allowlist-gap"
 * reaberto um nível acima, com SELECTABLE_VECTORS/DISCRETE_EXCLUSIVE_VECTORS
 * copiados manualmente do enum em vez de derivados dele).
 *
 * web/console/src/lib/contradiction.ts continua com sua PRÓPRIA cópia da
 * lista (por isolamento de bundle — ver acima). web/console/src/lib/
 * contradiction.test.ts NÃO importa este módulo (o `node --test` nativo do
 * console não resolve a convenção NodeNext `./schemas/config.js` → .ts
 * usada aqui fora do tsc/ts-jest — importar quebraria web-test); em vez
 * disso lê o TEXTO-FONTE de bff/src/schemas/config.ts em tempo de teste e
 * faz parse do array literal de DeliveryRuleVectorSchema, comparando contra
 * SELECTABLE_VECTORS do console. Mesmo efeito (falha se as duas listas
 * divergirem — gate executável, não um comentário "mantenha sincronizado"),
 * sem executar o módulo do BFF (e suas dependências server-only) dentro do
 * processo de teste do console.
 *
 * MOTIVO do backstop em si (wave 30, achado contradiction-vector-allowlist-
 * gap): a orientação do tech lead é explícita — "CA-4 é critério de
 * aceitação, e a UI sozinha não é fronteira de confiança". O builder de
 * segmentação (app/rules/page.tsx) já roda detectContradictions() no
 * cliente antes de enviar a mutação, mas isso é só UX (alerta o usuário,
 * permite corrigir). Sem um backstop server-side, uma regra AND mutuamente
 * exclusiva pode ser persistida por:
 *   - uma chamada tRPC direta (bypassa a UI inteiramente);
 *   - um bug futuro no builder que esqueça de chamar a validação;
 *   - um caminho de escrita que não seja app/rules/page.tsx.
 *
 * bff/src/routers/config.ts (deliveryRule.create E deliveryRule.update)
 * chama detectContradictions aqui ANTES de persistir: se houver conflito
 * com uma regra AND já existente do mesmo owner e o cliente não confirmou
 * explicitamente (acknowledgeContradiction=true no input — o mesmo padrão
 * do botão "Salvar mesmo assim" do builder), rejeita com TRPCError CONFLICT
 * contendo o motivo. O front usa isso para mostrar o mesmo banner de alerta
 * e reenviar com o flag após confirmação do usuário.
 */

import { DeliveryRuleVectorSchema } from "../schemas/config.js";

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
 * Vetores aceitos por deliveryRule.create/update — DERIVADO de
 * DeliveryRuleVectorSchema (bff/src/schemas/config.ts), o enum Zod que de
 * fato valida o input do router. Isto NÃO é uma cópia: é o próprio conjunto
 * de valores do enum (`.options`), então um vetor novo adicionado ao schema
 * aparece aqui automaticamente, sem edição manual deste arquivo.
 */
export const SELECTABLE_VECTORS = DeliveryRuleVectorSchema.options;

export type SelectableVector = (typeof SELECTABLE_VECTORS)[number];

/**
 * Classificação EXAUSTIVA, por vetor, de "valores discretos mutuamente
 * exclusivos sob o operador 'is'" (enum-like) vs. não-discreto.
 *
 * DEFAULT-DENY POR TIPO, não por convenção: `satisfies Record<SelectableVector,
 * boolean>` obriga TypeScript a recusar a compilação se:
 *   (a) um vetor NOVO for adicionado a DeliveryRuleVectorSchema e este objeto
 *       não ganhar uma entrada correspondente (propriedade faltando —
 *       Record<K, V> exige TODAS as chaves de K); ou
 *   (b) uma chave for adicionada aqui que não existe em SelectableVector
 *       (excess property check do `satisfies` sobre um Record fechado).
 * Isto fecha a FORMA do defeito original (wave 30,
 * contradiction-vector-allowlist-gap): SELECTABLE_VECTORS/
 * DISCRETE_EXCLUSIVE_VECTORS eram cópias manuais do enum de produção que
 * "esquecer de listar" um vetor novo silenciosamente. Agora esquecer é um
 * erro de `tsc`/`npm run typecheck` (bff-ci), não um buraco silencioso em
 * runtime — não depende de ninguém lembrar de rodar um teste.
 *
 * Hoje TODOS os vetores são enum-like sob "is" — cada entrada abaixo é
 * `true` com justificativa. Fail-safe: um falso-positivo (`true` para um
 * vetor não-discreto) gera um CONFLICT dispensável (o cliente reenvia com
 * acknowledgeContradiction=true); um falso-negativo (`false` para um vetor
 * discreto) persiste uma regra AND que nunca entrega, violando CA-4 — por
 * isso o padrão ao classificar um vetor novo deve ser `true` a menos que
 * haja uma razão explícita em contrário.
 */
const VECTOR_IS_DISCRETE_EXCLUSIVE = {
  "Time - Day of Week": true, // dia da semana é enum fechado (7 valores)
  "Site - URL": true, // "is" compara igualdade exata de URL/path
  "Geo - Country": true, // país sob "is" é comparação exata (ISO)
  "Geo - City": true, // cidade sob "is" é comparação exata
  "Client - Useragent": true, // "is" (não "contains") é comparação exata
  "Site - Variable": true, // chave=valor sob "is" é comparação exata
} satisfies Record<SelectableVector, boolean>;

const DISCRETE_EXCLUSIVE_VECTORS = new Set<string>(
  SELECTABLE_VECTORS.filter((v) => VECTOR_IS_DISCRETE_EXCLUSIVE[v])
);

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
