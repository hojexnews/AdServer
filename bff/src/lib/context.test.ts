/**
 * Testes da fronteira de ACL server-side do BFF (CA-1 / TX-3).
 *
 * Cobre createContext():
 *   1. tenant_id é resolvido EXCLUSIVAMENTE da sessão (header assinado pelo
 *      middleware Next.js), nunca aceito silenciosamente quando ausente/inválido.
 *   2. CSRF (M1): mutations (POST) exigem Origin na allow-list E
 *      X-Requested-With; queries (GET) não passam por esse check.
 *
 * Sem estes testes, remover o guard de sessão (fallback silencioso de
 * tenant_id) ou o bloco de CSRF inteiro deixa o bff-ci verde (achado #26).
 */

import { TRPCError } from "@trpc/server";
import { createContext } from "./context.js";

const VALID_TENANT = "11111111-1111-1111-1111-111111111111";
const ALLOWED_ORIGIN = "http://localhost:3000"; // default de ALLOWED_ORIGINS em CI/dev

function expectTrpcError(fn: () => unknown, code: string): void {
  try {
    fn();
    throw new Error("esperava TRPCError, mas nenhum erro foi lançado");
  } catch (err) {
    expect(err).toBeInstanceOf(TRPCError);
    expect((err as TRPCError).code).toBe(code);
  }
}

describe("createContext — resolução de tenant_id (CA-1/TX-3)", () => {
  test("sessão ausente (sem header de tenant) -> UNAUTHORIZED, nunca cai em fallback", () => {
    expectTrpcError(
      () => createContext({ req: { headers: {}, method: "GET" } }),
      "UNAUTHORIZED"
    );
  });

  test("header de tenant malformado (não-UUID) -> UNAUTHORIZED, nunca aceito como-está", () => {
    expectTrpcError(
      () =>
        createContext({
          req: {
            headers: { "x-adserver-session-tenant": "'; DROP TABLE--" },
            method: "GET",
          },
        }),
      "UNAUTHORIZED"
    );
  });

  test("sessão válida -> tenantId resolvido do header assinado pelo middleware", () => {
    const ctx = createContext({
      req: {
        headers: {
          "x-adserver-session-tenant": VALID_TENANT,
          "x-adserver-session-user": "user-1",
        },
        method: "GET",
      },
    });
    expect(ctx.tenantId).toBe(VALID_TENANT);
    expect(ctx.userId).toBe("user-1");
  });

  test("array de headers usa o primeiro valor (nunca concatena múltiplos tenants)", () => {
    const ctx = createContext({
      req: {
        headers: {
          "x-adserver-session-tenant": [VALID_TENANT, "22222222-2222-2222-2222-222222222222"],
        },
        method: "GET",
      },
    });
    expect(ctx.tenantId).toBe(VALID_TENANT);
  });
});

describe("createContext — CSRF em mutations (M1)", () => {
  test("POST sem Origin nem X-Requested-With -> FORBIDDEN mesmo com sessão válida", () => {
    expectTrpcError(
      () =>
        createContext({
          req: {
            headers: { "x-adserver-session-tenant": VALID_TENANT },
            method: "POST",
          },
        }),
      "FORBIDDEN"
    );
  });

  test("POST com Origin fora da allow-list -> FORBIDDEN (CSRF cross-site rejeitado)", () => {
    expectTrpcError(
      () =>
        createContext({
          req: {
            headers: {
              "x-adserver-session-tenant": VALID_TENANT,
              origin: "https://evil.example",
              "x-requested-with": "XMLHttpRequest",
            },
            method: "POST",
          },
        }),
      "FORBIDDEN"
    );
  });

  test("POST com Origin permitida mas sem X-Requested-With -> FORBIDDEN (segunda linha de defesa)", () => {
    expectTrpcError(
      () =>
        createContext({
          req: {
            headers: {
              "x-adserver-session-tenant": VALID_TENANT,
              origin: ALLOWED_ORIGIN,
            },
            method: "POST",
          },
        }),
      "FORBIDDEN"
    );
  });

  test("POST com Origin permitida + X-Requested-With -> passa CSRF e resolve tenant normalmente", () => {
    const ctx = createContext({
      req: {
        headers: {
          "x-adserver-session-tenant": VALID_TENANT,
          origin: ALLOWED_ORIGIN,
          "x-requested-with": "XMLHttpRequest",
        },
        method: "POST",
      },
    });
    expect(ctx.tenantId).toBe(VALID_TENANT);
  });

  test("GET (query) não passa pelo check de CSRF — sem Origin/X-Requested-With ainda resolve", () => {
    const ctx = createContext({
      req: {
        headers: { "x-adserver-session-tenant": VALID_TENANT },
        method: "GET",
      },
    });
    expect(ctx.tenantId).toBe(VALID_TENANT);
  });
});
