/**
 * Cliente tRPC para o console — conecta ao BFF.
 *
 * SEGURANÇA (CA-1): o cliente NUNCA envia tenant_id.
 * O BFF resolve tenant_id da sessão HTTP-only server-side.
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
        // Sem envio de tenant_id — resolvido server-side no BFF
        headers() {
          return {};
        },
      }),
    ],
  });
}
