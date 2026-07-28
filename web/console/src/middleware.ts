/**
 * Next.js Edge Middleware — fronteira de segurança de tenant (TX-3 / CA-1).
 *
 * RESPONSABILIDADES:
 *   1. DENYLIST: strip de todos os headers X-Adserver-* e X-Tenant-* que venham
 *      do browser antes de qualquer rota — impede que o cliente forje tenant_id.
 *   2. INJEÇÃO SEGURA: deriva o tenant_id EXCLUSIVAMENTE do cookie de sessão
 *      HttpOnly (SESSION_COOKIE_NAME), verifica a assinatura HMAC/JWT e injeta
 *      X-Adserver-Session-Tenant e X-Adserver-Session-User como headers internos
 *      (server-side) para as rotas downstream consumirem.
 *   3. GUARDA DE AUTENTICAÇÃO: rotas /api/copilot/* e /api/trpc/* sem sessão
 *      válida retornam 401 imediatamente — o BFF e a rota SSE nunca chegam a
 *      processar um request não autenticado.
 *   4. FAIL-CLOSED DE CONFIGURAÇÃO (G0/frontend E9, TX-3/CA-1): em produção
 *      (NODE_ENV=production), SESSION_SECRET ausente OU curto demais (<32
 *      bytes) faz TODA rota casada pelo matcher retornar 500 — nunca cai no
 *      ramo dev-stub de verifySessionToken (que aceita token sem assinatura).
 *      Ver lib/session-guard.ts para o predicado puro e seus testes. Duas
 *      camadas de defesa: (a) o guard no topo de middleware() recusa a
 *      requisição antes de qualquer outra lógica; (b) verifySessionToken()
 *      também recusa (retorna null) se produção + sem segredo, mesmo que (a)
 *      seja removido por engano. Isto é hardening de CÓDIGO — independente
 *      da injeção real do segredo via OpenBao/infra (item E11, separado).
 *
 * POR QUE ISSO RESOLVE H4:
 *   - Mesmo que o cliente envie X-Adserver-Session-Tenant no request, o middleware
 *     o remove antes de qualquer handler ser chamado.
 *   - O header injetado pelo middleware (RequestHeaders) NÃO pode ser forjado pelo
 *     browser pois o middleware roda server-side (Edge Runtime).
 *   - A rota SSE e o contexto tRPC leem o header APÓS a denylist, portanto sempre
 *     obtêm o valor derivado da sessão autenticada.
 *
 * PRODUÇÃO — variáveis de ambiente (OpenBao):
 *   SESSION_COOKIE_NAME  — nome do cookie HttpOnly (default: "__Host-sess")
 *   SESSION_SECRET       — segredo HMAC-SHA256 de 32+ bytes para verificar assinatura
 *                          do cookie. Em produção: segredo de 256 bits em base64.
 *                          Enquanto ausente (dev/CI): modo stub (SKIP_AUTH_DEV=true).
 *   ALLOWED_ORIGINS      — CSV de origens permitidas para CSRF (ex.: https://app.hojex.com)
 *
 * REGRA DE INGRESS/CILIUM (documentar em infra/k8s/network-policies/console.yaml):
 *   A denylist aqui é defense-in-depth. O Ingress Controller (Nginx/Cilium) DEVE
 *   remover os headers proibidos na borda antes de atingir o pod do Next.js:
 *     nginx.ingress.kubernetes.io/configuration-snippet: |
 *       more_clear_input_headers "X-Adserver-*" "X-Tenant-*" "X-Internal-*";
 *   Cilium NetworkPolicy deve restringir o tráfego L7 para que apenas o Ingress
 *   pod possa chamar o console:3000 e apenas o console:3000 possa chamar o bff:3001.
 *   Ver docs/security/ingress-header-denylist.md (a criar pelo infra-engineer).
 */

import { NextRequest, NextResponse } from "next/server";
import { sessionConfigError } from "@/lib/session-guard";

// ---------------------------------------------------------------------------
// Configuração
// ---------------------------------------------------------------------------

/**
 * Prefixos de headers que NUNCA devem vir do browser.
 * O middleware os remove antes de qualquer handler.
 */
const FORBIDDEN_HEADER_PREFIXES = [
  "x-adserver-",
  "x-tenant-",
  "x-internal-",
] as const;

