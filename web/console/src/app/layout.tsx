/**
 * Root layout — App Router (Next.js 15).
 * Providers: TanStack Query v5 + tRPC client.
 * WCAG 2.2 AA: lang, skip-nav, aria-labels no nav.
 */

import type { Metadata } from "next";
import "./globals.css";
import { Providers } from "@/components/providers";

export const metadata: Metadata = {
  title: "AdServer Console",
  description: "Console self-service do anunciante — Hojex AdServer",
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="pt-BR">
      <body className="min-h-screen bg-gray-50 font-sans antialiased">
        {/* Skip navigation — WCAG 2.4.1 */}
        <a
          href="#main-content"
          className="sr-only focus:not-sr-only focus:absolute focus:top-4 focus:left-4 focus:z-50 focus:rounded focus:bg-brand-600 focus:px-4 focus:py-2 focus:text-white focus:no-underline"
        >
          Pular para o conteúdo principal
        </a>

        <Providers>
          <div className="flex min-h-screen">
            {/* Sidebar nav */}
            <nav
              aria-label="Navegação principal"
              className="w-64 shrink-0 border-r border-gray-200 bg-white"
            >
              <div className="flex h-16 items-center border-b border-gray-200 px-6">
                <span className="text-lg font-semibold text-brand-700">
                  AdServer Console
                </span>
              </div>
              <NavLinks />
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
  ];

  return (
    <ul role="list" className="mt-4 space-y-1 px-3">
      {links.map((link) => (
        <li key={link.href}>
          <a
            href={link.href}
            className="flex items-center rounded-md px-3 py-2 text-sm font-medium text-gray-700 hover:bg-brand-50 hover:text-brand-700 focus-visible:ring-2 focus-visible:ring-brand-500"
          >
            {link.label}
          </a>
        </li>
      ))}
    </ul>
  );
}
