import { notFound } from "next/navigation";
import { A11yHarness } from "./harness-client";

/**
 * Harness de acessibilidade — rota CI/dev-only (G0/frontend E10).
 *
 * Monta os componentes REAIS do design system com props-fixture, de forma
 * determinística e client-only (sem BFF/tRPC vivo — nenhuma requisição de
 * rede dispara no mount; escrita só ocorreria com clique explícito, que o
 * harness nunca simula).
 *
 * GATE DE PRODUÇÃO (obrigatório): esta página só renderiza se a env var
 * A11Y_HARNESS estiver setada como "1" no momento do build. Sem ela, a
 * rota 404 sempre — inclusive em produção real, onde A11Y_HARNESS nunca é
 * definida. `make web-a11y` builda explicitamente com A11Y_HARNESS=1; todo
 * outro alvo (`web-build`, deploy real) não define a env var.
 */
export default function A11yHarnessPage() {
  if (process.env.A11Y_HARNESS !== "1") {
    notFound();
  }

  return <A11yHarness />;
}