/** Cookie HttpOnly que carrega o token de sessão. */
const SESSION_COOKIE_NAME =
  process.env["SESSION_COOKIE_NAME"] ?? "__Host-sess";

/** Segredo HMAC para verificar o cookie. Ausente = modo dev-stub. */
const SESSION_SECRET = process.env["SESSION_SECRET"];

/**
 * Origens permitidas para CSRF (M1).
 * Em produção: definir ALLOWED_ORIGINS como CSV.
 */
const ALLOWED_ORIGINS: ReadonlySet<string> = new Set(
  (process.env["ALLOWED_ORIGINS"] ?? "http://localhost:3000")
    .split(",")
    .map((o) => o.trim())
    .filter(Boolean)
);

// ---------------------------------------------------------------------------
// Rotas que exigem autenticação
// ---------------------------------------------------------------------------

/** Padrões de pathname que exigem sessão válida. */
const AUTH_REQUIRED_PATTERNS = [
  /^\/api\/copilot\//,
  /^\/api\/trpc\//,
] as const;

// ---------------------------------------------------------------------------
// Verificação de assinatura do cookie de sessão
// ---------------------------------------------------------------------------

/**
 * Payload mínimo do token de sessão.
 * Em produção: usar PASETO v4 ou JWT RS256/EdDSA com claims padrão.
 * Aqui: formato simplificado base64url(JSON).HMAC-SHA256 para fase MVP.
 */
interface SessionPayload {
  /** UUID do tenant. */
  tenantId: string;
  /** UUID do usuário. */
  userId: string;
  /** Expiração UNIX timestamp (segundos). */
  exp: number;
}

/**
 * Verifica e decodifica o token de sessão do cookie.
 *
 * Formato: base64url(payload_json) + "." + hex(HMAC-SHA256(payload, secret))
 *
 * NOTA: Em produção substituir por `jose` (JWT RS256) ou `@noble/post-quantum`
 * (PASETO v4). O formato atual é suficiente para dev/staging com SESSION_SECRET.
 *
 * @returns SessionPayload se válido, null caso contrário.
 */
async function verifySessionToken(
  token: string
): Promise<SessionPayload | null> {
  if (!SESSION_SECRET) {
    // ---- Camada 2 de defense-in-depth (TX-3/CA-1, G0/frontend E9) ----
    // Mesmo que o guard de topo em middleware() seja removido por engano no
    // futuro, este branch NUNCA aceita um token não-assinado em produção.
    // Rejeita incondicionalmente em vez de cair no parse do dev-stub abaixo.
    if (process.env["NODE_ENV"] === "production") {
      return null;
    }

    // Dev-stub: aceita token no formato JSON simples (sem assinatura)
    // NUNCA em produção — SESSION_SECRET deve estar definido.
    try {
      const decoded = atob(token.replace(/-/g, "+").replace(/_/g, "/"));
      const payload = JSON.parse(decoded) as Partial<SessionPayload>;
      if (
        typeof payload.tenantId === "string" &&
        typeof payload.userId === "string"
      ) {
        return {
          tenantId: payload.tenantId,
          userId: payload.userId,
          exp: payload.exp ?? Number.MAX_SAFE_INTEGER,
        };
      }
    } catch {
      // token malformado
    }
    return null;
  }

  // Produção: verifica HMAC
  const dotIndex = token.lastIndexOf(".");
  if (dotIndex === -1) return null;

  const payloadB64 = token.slice(0, dotIndex);
  const sigHex = token.slice(dotIndex + 1);

  try {
    const encoder = new TextEncoder();
    const keyData = encoder.encode(SESSION_SECRET);
    const cryptoKey = await crypto.subtle.importKey(
      "raw",
      keyData,
      { name: "HMAC", hash: "SHA-256" },
      false,
      ["sign", "verify"]
    );

    const messageBytes = encoder.encode(payloadB64);
    const sigBytes = hexToBytes(sigHex);
    if (!sigBytes) return null;

    const valid = await crypto.subtle.verify(
      "HMAC",
      cryptoKey,
      sigBytes.buffer as ArrayBuffer,
      messageBytes.buffer as ArrayBuffer
    );
    if (!valid) return null;

    const decoded = atob(payloadB64.replace(/-/g, "+").replace(/_/g, "/"));
    const payload = JSON.parse(decoded) as Partial<SessionPayload>;

    if (
      typeof payload.tenantId !== "string" ||
      typeof payload.userId !== "string" ||
      typeof payload.exp !== "number"
    ) {
      return null;
    }

    // Verifica expiração
    if (payload.exp < Math.floor(Date.now() / 1000)) {
      return null;
    }

    // Valida UUID
    if (!isUuid(payload.tenantId) || !isUuid(payload.userId)) {
      return null;
    }

    return { tenantId: payload.tenantId, userId: payload.userId, exp: payload.exp };
  } catch {
    return null;
  }
}

