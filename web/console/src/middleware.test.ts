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

// ---------------------------------------------------------------------------
// DENYLIST de headers forjados pelo cliente (31ª onda)
//
// Achado denylist-header-sem-teste / middleware-denylist-csrf-untested: a
// denylist do passo 1 (remover x-adserver-*, x-tenant-*, x-internal-* vindos do
// browser) é a defesa que impede o cliente de injetar tenant_id direto no header
// que o BFF confia — e NÃO tinha nenhum teste. Apagar o laço de `delete` deixava
// `make web-test` verde.
//
// MUTAÇÃO: remover o `cleanedHeaders.delete(key)` de middleware.ts faz o
// primeiro teste abaixo FALHAR.
// ---------------------------------------------------------------------------

test("denylist: x-adserver-session-tenant enviado pelo BROWSER é sobrescrito pelo tenant da sessão", async () => {
  await withEnv(
    { NODE_ENV: "production", SESSION_SECRET: STRONG_SECRET },
    async () => {
      const middleware = await freshMiddleware();
      const { NextRequest: NextReq } = await import("next/server.js");
      const REAL_TENANT = "33333333-3333-3333-3333-333333333333";
      const REAL_USER = "44444444-4444-4444-4444-444444444444";

      const req = new NextReq(PROTECTED_URL, {
        headers: {
          cookie: `${COOKIE_NAME}=${signedToken(REAL_TENANT, REAL_USER, STRONG_SECRET)}`,
          // O atacante tenta injetar OUTRO tenant direto no header interno.
          "x-adserver-session-tenant": FORGED_TENANT,
          "x-adserver-session-user": FORGED_USER,
        },
      });

      const res = await middleware(req);

      assert.equal(res.headers.get("x-middleware-next"), "1");
      assert.equal(
        res.headers.get("x-middleware-request-x-adserver-session-tenant"),
        REAL_TENANT,
        "o tenant que chega ao BFF tem de vir do cookie assinado, nunca do header do browser",
      );
      assert.notEqual(
        res.headers.get("x-middleware-request-x-adserver-session-tenant"),
        FORGED_TENANT,
      );
    },
  );
});

test("denylist: x-tenant-* e x-internal-* do browser NUNCA sobrevivem ao middleware", async () => {
  await withEnv(
    { NODE_ENV: "production", SESSION_SECRET: STRONG_SECRET },
    async () => {
      const middleware = await freshMiddleware();
      const { NextRequest: NextReq } = await import("next/server.js");
      const REAL_TENANT = "33333333-3333-3333-3333-333333333333";

      const req = new NextReq(PROTECTED_URL, {
        headers: {
          cookie: `${COOKIE_NAME}=${signedToken(REAL_TENANT, REAL_TENANT, STRONG_SECRET)}`,
          "x-tenant-id": FORGED_TENANT,
          // Assinatura interna console→copiloto: se o browser pudesse definir,
          // falaria com o serviço interno se passando pelo console.
          "x-internal-signature": "forjada",
        },
      });

      const res = await middleware(req);

      assert.equal(
        res.headers.get("x-middleware-request-x-tenant-id"),
        null,
        "x-tenant-* do browser tem de ser removido antes de qualquer handler",
      );
      assert.equal(
        res.headers.get("x-middleware-request-x-internal-signature"),
        null,
        "x-internal-* do browser tem de ser removido antes de qualquer handler",
      );
    },
  );
});

// ---------------------------------------------------------------------------
// CSRF (31ª onda, achado copilot-stream-get-csrf)
//
// A condição antiga era `origin !== null && !ALLOWED_ORIGINS.has(origin)`:
// requisição SEM Origin passava direto. Navegadores omitem Origin em GET
// same-origin, então GET /api/copilot/stream/:id?message=... — que dispara
// chamada PAGA de LLM — não tinha checagem alguma.
//
// MUTAÇÃO: reintroduzir o `origin !== null &&` no guard de método não-seguro
// faz o teste de POST-sem-Origin FALHAR.
// ---------------------------------------------------------------------------

const COPILOT_URL =
  "http://localhost:3000/api/copilot/stream/55555555-5555-5555-5555-555555555555";

test("CSRF: Sec-Fetch-Site cross-site -> 403 mesmo em GET (EventSource forjado de outro site)", async () => {
  await withEnv(
    { NODE_ENV: "production", SESSION_SECRET: STRONG_SECRET, ALLOWED_ORIGINS: "https://app.hojex.com" },
    async () => {
      const middleware = await freshMiddleware();
      const { NextRequest: NextReq } = await import("next/server.js");
      const REAL_TENANT = "33333333-3333-3333-3333-333333333333";

      const req = new NextReq(COPILOT_URL, {
        headers: {
          cookie: `${COOKIE_NAME}=${signedToken(REAL_TENANT, REAL_TENANT, STRONG_SECRET)}`,
          "sec-fetch-site": "cross-site",
        },
      });

      const res = await middleware(req);
      assert.equal(res.status, 403);
    },
  );
});

