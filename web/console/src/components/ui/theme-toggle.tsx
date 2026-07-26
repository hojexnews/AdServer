"use client";

/**
 * ThemeToggle — controle segmentado Sistema / Claro / Escuro.
 *
 * WCAG 2.2 AA:
 *   - <div role="group"> nomeado; cada opção é <button> com aria-pressed.
 *   - Nome acessível por <span class="sr-only"> (ícone é aria-hidden), então
 *     não depende só de cor/ícone.
 *   - A preferência vem de useSyncExternalStore (via useTheme): no SSR e no 1º
 *     render do cliente vale "system" (mesmo markup dos dois lados → sem warning
 *     de hidratação); o valor salvo é refletido logo após a hidratação.
 */

import { useTheme } from "@/components/theme-provider";
import { THEME_PREFS, type ThemePref } from "@/lib/theme";
import { cn } from "@/lib/utils";

const OPTIONS: Record<ThemePref, { label: string; icon: React.ReactNode }> = {
  system: { label: "Sistema", icon: <MonitorIcon /> },
  light: { label: "Claro", icon: <SunIcon /> },
  dark: { label: "Escuro", icon: <MoonIcon /> },
};

export function ThemeToggle({ className }: { className?: string }) {
  const { pref, setPref } = useTheme();

  return (
    <div
      role="group"
      aria-label="Tema da interface"
      className={cn(
        "inline-flex items-center gap-0.5 rounded-lg border border-border bg-muted p-0.5",
        className,
      )}
    >
      {THEME_PREFS.map((value) => {
        const active = pref === value;
        const { label, icon } = OPTIONS[value];
        return (
          <button
            key={value}
            type="button"
            onClick={() => setPref(value)}
            aria-pressed={active}
            title={label}
            className={cn(
              "flex h-7 w-8 items-center justify-center rounded-md text-muted-foreground transition-colors",
              "hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-500",
              "aria-pressed:bg-card aria-pressed:text-foreground aria-pressed:shadow-sm",
            )}
          >
            {icon}
            <span className="sr-only">{label}</span>
          </button>
        );
      })}
    </div>
  );
}

/* -- Ícones inline (sem dependência; herdam currentColor) ------------------ */

function SunIcon() {
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 24 24"
      width="16"
      height="16"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      <circle cx="12" cy="12" r="4" />
      <path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M6.34 17.66l-1.41 1.41M19.07 4.93l-1.41 1.41" />
    </svg>
  );
}

function MoonIcon() {
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 24 24"
      width="16"
      height="16"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      <path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z" />
    </svg>
  );
}

function MonitorIcon() {
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 24 24"
      width="16"
      height="16"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      <rect x="2" y="3" width="20" height="14" rx="2" />
      <path d="M8 21h8M12 17v4" />
    </svg>
  );
}