function hexToBytes(hex: string): Uint8Array | null {
  if (hex.length % 2 !== 0) return null;
  const bytes = new Uint8Array(hex.length / 2);
  for (let i = 0; i < hex.length; i += 2) {
    const byte = parseInt(hex.slice(i, i + 2), 16);
    if (isNaN(byte)) return null;
    bytes[i / 2] = byte;
  }
  return bytes;
}

const UUID_RE =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

function isUuid(s: string): boolean {
  return UUID_RE.test(s);
}

// ---------------------------------------------------------------------------
// Middleware principal
// ---------------------------------------------------------------------------

export async function middleware(request: NextRequest): Promise<NextResponse> {
  // ---- 0. GUARD DE CONFIGURAÇÃO (TX-3/CA-1, G0/frontend E9) ----
  // Fail-closed REAL: em produção, SESSION_SECRET ausente (ou fraco) nunca
  // pode cair no ramo dev-stub de verifySessionToken (que aceita tokens sem
  // verificação de assinatura). Recusa TODA rota casada pelo matcher abaixo
  // com 500, ANTES de qualquer outra lógica — "recusar boot" em nível de
  // requisição, já que o Edge Runtime não expõe um hook de boot do processo.
  // Isto é hardening de CÓDIGO, independente da injeção real do segredo via
  // OpenBao/infra (item G0/frontend E11, separado e ainda pendente).
  const configError = sessionConfigError(
    process.env["NODE_ENV"],
    SESSION_SECRET
  );
  if (configError !== null) {
    console.error(`[middleware] server misconfiguration: ${configError}`);
    return NextResponse.json(
      { error: "server misconfiguration" },
      { status: 500 }
    );
  }

  const { pathname } = request.nextUrl;

  // ---- 1. DENYLIST: remove todos os headers proibidos do request de entrada ----
  // Constrói novo objeto de headers sem os prefixos proibidos.
  // O NextResponse.next() com { request: { headers } } substitui os headers
  // do request antes de chegar ao handler.
  const cleanedHeaders = new Headers(request.headers);

  for (const [key] of request.headers.entries()) {
    const lkey = key.toLowerCase();
    if (
      FORBIDDEN_HEADER_PREFIXES.some((prefix) => lkey.startsWith(prefix))
    ) {
      cleanedHeaders.delete(key);
    }
  }

  // ---- 2. Verifica se a rota exige autenticação ----
  const requiresAuth = AUTH_REQUIRED_PATTERNS.some((re) => re.test(pathname));

  if (requiresAuth) {
    // ---- 3. Lê o cookie de sessão HttpOnly ----
    const sessionToken = request.cookies.get(SESSION_COOKIE_NAME)?.value;

    if (!sessionToken) {
      return NextResponse.json(
        { error: "Sessão ausente. Faça login novamente." },
        { status: 401 }
      );
    }

    // ---- 4. Verifica e decodifica o token ----
    const session = await verifySessionToken(sessionToken);

    if (!session) {
      return NextResponse.json(
        { error: "Sessão inválida ou expirada. Faça login novamente." },
        { status: 401 }
      );
    }

    // ---- 5. CSRF (M1 — defense-in-depth; a verificação completa está no BFF) ----
    //
    // FIX (31ª onda, copilot-stream-get-csrf): a condição anterior era
    // `origin !== null && !ALLOWED_ORIGINS.has(origin)` — ou seja, requisição
    // SEM header Origin passava direto. Navegadores omitem Origin em GET
    // same-origin, então a checagem simplesmente não existia para
    // `GET /api/copilot/stream/:id?message=...` — uma rota que dispara chamada
    // PAGA de LLM no tenant da vítima.
    //
    // Duas camadas, escolhidas para NÃO quebrar o EventSource:
    //
    //   (a) Sec-Fetch-Site — enviado por navegadores modernos em TODA
    //       requisição, inclusive GET same-origin (ao contrário de Origin).
    //       Só `same-origin` e `none` passam; `cross-site` e `same-site` são
    //       rejeitados. Ausência é tolerada (navegador antigo / cliente
    //       não-browser): endurecer isso quebraria compatibilidade, e a
    //       camada (b) + a política SameSite do cookie cobrem esse caso.
    //
    //   (b) Origin obrigatório em métodos que mudam estado (não-GET/HEAD).
    //       Aqui o fail-closed é seguro: navegadores SEMPRE enviam Origin em
    //       POST, então ausência indica cliente forjado. É o caso das mutations
    //       tRPC (hitlApprove/hitlReject/chat). Para GET, a allow-list de
    //       Origin segue aplicada quando o header está presente — redundante
    //       com (a) nos navegadores modernos, mas é a única barreira nos que
    //       não enviam Sec-Fetch-Site.
    // Bloqueia `cross-site` E `same-site`. Só `same-origin` e `none` passam.
    //
    // `same-site` cobre um subdomínio do MESMO site registrável — por exemplo
    // um blog ou uma landing em `*.hojex.com` que sofra XSS. O console não faz
    // nenhuma requisição legítima cross-origin para si mesmo (o cliente tRPC e
    // o EventSource usam caminhos relativos, sempre `same-origin`), então
    // aceitar `same-site` só ampliaria a superfície sem habilitar nada
    // (31ª onda, CSRF-SAME-SITE-BYPASS).
    //
    // `none` = navegação digitada na barra de endereço / favorito: legítima.
    // Header ausente = navegador antigo ou cliente não-browser; a camada (b)
    // abaixo e a política SameSite do cookie continuam valendo.
    const secFetchSite = request.headers.get("sec-fetch-site");
    if (secFetchSite === "cross-site" || secFetchSite === "same-site") {
      return NextResponse.json(
        { error: "Origem não permitida." },
        { status: 403 }
      );
    }

    const origin = request.headers.get("origin");
    const method = request.method.toUpperCase();
    const isSafeMethod = method === "GET" || method === "HEAD";

    if (!isSafeMethod) {
      // Fail-closed: sem Origin OU fora da allow-list.
      if (origin === null || !ALLOWED_ORIGINS.has(origin)) {
        return NextResponse.json(
          { error: "Origem não permitida." },
          { status: 403 }
        );
      }
    } else if (origin !== null && !ALLOWED_ORIGINS.has(origin)) {
      // GET com Origin explícito fora da allow-list (requisição cross-origin
      // de verdade) continua barrado.
      return NextResponse.json(
        { error: "Origem não permitida." },
        { status: 403 }
      );
    }

    // ---- 6. Injeta headers internos derivados da sessão autenticada ----
    // Estes headers são criados AQUI (server-side) após verificação criptográfica.
    // O cliente nunca os envia — mesmo que tentasse, foram removidos no passo 1.
    cleanedHeaders.set("x-adserver-session-tenant", session.tenantId);
    cleanedHeaders.set("x-adserver-session-user", session.userId);
  }

  // ---- 7. Propaga request com headers limpos ----
  return NextResponse.next({
    request: { headers: cleanedHeaders },
  });
}

// ---------------------------------------------------------------------------
// Configuração do matcher — apenas rotas relevantes (excluir _next/static etc.)
// ---------------------------------------------------------------------------

export const config = {
  matcher: [
    /*
     * Aplica o middleware a todas as rotas exceto:
     * - _next/static (assets estáticos)
     * - _next/image (otimização de imagem)
     * - favicon.ico, robots.txt, etc.
     * - ícones/OG do App Router (icon.png, apple-icon.png, opengraph-image.png),
     *   que o <head> referencia mesmo em páginas não autenticadas.
     */
    "/((?!_next/static|_next/image|favicon.ico|icon.png|apple-icon.png|opengraph-image.png|robots.txt|sitemap.xml).*)",
  ],
};
