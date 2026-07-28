"use client";

/**
 * ThemeProvider — estado de tema em runtime (client).
 *
 * O primeiro paint já vem correto do THEME_INIT_SCRIPT (layout raiz); este
 * provider assume o controle após a hidratação.
 *
 * Fonte da verdade = localStorage + matchMedia (estado EXTERNO ao React), lido
 * via useSyncExternalStore. Esse hook é o padrão indicado para estado externo
 * mutável: é hydration-safe (usa getServerSnapshot no SSR e no 1º render do
 * cliente, depois re-renderiza) — então o toggle não sofre mismatch — e evita
 * setState dentro de efeito (que a regra react-hooks/set-state-in-effect barra).
 *
 * O DOM (classe `.dark`) só é reaplicado em MUDANÇAS reais (usuário troca ou o
 * SO muda em modo "system"); a aplicação inicial é pulada por um ref, pois o
 * THEME_INIT_SCRIPT já deixou o <html> correto antes da hidratação — reaplicar
 * o snapshot de servidor ("light") aqui causaria flash.
 */

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useSyncExternalStore,
} from "react";
import {
  applyResolvedTheme,
  isThemePref,
  resolveTheme,
  THEME_STORAGE_KEY,
  type ResolvedTheme,
  type ThemePref,
} from "@/lib/theme";

interface ThemeContextValue {
  /** Preferência escolhida pelo usuário. */
  pref: ThemePref;
  /** Tema efetivamente aplicado (system já resolvido). */
  resolved: ResolvedTheme;
  /** Define e persiste a preferência. */
  setPref: (pref: ThemePref) => void;
}

const ThemeContext = createContext<ThemeContextValue | null>(null);

const DARK_MQ = "(prefers-color-scheme: dark)";

function systemPrefersDark(): boolean {
  return (
    typeof window !== "undefined" && window.matchMedia(DARK_MQ).matches
  );
}

/** Lê a preferência salva; default "system". */
function readStoredPref(): ThemePref {
  if (typeof window === "undefined") return "system";
  try {
    const raw = window.localStorage.getItem(THEME_STORAGE_KEY);
    return isThemePref(raw) ? raw : "system";
  } catch {
    return "system";
  }
}

// ---- Store externo (localStorage + matchMedia) ---------------------------
// O snapshot é uma STRING primitiva ("pref|resolved") — comparável por Object.is,
// então getSnapshot pode ser chamado a cada render sem quebrar o bail-out.

const listeners = new Set<() => void>();

/** Notifica assinantes na MESMA aba (o evento `storage` só cruza abas). */
function emitChange(): void {
  for (const l of listeners) l();
}

function subscribe(onStoreChange: () => void): () => void {
  listeners.add(onStoreChange);
  const onStorage = (e: StorageEvent) => {
    if (e.key === THEME_STORAGE_KEY) onStoreChange();
  };
  const mq = window.matchMedia(DARK_MQ);
  window.addEventListener("storage", onStorage);
  mq.addEventListener("change", onStoreChange);
  return () => {
    listeners.delete(onStoreChange);
    window.removeEventListener("storage", onStorage);
    mq.removeEventListener("change", onStoreChange);
  };
}

function getSnapshot(): string {
  const pref = readStoredPref();
  return `${pref}|${resolveTheme(pref, systemPrefersDark())}`;
}

function getServerSnapshot(): string {
  return "system|light";
}

export function ThemeProvider({ children }: { children: React.ReactNode }) {
  const snapshot = useSyncExternalStore(
    subscribe,
    getSnapshot,
    getServerSnapshot,
  );
  const [pref, resolved] = snapshot.split("|") as [ThemePref, ResolvedTheme];

  // Reaplica a classe no <html> apenas em MUDANÇAS (não na montagem: o
  // THEME_INIT_SCRIPT já resolveu o 1º paint). Efeito puro de sincronização com
  // sistema externo (DOM) — não chama setState, então não viola a lint rule.
  const firstApply = useRef(true);
  useEffect(() => {
    if (firstApply.current) {
      firstApply.current = false;
      return;
    }
    applyResolvedTheme(resolved);
  }, [resolved]);

  const setPref = useCallback((next: ThemePref) => {
    try {
      window.localStorage.setItem(THEME_STORAGE_KEY, next);
    } catch {
      /* storage indisponível (modo privado) — aplica direto, sem persistir */
    }
    // Aplica já (feedback imediato ao clique) e notifica o store p/ re-render.
    applyResolvedTheme(resolveTheme(next, systemPrefersDark()));
    emitChange();
  }, []);

  return (
    <ThemeContext.Provider value={{ pref, resolved, setPref }}>
      {children}
    </ThemeContext.Provider>
  );
}

export function useTheme(): ThemeContextValue {
  const ctx = useContext(ThemeContext);
  if (!ctx) {
    throw new Error("useTheme deve ser usado dentro de <ThemeProvider>");
  }
  return ctx;
}