test("CSRF: método de mutação SEM header Origin -> 403 (fail-closed; antes passava)", async () => {
  await withEnv(
    { NODE_ENV: "production", SESSION_SECRET: STRONG_SECRET, ALLOWED_ORIGINS: "https://app.hojex.com" },
    async () => {
      const middleware = await freshMiddleware();
      const { NextRequest: NextReq } = await import("next/server.js");
      const REAL_TENANT = "33333333-3333-3333-3333-333333333333";

      const req = new NextReq(PROTECTED_URL, {
        method: "POST",
        headers: {
          cookie: `${COOKIE_NAME}=${signedToken(REAL_TENANT, REAL_TENANT, STRONG_SECRET)}`,
          // sem Origin — navegador legítimo SEMPRE envia em POST
        },
      });

      const res = await middleware(req);
      assert.equal(res.status, 403);
    },
  );
});

test("CSRF: GET same-origin SEM Origin (EventSource legítimo) -> NÃO é bloqueado", async () => {
  await withEnv(
    { NODE_ENV: "production", SESSION_SECRET: STRONG_SECRET, ALLOWED_ORIGINS: "https://app.hojex.com" },
    async () => {
      const middleware = await freshMiddleware();
      const { NextRequest: NextReq } = await import("next/server.js");
      const REAL_TENANT = "33333333-3333-3333-3333-333333333333";

      const req = new NextReq(COPILOT_URL, {
        headers: {
          cookie: `${COOKIE_NAME}=${signedToken(REAL_TENANT, REAL_TENANT, STRONG_SECRET)}`,
          "sec-fetch-site": "same-origin",
        },
      });

      const res = await middleware(req);
      assert.equal(
        res.headers.get("x-middleware-next"),
        "1",
        "o EventSource do próprio console não pode ser barrado — regressão de funcionalidade",
      );
    },
  );
});

test("CSRF: POST com Origin permitido -> passa (caminho feliz das mutations tRPC)", async () => {
  await withEnv(
    { NODE_ENV: "production", SESSION_SECRET: STRONG_SECRET, ALLOWED_ORIGINS: "https://app.hojex.com" },
    async () => {
      const middleware = await freshMiddleware();
      const { NextRequest: NextReq } = await import("next/server.js");
      const REAL_TENANT = "33333333-3333-3333-3333-333333333333";

      const req = new NextReq(PROTECTED_URL, {
        method: "POST",
        headers: {
          cookie: `${COOKIE_NAME}=${signedToken(REAL_TENANT, REAL_TENANT, STRONG_SECRET)}`,
          origin: "https://app.hojex.com",
          "sec-fetch-site": "same-origin",
        },
      });

      const res = await middleware(req);
      assert.equal(res.headers.get("x-middleware-next"), "1");
    },
  );
});

test("CSRF: Sec-Fetch-Site same-site (subdomínio do mesmo site) -> 403", async () => {
  // O console não faz nenhuma requisição legítima a si mesmo que não seja
  // same-origin, então `same-site` só existiria vindo de outro host sob o mesmo
  // domínio registrável (ex.: um blog em *.hojex.com comprometido por XSS).
  // Aceitá-lo ampliaria a superfície sem habilitar nada
  // (31ª onda, CSRF-SAME-SITE-BYPASS — pego pela revisão do próprio diff).
  await withEnv(
    { NODE_ENV: "production", SESSION_SECRET: STRONG_SECRET, ALLOWED_ORIGINS: "https://app.hojex.com" },
    async () => {
      const middleware = await freshMiddleware();
      const { NextRequest: NextReq } = await import("next/server.js");
      const REAL_TENANT = "33333333-3333-3333-3333-333333333333";

      const req = new NextReq(COPILOT_URL, {
        headers: {
          cookie: `${COOKIE_NAME}=${signedToken(REAL_TENANT, REAL_TENANT, STRONG_SECRET)}`,
          "sec-fetch-site": "same-site",
        },
      });

      const res = await middleware(req);
      assert.equal(res.status, 403);
    },
  );
});

test("CSRF: Sec-Fetch-Site none (barra de endereço/favorito) -> NÃO é bloqueado", async () => {
  await withEnv(
    { NODE_ENV: "production", SESSION_SECRET: STRONG_SECRET, ALLOWED_ORIGINS: "https://app.hojex.com" },
    async () => {
      const middleware = await freshMiddleware();
      const { NextRequest: NextReq } = await import("next/server.js");
      const REAL_TENANT = "33333333-3333-3333-3333-333333333333";

      const req = new NextReq(COPILOT_URL, {
        headers: {
          cookie: `${COOKIE_NAME}=${signedToken(REAL_TENANT, REAL_TENANT, STRONG_SECRET)}`,
          "sec-fetch-site": "none",
        },
      });

      const res = await middleware(req);
      assert.equal(res.headers.get("x-middleware-next"), "1");
    },
  );
});
