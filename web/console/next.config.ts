import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  // Strict mode para detectar efeitos colaterais (React 19)
  reactStrictMode: true,

  // Turbopack habilitado (--turbopack no dev script)
  // Build usa webpack (default do Next.js 15)

  // Headers de segurança
  async headers() {
    return [
      {
        source: "/(.*)",
        headers: [
          { key: "X-Frame-Options", value: "DENY" },
          { key: "X-Content-Type-Options", value: "nosniff" },
          { key: "Referrer-Policy", value: "strict-origin-when-cross-origin" },
          {
            key: "Permissions-Policy",
            value: "camera=(), microphone=(), geolocation=()",
          },
        ],
      },
    ];
  },

  /**
   * SEM rewrites para /api/trpc.
   *
   * O proxy para o BFF é um ROUTE HANDLER — src/app/api/trpc/[trpc]/route.ts.
   *
   * Historicamente havia aqui uma rewrite condicionada a
   * `NODE_ENV !== "production"`, o que deixava o console SEM control-plane em
   * produção (achado CRÍTICO da 31ª onda: `rewrites()` devolvia [], não havia
   * route handler, e nenhuma regra de ingress em deploy//platform/ cobria o
   * caminho — toda chamada tRPC 404ava).
   *
   * Tornar a rewrite incondicional NÃO resolve: `rewrites()` só é avaliado
   * durante `next build` e o destino já interpolado é congelado em
   * .next/routes-manifest.json. Uma `BFF_INTERNAL_URL` injetada no container em
   * tempo de deploy (o padrão do repo para SESSION_SECRET/ALLOWED_ORIGINS via
   * OpenBao) seria silenciosamente ignorada, mantendo o localhost do build.
   * O route handler lê a env var por requisição e falha fechado sem ela.
   */
};

export default nextConfig;
