/**
 * UnconfiguredStatsAdapter — fail-closed quando não há backend real de stats.
 *
 * POR QUE ISTO EXISTE (achado da 31ª onda, "/dashboard serve dado fabricado"):
 *
 * `bff/src/index.ts` instanciava `new InMemoryStatsAdapter()` INCONDICIONALMENTE
 * — sem o ramo `BFF_PG_DSN ? real : stub` que config e payments já têm. O
 * InMemoryStatsAdapter gera os números com `Math.random()`. Consequência: em
 * QUALQUER ambiente, inclusive produção com banco real configurado, o
 * `/dashboard` do anunciante exibia métricas inventadas — e a UI as rotula com
 * `DataSourceBadge source="consolidated"`, cujo texto é "Consolidado ≤1h" e
 * cujo aria-label diz **"faturável"** (web/console/src/components/ui/status-badge.tsx).
 *
 * Isto é pior que um bug de exibição: apresenta ficção como base de cobrança.
 * Um anunciante poderia conciliar fatura contra números que nunca existiram.
 *
 * NÃO existe hoje um ClickHouseStatsAdapter no repo — o backend real é item de
 * infra (addon §3.6 E11 / §3.3 E8), fora do alcance de uma correção de código.
 * O que ESTÁ ao alcance é recusar-se a fabricar: em produção, sem backend real,
 * o dashboard falha de forma visível e diagnosticável em vez de mentir.
 *
 * Mesma disciplina do fail-closed de SESSION_SECRET no middleware do console
 * (G0/frontend E9): a ausência de configuração nunca pode degradar
 * silenciosamente para um caminho de dev.
 *
 * Escape hatch explícito: `ALLOW_SYNTHETIC_STATS=1` reativa o stub sintético
 * mesmo em produção — para demo/staging onde a ficção é intencional e sabida.
 * É opt-in por variável de ambiente, nunca o default.
 */

import { TRPCError } from "@trpc/server";
import type { StatsAdapter } from "./stats-adapter.js";
import type { KpiRow } from "../schemas/stats.js";

const MESSAGE =
  "Métricas indisponíveis: nenhum backend de estatísticas (ClickHouse) está " +
  "configurado neste ambiente. O dashboard não exibe números sintéticos em " +
  "produção porque eles são rotulados como consolidados/faturáveis. " +
  "Configure o adapter real ou defina ALLOW_SYNTHETIC_STATS=1 para um " +
  "ambiente de demonstração.";

export class UnconfiguredStatsAdapter implements StatsAdapter {
  queryConsolidated(): Promise<{ rows: KpiRow[]; asOf: Date | null }> {
    return Promise.reject(
      new TRPCError({ code: "PRECONDITION_FAILED", message: MESSAGE })
    );
  }

  queryLive(): Promise<{ rows: KpiRow[]; asOf: Date | null }> {
    return Promise.reject(
      new TRPCError({ code: "PRECONDITION_FAILED", message: MESSAGE })
    );
  }
}

/**
 * Decide se o stub sintético pode ser servido neste ambiente.
 *
 * Função pura para ser testável sem montar o processo (ver
 * in-memory-stats.test.ts). Fora de produção o stub é legítimo (dev/CI);
 * em produção só com opt-in explícito.
 */
export function syntheticStatsAllowed(
  nodeEnv: string | undefined,
  allowSyntheticStats: string | undefined
): boolean {
  // Opt-in explícito vence sempre (demo/staging).
  if (allowSyntheticStats === "1") return true;

  // ALLOWLIST, não denylist (corrigido pela revisão adversarial do próprio
  // diff da 31ª onda — achado STATS-FAILOPEN-NODE-ENV).
  //
  // A primeira versão era `if (nodeEnv !== "production") return true`, ou seja,
  // NODE_ENV ausente ⇒ dado sintético PERMITIDO. Isso é fail-OPEN justamente no
  // caso mais provável: não existe Dockerfile do BFF neste repo, nem script de
  // start que exporte NODE_ENV, então um BFF subindo em staging/produção com
  // `node dist/index.js` e BFF_PG_DSN real teria NODE_ENV=undefined — e voltaria
  // a servir Math.random() rotulado como "Consolidado ≤1h / faturável". A
  // proteção falharia exatamente onde precisava funcionar.
  //
  // Agora só os ambientes que se declaram de desenvolvimento/teste ganham o
  // stub. Ambiguidade (NODE_ENV ausente ou desconhecido) ⇒ recusa.
  return nodeEnv === "development" || nodeEnv === "test";
}
