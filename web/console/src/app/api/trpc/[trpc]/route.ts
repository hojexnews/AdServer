/**
 * Proxy do console para o BFF tRPC — `/api/trpc/*` → BFF (porta 3001).
 *
 * POR QUE UM ROUTE HANDLER E NÃO UMA REWRITE (31ª onda, achados C2 e F1):
 *
 * O cliente tRPC aponta para `/api/trpc` (src/lib/trpc.ts). Esse caminho
 * existia APENAS como `rewrites()` condicionada a `NODE_ENV !== "production"`
 * em next.config.ts — em build de produção `rewrites()` devolvia `[]`, não há
 * regra de ingress em deploy//platform/, e portanto TODA chamada tRPC do
 * console 404ava em produção (confirmado na lista de rotas do `next build`).
 *
 * A primeira tentativa de correção foi tornar a rewrite incondicional lendo
 * `BFF_INTERNAL_URL`. A revisão adversarial do próprio diff derrubou essa
 * solução, com prova: `rewrites()` só é invocado por `next build`
 * (next/dist/lib/load-custom-routes.js), e o valor JÁ INTERPOLADO é congelado
 * em `.next/routes-manifest.json`. Ou seja, injetar `BFF_INTERNAL_URL` como
 * variável de ambiente do container em tempo de deploy — que é exatamente o
 * padrão documentado para SESSION_SECRET/ALLOWED_ORIGINS via OpenBao — não
 * teria efeito nenhum: o destino continuaria sendo o `http://localhost:3001`
 * congelado no build. Seria um fail-open silencioso, com a CI verde.
 *
 * Um route handler resolve a env var A CADA REQUISIÇÃO, então o endereço do
 * BFF é configurável no deploy como qualquer outro segredo. De quebra, mantém
 * o mesmo padrão dos proxies que já existem neste addon
 * (`/api/copilot/stream`, `/api/copilot/resume`).
 *
 * SEGURANÇA:
 *   - O middleware (src/middleware.ts) roda ANTES deste handler: remove os
 *     headers de prefixo `x-adserver-`, `x-tenant-` e `x-internal-` que venham
 *     do browser e injeta x-adserver-session-tenant/-user derivados do cookie
 *     HttpOnly verificado por HMAC. Este handler apenas REPASSA o que chega.
 *   - `/api/trpc/*` está em AUTH_REQUIRED_PATTERNS: requisição sem sessão
 *     válida nunca chega aqui (401 no middleware).
 *   - O destino NUNCA vem do request: só de BFF_INTERNAL_URL (env do
 *     servidor). Não há superfície de SSRF controlada pelo cliente.
 *   - Sem BFF_INTERNAL_URL válida em produção, responde 500 em vez de cair
 *     num default silencioso (fail-closed, mesma disciplina do SESSION_SECRET).
 */

import { type NextRequest, NextResponse } from "next/server";

/** Fallback de desenvolvimento — o BFF local sobe em :3001 (bff/src/index.ts). */
const DEV_BFF_URL = "http://localhost:3001";

/**
 * Headers que NUNCA devem ser repassados adiante: são específicos da conexão
 * entre browser e console, e reencaminhá-los corrompe o proxy.
 */
const HOP_BY_HOP = new Set([
  "connection",
  "keep-alive",
  "transfer-encoding",
  "upgrade",
  "proxy-authenticate",
  "proxy-authorization",
  "te",
  "trailer",
  "host",
  "content-length",
]);

/**
 * Resolve a base do BFF.
 *
 * Fail-closed em produção: `??` sozinho aceitaria string VAZIA (caso comum de
 * template de Helm/CI que não resolveu, `BFF_INTERNAL_URL: "${...}"` expandindo
 * para ""), produzindo um destino sem host que reescreveria silenciosamente
 * para dentro do próprio console. Validamos que é uma URL http(s) de verdade.
 */
function resolveBffUrl(): { url: string } | { error: string } {
  const raw = process.env["BFF_INTERNAL_URL"]?.trim();

  if (!raw) {
    if (process.env["NODE_ENV"] === "production") {
      return {
        error:
          "BFF_INTERNAL_URL não configurada em produção — o console não tem " +
          "como alcançar o BFF. Defina a variável no deploy (OpenBao).",
      };
    }
    return { url: DEV_BFF_URL };
  }

  let parsed: URL;
  try {
    parsed = new URL(raw);
  } catch {
    return { error: `BFF_INTERNAL_URL inválida: não é uma URL absoluta.` };
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
    return {
      error: `BFF_INTERNAL_URL inválida: protocolo ${parsed.protocol} não suportado.`,
    };
  }
  // Sem barra final — o path do request já começa com "/".
  return { url: raw.replace(/\/+$/, "") };
}

async function proxy(request: NextRequest): Promise<Response> {
  const resolved = resolveBffUrl();
  if ("error" in resolved) {
    console.error(`[api/trpc] ${resolved.error}`);
    return NextResponse.json(
      { error: "server misconfiguration" },
      { status: 500 }
    );
  }

  // O BFF (createHTTPServer do tRPC) serve o router na RAIZ, então o prefixo
  // /api/trpc é removido — mesma transformação que a rewrite de dev fazia.
  const { pathname, search } = request.nextUrl;
  const downstreamPath = pathname.replace(/^\/api\/trpc/, "") || "/";
  const target = `${resolved.url}${downstreamPath}${search}`;

  const headers = new Headers();
  for (const [key, value] of request.headers.entries()) {
    if (!HOP_BY_HOP.has(key.toLowerCase())) {
      headers.set(key, value);
    }
  }

  const hasBody = request.method !== "GET" && request.method !== "HEAD";

  let upstream: Response;
  try {
    upstream = await fetch(target, {
      method: request.method,
      headers,
      ...(hasBody ? { body: await request.arrayBuffer() } : {}),
      cache: "no-store",
      redirect: "manual",
    });
  } catch (err) {
    // Não vazar o endereço interno nem o erro do driver para o cliente.
    console.error("[api/trpc] upstream inalcançável", {
      message: err instanceof Error ? err.message : String(err),
    });
    return NextResponse.json(
      { error: "Serviço temporariamente indisponível." },
      { status: 502 }
    );
  }

  const responseHeaders = new Headers();
  for (const [key, value] of upstream.headers.entries()) {
    if (!HOP_BY_HOP.has(key.toLowerCase())) {
      responseHeaders.set(key, value);
    }
  }

  return new Response(upstream.body, {
    status: upstream.status,
    statusText: upstream.statusText,
    headers: responseHeaders,
  });
}

export const GET = proxy;
export const POST = proxy;

/** Sem cache: toda chamada tRPC é dinâmica e escopada ao tenant da sessão. */
export const dynamic = "force-dynamic";
