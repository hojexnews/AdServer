/**
 * Tema claro/escuro — lógica pura + script anti-FOUC.
 *
 * Estratégia (padrão da indústria, sem dependência externa como next-themes):
 *   - Preferência do usuário ("system" | "light" | "dark") persiste em
 *     localStorage. "system" segue prefers-color-scheme do SO.
 *   - THEME_INIT_SCRIPT roda SÍNCRONO como primeiro filho do <body>, ANTES da
 *     hidratação/primeira pintura: lê a preferência, resolve para claro/escuro
 *     e aplica a classe `.dark` no <html>. Isso elimina o flash de tema errado
 *     (FOUC) que um useEffect (pós-hidratação) causaria.
 *   - ThemeProvider (client) gerencia o estado em runtime e reaplica.
 *
 * As funções puras (resolveTheme/isThemePref) são testadas por src/lib/theme.test.ts
 * (node:test — o console não tem jest/vitest). applyResolvedTheme toca o DOM e
 * só é chamada no browser (nunca no runner de teste).
 */

export type ThemePref = "system" | "light" | "dark";
export type ResolvedTheme = "light" | "dark";

/** Chave do localStorage. Referenciada literalmente no THEME_INIT_SCRIPT. */
export const THEME_STORAGE_KEY = "adserver-console-theme";

/** Ordem canônica das preferências (usada pelo toggle segmentado). */
export const THEME_PREFS: readonly ThemePref[] = ["system", "light", "dark"];

/** Type guard — valida valor cru vindo de localStorage/entrada externa. */
export function isThemePref(value: unknown): value is ThemePref {
  return value === "system" || value === "light" || value === "dark";
}

/** Resolve a preferência para o tema efetivo, dado o sinal do SO. */
export function resolveTheme(
  pref: ThemePref,
  systemPrefersDark: boolean,
): ResolvedTheme {
  if (pref === "system") return systemPrefersDark ? "dark" : "light";
  return pref;
}

/**
 * Aplica o tema resolvido ao <html>: classe `.dark` (variantes Tailwind +
 * tokens semânticos), color-scheme (form controls/scrollbars nativos) e
 * data-theme (hook para CSS/testes). ESPELHA exatamente THEME_INIT_SCRIPT —
 * manter os dois em sincronia.
 */
export function applyResolvedTheme(resolved: ResolvedTheme): void {
  const el = document.documentElement;
  const isDark = resolved === "dark";
  el.classList.toggle("dark", isDark);
  el.style.colorScheme = isDark ? "dark" : "light";
  el.setAttribute("data-theme", resolved);
}

/**
 * Script inline anti-FOUC. Injetado via dangerouslySetInnerHTML como primeiro
 * filho do <body> no layout raiz. Mantido como string mínima (roda antes de
 * qualquer bundle) e single-source da chave de storage via JSON.stringify.
 */
export const THEME_INIT_SCRIPT = `(function(){try{var k=${JSON.stringify(
  THEME_STORAGE_KEY,
)};var p=localStorage.getItem(k);if(p!=="light"&&p!=="dark"&&p!=="system"){p="system";}var d=p==="dark"||(p==="system"&&window.matchMedia("(prefers-color-scheme: dark)").matches);var e=document.documentElement;e.classList.toggle("dark",d);e.style.colorScheme=d?"dark":"light";e.setAttribute("data-theme",d?"dark":"light");}catch(_){}})();`;
