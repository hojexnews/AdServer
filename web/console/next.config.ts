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

  // O BFF roda em :3001 — proxy apenas em dev
  async rewrites() {
    if (process.env.NODE_ENV !== "production") {
      return [
        {
          source: "/api/trpc/:path*",
          destination: "http://localhost:3001/:path*",
        },
      ];
    }
    return [];
  },
};

export default nextConfig;
