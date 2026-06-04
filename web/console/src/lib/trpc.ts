/**
 * Cliente tRPC para o console — conecta ao BFF.
 *
 * SEGURANÇA (CA-1): o cliente NUNCA envia tenant_id.
 * O BFF resolve tenant_id da sessão HTTP-only server-side.
 *
 * CSRF (M1): o cliente envia X-Requested-With: XMLHttpRequest em todas as
 * chamadas tRPC. O BFF (bff/src/lib/context.ts) exige esse header em mutations
 * POST como segunda linha de defesa CSRF (browsers cross-site não enviam
 * headers customizados sem preflight CORS).
 *
 * Money: toda quantia monetária vem do BFF como string DECIMAL + currency.
 * O cliente usa formatMoney() de @/lib/money para exibir — nunca Number().
 */

import { createTRPCReact } from "@trpc/react-query";
import { httpBatchLink } from "@trpc/client";
import type { AppRouter } from "../types/bff";

export const trpc = createTRPCReact<AppRouter>();

export function createTrpcClient() {
  return trpc.createClient({
    links: [
      httpBatchLink({
        url: "/api/trpc",
        headers() {
          return {
            // CSRF defense-in-depth (M1):
            // browsers cross-site não enviam headers customizados sem CORS preflight.
            // O BFF rejeita mutations POST que não carreguem este header.
            "X-Requested-With": "XMLHttpRequest",
            // Sem envio de tenant_id — resolvido server-side no BFF
          };
        },
      }),
    ],
  });
}
