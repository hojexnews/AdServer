/**
 * Guard de configuração de sessão (TX-3 / CA-1) — fail-closed real do
 * middleware de sessão em produção (G0/frontend E9).
 *
 * Predicado PURO, síncrono e sem I/O — não depende de Edge Runtime, Web
 * Crypto nem de `next/server` — para poder ser testado isoladamente com
 * `node --test` (o console não tem jest/vitest, ver web/console/src/lib/session-guard.test.ts)
 * e para ser reutilizável tanto por `middleware.ts` (camada 1: recusa a
 * rota inteira com 500 ANTES de qualquer outra lógica) quanto por
 * `verifySessionToken()` (camada 2: nunca aceitar um token não-assinado em
 * produção, mesmo que a camada 1 seja removida por engano — defense in depth).
 *
 * IMPORTANTE: isto é hardening de CÓDIGO. É independente da injeção real do
 * segredo via OpenBao/infra, que é o item G0/frontend E11 (gated, separado).
 * Aqui garantimos apenas que a AUSÊNCIA (ou fraqueza) do SESSION_SECRET em
 * produção nunca resulta em fail-OPEN (bypass de tenant via token forjado).
 */

/**
 * Tamanho mínimo aceitável do SESSION_SECRET em produção, em bytes
 * (UTF-8). 32 bytes = 256 bits, alinhado ao comentário de topo de
 * middleware.ts ("segredo de 256 bits em base64").
 */
export const MIN_SESSION_SECRET_BYTES = 32;

/**
 * Avalia se a configuração de sessão é segura para o ambiente informado.
 *
 * Regra (núcleo): em produção (`nodeEnv === "production"`), SESSION_SECRET
 * é OBRIGATÓRIO — sua ausência nunca é aceitável, pois o único fallback
 * disponível é o dev-stub que aceita tokens sem verificação de assinatura.
 * Hardening adicional (barato, mesma regra): o segredo também precisa ter
 * pelo menos MIN_SESSION_SECRET_BYTES bytes.
 *
 * Fora de produção (dev/CI/test/undefined), a ausência do segredo é
 * permitida — o comportamento atual do dev-stub é preservado 100%.
 *
 * @param nodeEnv `process.env.NODE_ENV` (ou equivalente injetado para teste).
 * @param secret `process.env.SESSION_SECRET` (ou equivalente injetado para teste).
 * @returns mensagem de erro (não-vazia) se a configuração for insegura;
 *   `null` se estiver OK e o middleware pode prosseguir normalmente.
 */
export function sessionConfigError(
  nodeEnv: string | undefined,
  secret: string | undefined
): string | null {
  if (nodeEnv !== "production") {
    // Dev/CI/test: dev-stub permanece permitido, comportamento inalterado.
    return null;
  }

  if (!secret) {
    return (
      "SESSION_SECRET ausente em produção (NODE_ENV=production) — " +
      "recusando a requisição (fail-closed, TX-3/CA-1). O dev-stub de " +
      "verificação de sessão NUNCA pode ser alcançado em produção."
    );
  }

  const secretBytes = new TextEncoder().encode(secret).length;
  if (secretBytes < MIN_SESSION_SECRET_BYTES) {
    return (
      `SESSION_SECRET curto demais em produção (${secretBytes} bytes, ` +
      `mínimo ${MIN_SESSION_SECRET_BYTES}) — recusando a requisição ` +
      "(fail-closed, TX-3/CA-1)."
    );
  }

  return null;
}
