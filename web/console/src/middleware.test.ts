/**
 * Testes de WIRING do middleware de sessão (TX-3/CA-1, G0/frontend E9).
 *
 * session-guard.test.ts (src/lib/session-guard.test.ts) já cobre o
 * PREDICADO puro (sessionConfigError) isoladamente. Este arquivo cobre a
 * camada que faltava: chamar middleware() de verdade — o handler que roda
 * no Edge Runtime em produção — com um NextRequest real, e observar o
 * comportamento fim-a-fim.
 *
 * Sem este arquivo, uma mutação composta que remove:
 *   (a) o guard de topo em middleware() (linhas ~244-254, camada 1: recusa
 *       com 500 antes de qualquer outra lógica), E
 *   (b) o `if (NODE_ENV === "production") return null` dentro de
 *       verifySessionToken() (linhas ~131-133, camada 2)
 * faz com que, em produção sem SESSION_SECRET configurado, um token de
 * sessão FORJADO (formato dev-stub, sem assinatura) seja aceito e o
 * tenant_id forjado seja injetado no header que o BFF confia — bypass
 * completo de tenant via cliente. `make web-test` ficava verde mesmo com
 * essa mutação (achado #14), porque nenhum teste chamava middleware().
 *
 * NOTA DE INFRAESTRUTURA DE TESTE: o console não tem jest/vitest (ver
 * make/web.mk). node:test roda .ts nativamente, mas não resolve, fora do
 * bundler do Next: (1) o subpath "next/server" sem extensão, nem (2) o
 * alias de path "@/*". src/lib/test-node-loader-hook.mjs (test-only)
 * registra um resolve hook mínimo só para esses dois casos — middleware.ts
 * não muda.
 */

import { test } from "node:test";
import assert from "node:assert/strict";
import { register } from "node:module";
import { createHmac } from "node:crypto";
import type { NextRequest } from "next/server.js";

register(new URL("./lib/test-node-loader-hook.mjs", import.meta.url));

type MiddlewareFn = (req: NextRequest) => Promise<Response>;

const PROTECTED_URL = "http://localhost:3000/api/trpc/cfg.advertiser.list";
const COOKIE_NAME = "__Host-sess";
const FORGED_TENANT = "11111111-1111-1111-1111-111111111111";
const FORGED_USER = "22222222-2222-2222-2222-222222222222";
const STRONG_SECRET = "s".repeat(32); // 32 bytes, alinhado a MIN_SESSION_SECRET_BYTES

function base64url(input: string): string {
  return Buffer.from(input, "utf8")
    .toString("base64")
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
}

/** Token no formato dev-stub (SEM assinatura) — o que um atacante forjaria. */
function forgedDevStubToken(tenantId: string, userId: string): string {
  return base64url(JSON.stringify({ tenantId, userId }));
}

/** Token real: base64url(payload) + "." + hex(HMAC-SHA256(payload, secret)). */
function signedToken(tenantId: string, userId: string, secret: string): string {
  const payload = {
    tenantId,
    userId,
    exp: Math.floor(Date.now() / 1000) + 3600,
  };
  const payloadB64 = base64url(JSON.stringify(payload));
  const sig = createHmac("sha256", secret).update(payloadB64).digest("hex");
  return `${payloadB64}.${sig}`;
}

let importSeq = 0;

/**
 * Reimporta middleware.ts do zero (cache-busting via query string).
 * Necessário porque SESSION_SECRET/NODE_ENV são lidos em `process.env` no
 * TOPO do módulo (uma vez, no import) — cada cenário precisa de uma
 * instância nova do módulo para capturar o env correto.
 */
async function freshMiddleware(): Promise<MiddlewareFn> {
  importSeq += 1;
  const mod = (await import(`./middleware.ts?wiring-test=${importSeq}`)) as {
    middleware: MiddlewareFn;
  };
  return mod.middleware;
}

async function withEnv<T>(
  vars: Record<string, string | undefined>,
  fn: () => Promise<T>
): Promise<T> {
  const prev: Record<string, string | undefined> = {};
  for (const key of Object.keys(vars)) {
    prev[key] = process.env[key];
  }
  try {
    for (const [key, val] of Object.entries(vars)) {
      if (val === undefined) delete process.env[key];
      else process.env[key] = val;
    }
    return await fn();
  } finally {
    for (const [key, val] of Object.entries(prev)) {
      if (val === undefined) delete process.env[key];
      else process.env[key] = val;
    }
  }
}

test("producao sem SESSION_SECRET + token forjado (dev-stub) -> NUNCA autentica, tenant forjado nunca propaga", async () => {
  await withEnv(
    { NODE_ENV: "production", SESSION_SECRET: undefined },
    async () => {
      const middleware = await freshMiddleware();
      const { NextRequest: NextReq } = await import("next/server.js");
      const req = new NextReq(PROTECTED_URL, {
        headers: {
          cookie: `${COOKIE_NAME}=${forgedDevStubToken(FORGED_TENANT, FORGED_USER)}`,
        },
      });

      const res = await middleware(req);

      // Nunca pode ser um pass-through (x-middleware-next = "1") com o
      // tenant forjado propagado para o header que o BFF confia.
      assert.notEqual(res.headers.get("x-middleware-next"), "1");
      assert.equal(
        res.headers.get("x-middleware-request-x-adserver-session-tenant"),
        null
      );
      assert.ok(
        res.status === 500 || res.status === 401,
        `esperava 500 (camada 1) ou 401 (camada 2), recebeu ${res.status}`
      );
    }
  );
});

test("producao sem SESSION_SECRET -> camada 1 recusa com 500 antes de qualquer outra logica", async () => {
  await withEnv(
    { NODE_ENV: "production", SESSION_SECRET: undefined },
    async () => {
      const middleware = await freshMiddleware();
      const { NextRequest: NextReq } = await import("next/server.js");
      const req = new NextReq(PROTECTED_URL); // sem cookie algum
      const res = await middleware(req);
      assert.equal(res.status, 500);
    }
  );
});

test("producao com SESSION_SECRET forte + cookie assinado valido -> autentica e injeta o tenant real (caminho feliz)", async () => {
  await withEnv(
    { NODE_ENV: "production", SESSION_SECRET: STRONG_SECRET },
    async () => {
      const middleware = await freshMiddleware();
      const { NextRequest: NextReq } = await import("next/server.js");
      const token = signedToken(FORGED_TENANT, FORGED_USER, STRONG_SECRET);
      const req = new NextReq(PROTECTED_URL, {
        headers: { cookie: `${COOKIE_NAME}=${token}` },
      });

      const res = await middleware(req);

      assert.equal(res.headers.get("x-middleware-next"), "1");
      assert.equal(
        res.headers.get("x-middleware-request-x-adserver-session-tenant"),
        FORGED_TENANT
      );
    }
  );
});

test("dev (sem SESSION_SECRET) + token dev-stub -> autentica normalmente (comportamento fora de producao preservado)", async () => {
  await withEnv(
    { NODE_ENV: "development", SESSION_SECRET: undefined },
    async () => {
      const middleware = await freshMiddleware();
      const { NextRequest: NextReq } = await import("next/server.js");
      const req = new NextReq(PROTECTED_URL, {
        headers: {
          cookie: `${COOKIE_NAME}=${forgedDevStubToken(FORGED_TENANT, FORGED_USER)}`,
        },
      });

      const res = await middleware(req);

      assert.equal(res.headers.get("x-middleware-next"), "1");
      assert.equal(
        res.headers.get("x-middleware-request-x-adserver-session-tenant"),
        FORGED_TENANT
      );
    }
  );
});
