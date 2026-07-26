/**
 * Testes de lib/dashboard-source-state.ts — remediação H-3 (onda "perfil
 * BETA", ADR-0005): a seção "Dados consolidados" do dashboard NUNCA pode
 * renderizar "Sem dados consolidados no período" quando a fonte está
 * indisponível (consolidatedUnavailable não-nulo) — isso é uma meia-verdade
 * fabricada, a mesma classe que a 31ª onda removeu do dashboard.
 *
 * `web/console/src/app/dashboard/page.tsx` importa e usa a MESMA função
 * testada aqui — não há reimplementação a divergir.
 */

import { test } from "node:test";
import assert from "node:assert/strict";
import { consolidatedSectionState } from "./dashboard-source-state.ts";

test("consolidatedUnavailable não-nulo -> kind='unavailable' com a mensagem real, mesmo com consolidated=[]", () => {
  const result = consolidatedSectionState({
    consolidatedUnavailable:
      "Métricas indisponíveis: nenhum backend de estatísticas está configurado neste ambiente.",
    consolidated: [],
  });
  assert.deepEqual(result, {
    kind: "unavailable",
    message:
      "Métricas indisponíveis: nenhum backend de estatísticas está configurado neste ambiente.",
  });
});

test("consolidatedUnavailable=null + consolidated=[] -> kind='empty' (CA-6 legítimo, zero eventos no período)", () => {
  const result = consolidatedSectionState({
    consolidatedUnavailable: null,
    consolidated: [],
  });
  assert.deepEqual(result, { kind: "empty" });
});

test("consolidatedUnavailable=null + consolidated com linhas -> kind='rows'", () => {
  const result = consolidatedSectionState({
    consolidatedUnavailable: null,
    consolidated: [{ source: "consolidated" }],
  });
  assert.deepEqual(result, { kind: "rows" });
});

test("consolidatedUnavailable não-nulo VENCE mesmo se, por algum bug futuro do BFF, consolidated vier com linhas — a recusa é estrutural, não inferida do tamanho do array", () => {
  const result = consolidatedSectionState({
    consolidatedUnavailable: "fonte indisponível",
    consolidated: [{ source: "consolidated" }, { source: "consolidated" }],
  });
  assert.equal(result.kind, "unavailable");
});
