/**
 * Root layout — App Router (Next.js 15).
 * Providers: TanStack Query v5 + tRPC client.
 * WCAG 2.2 AA: lang, skip-nav, aria-labels no nav.
 */

import type { Metadata } from "next";
import Link from "next/link";
import "./globals.css";
import { Providers } from "@/components/providers";
import { ThemeProvider } from "@/components/theme-provider";
import { ThemeToggle } from "@/components/ui/theme-toggle";
import { Logo } from "@/components/logo";
import { THEME_INIT_SCRIPT } from "@/lib/theme";

/**
 * Base absoluta das URLs de metadata (OG image, canonical).
 *
 * ATENÇÃO (31ª onda, og-image-localhost-baked): NEXT_PUBLIC_* é inlinado em
 * tempo de BUILD, não lido em runtime. Se o build de produção rodar sem
 * NEXT_PUBLIC_SITE_URL, o fallback "http://localhost:3000" fica CRAVADO no
 * bundle e a og:image publicada aponta para localhost — todo preview em
 * WhatsApp/Slack/LinkedIn quebra, e nenhum teste pega porque a página
 * funciona normalmente.
 *
 * Não podemos lançar aqui: `next build` roda com NODE_ENV=production também
 * localmente e no gate de a11y, então um throw quebraria `make web-build` e
 * `make web-a11y` em toda máquina de dev. O que dá para fazer sem falso-RED é
 * gritar no log do build — o cutover de infra (§3.6 E11) tem de definir a var.
 */
const SITE_URL = process.env.NEXT_PUBLIC_SITE_URL ?? "http://localhost:3000";

if (process.env.NODE_ENV === "production" && !process.env.NEXT_PUBLIC_SITE_URL) {
  console.warn(
    "[metadata] NEXT_PUBLIC_SITE_URL ausente no build de produção — " +
      "metadataBase cai em http://localhost:3000 e a og:image publicada " +
      "apontará para localhost. Defina a variável antes do build de release.",
  );
}

export const metadata: Metadata = {
  metadataBase: new URL(SITE_URL),
  title: {
    default: "AdServer Console",
    template: "%s · AdServer Console",
  },
  description: "Console self-service do anunciante — Hojex AdServer",
  applicationName: "Hojex AdServer",
  // favicon/icon/apple-icon/opengraph-image são resolvidos pelas convenções de
  // arquivo do App Router (src/app/icon.png, apple-icon.png, opengraph-image.png).
  openGraph: {
    type: "website",
    siteName: "Hojex AdServer",
    title: "AdServer Console",
    description: "Console self-service do anunciante — Hojex AdServer",
    locale: "pt_BR",
  },
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="pt-BR" suppressHydrationWarning>
      <body className="min-h-screen bg-background font-sans text-foreground antialiased">
        {/*
          Anti-FOUC: aplica a classe `.dark` no <html> ANTES da primeira pintura,
          lendo a preferência salva. Primeiro filho do <body> = roda síncrono
          durante o parse, antes de qualquer bundle. `suppressHydrationWarning`
          no <html> porque este script muta a classe antes da hidratação.
        */}
        <script dangerouslySetInnerHTML={{ __html: THEME_INIT_SCRIPT }} />

        {/* Skip navigation — WCAG 2.4.1 */}
        <a
          href="#main-content"
          className="sr-only focus:not-sr-only focus:absolute focus:top-4 focus:left-4 focus:z-50 focus:rounded focus:bg-brand-600 focus:px-4 focus:py-2 focus:text-white focus:no-underline"
        >
          Pular para o conteúdo principal
        </a>

        <ThemeProvider>
          <Providers>
            <div className="flex min-h-screen">
              {/* Sidebar nav */}
              <nav
                aria-label="Navegação principal"
                className="flex w-64 shrink-0 flex-col border-r border-border bg-card"
              >
                <div className="flex h-16 items-center border-b border-border px-6">
                  <Link href="/" aria-label="Hojex AdServer — início" className="rounded focus-visible:ring-2 focus-visible:ring-brand-500">
                    <Logo markSize={28} />
                  </Link>
                </div>
                <NavLinks />
                <div className="mt-auto border-t border-border p-3">
                  <p className="px-1 pb-1.5 text-xs font-medium text-muted-foreground">
                    Tema
                  </p>
                  <ThemeToggle />
                </div>
              </nav>

              {/* Main content */}
              <main
                id="main-content"
                className="flex-1 overflow-auto p-8"
                tabIndex={-1}
              >
                {children}
              </main>
            </div>
          </Providers>
        </ThemeProvider>
      </body>
    </html>
  );
}

function NavLinks() {
  const links = [
    { href: "/advertisers", label: "Anunciantes" },
    { href: "/campaigns", label: "Campanhas" },
    { href: "/banners", label: "Banners" },
    { href: "/sites", label: "Sites" },
    { href: "/zones", label: "Zonas" },
    { href: "/rules", label: "Regras de Entrega" },
    { href: "/dashboard", label: "Dashboard" },
    { href: "/copilot", label: "Copiloto IA" },
    /** K7 — faturamento/pagamentos self-service */
    { href: "/billing", label: "Faturamento" },
  ];

  return (
    <ul role="list" className="mt-4 space-y-1 px-3">
      {links.map((link) => (
        <li key={link.href}>
          <a
            href={link.href}
            className="flex items-center rounded-md px-3 py-2 text-sm font-medium text-muted-foreground hover:bg-brand-50 hover:text-brand-700 focus-visible:ring-2 focus-visible:ring-brand-500 dark:hover:text-brand-300"
          >
            {link.label}
          </a>
        </li>
      ))}
    </ul>
  );
}
