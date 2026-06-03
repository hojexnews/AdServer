/**
 * Tipos derivados do AppRouter do BFF (fonte única — type-only).
 *
 * Importação type-only: zero código de runtime no bundle do cliente.
 * O path alias no tsconfig aponta para o bff/src/index.ts.
 *
 * Em produção com npm workspaces, isso seria:
 *   import type { AppRouter } from "@adserver/bff";
 * Para Fase 1 (I4), usamos path alias relativo.
 */

// Reexporta apenas o tipo — o tRPC usa isso para inferência client/server
export type { AppRouter } from "../../../../bff/src/index";
