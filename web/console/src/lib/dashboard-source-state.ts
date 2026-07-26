/**
 * Decisão pura de renderização da seção "Dados consolidados" do dashboard.
 *
 * POR QUE ISTO EXISTE (remediação H-3, onda "perfil BETA", ADR-0005):
 *
 * `bff/src/routers/stats.ts` trocou `Promise.all` por `Promise.allSettled`
 * para que a recusa fail-closed de `queryConsolidated` (nenhum backend de
 * estatísticas configurado — ver adapters/unconfigured-stats.ts) não
 * derrubasse a série "ao vivo" real. Efeito colateral não previsto: o campo
 * `consolidatedUnavailable` (mensagem real do erro, ver bff/src/routers/
 * stats.ts) passou a ser produzido e validado no schema, mas
 * `web/console/src/app/dashboard/page.tsx` nunca o lia — a UI renderizava o
 * MESMO `EmptyState` ("Sem dados consolidados no período") tanto para "a
 * fonte não existe neste perfil" quanto para "a fonte existe e retornou zero
 * linhas". A primeira é uma recusa fail-closed que precisa ser visível; a
 * segunda é CA-6 legítimo. Confundir as duas é a mesma classe de meia-verdade
 * que a 31ª onda removeu do dashboard (dado fabricado apresentado como
 * ausência de dado).
 *
 * Esta função é extraída como lógica PURA (mesmo padrão de
 * lib/session-guard.ts e adapters/unconfigured-stats.ts#syntheticStatsAllowed)
 * para ser testável por `make web-test` (node:test nativo) SEM precisar de
 * um harness de renderização de componentes — este repo não tem
 * jest/RTL/vitest instalado para o console (ver make/web.mk#web-test).
 * `web/console/src/app/dashboard/page.tsx` importa e usa ESTA MESMA função
 * (não uma reimplementação) para decidir o que renderizar — mutar a função
 * aqui é mutar o comportamento real da página.
 */

export type ConsolidatedSectionState =
  /** Fonte consolidada NÃO PÔDE ser consultada — mensagem real do backend. */
  | { kind: "unavailable"; message: string }
  /** Fonte consultada com sucesso; zero linhas no período (CA-6 legítimo). */
  | { kind: "empty" }
  /** Fonte consultada com sucesso; há linhas para exibir. */
  | { kind: "rows" };

/**
 * `consolidatedUnavailable` não-nulo SEMPRE vence — é estruturalmente
 * diferente de "zero linhas no período" mesmo quando `consolidated` também
 * vier vazio (é o caso normal: quando a fonte falha, o router de fato
 * retorna `consolidated: []`, ver bff/src/routers/stats.ts).
 */
export function consolidatedSectionState(data: {
  consolidatedUnavailable: string | null;
  consolidated: readonly unknown[];
}): ConsolidatedSectionState {
  if (data.consolidatedUnavailable !== null) {
    return { kind: "unavailable", message: data.consolidatedUnavailable };
  }
  if (data.consolidated.length === 0) {
    return { kind: "empty" };
  }
  return { kind: "rows" };
}
