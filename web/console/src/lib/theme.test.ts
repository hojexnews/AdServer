/**
 * Testes da lógica pura de tema (node:test nativo — sem jest/vitest, mesmo
 * padrão de session-guard.test.ts). Rodado por `make web-test`
 * (node --test $(find src -name '*.test.ts')).
 */

import { test } from "node:test";
import assert from "node:assert/strict";
import {
  isThemePref,
  resolveTheme,
  THEME_PREFS,
  THEME_STORAGE_KEY,
  THEME_INIT_SCRIPT,
} from "./theme.ts";

test("resolveTheme: preferência explícita ignora o sinal do sistema", () => {
  assert.equal(resolveTheme("light", true), "light");
  assert.equal(resolveTheme("light", false), "light");
  assert.equal(resolveTheme("dark", true), "dark");
  assert.equal(resolveTheme("dark", false), "dark");
});

test("resolveTheme: 'system' segue prefers-color-scheme", () => {
  assert.equal(resolveTheme("system", true), "dark");
  assert.equal(resolveTheme("system", false), "light");
});

test("isThemePref: aceita só os três valores válidos", () => {
  for (const v of THEME_PREFS) assert.equal(isThemePref(v), true);
  for (const v of [null, undefined, "", "Dark", "auto", 0, {}]) {
    assert.equal(isThemePref(v), false);
  }
});

test("THEME_PREFS: ordem canônica do toggle", () => {
  assert.deepEqual([...THEME_PREFS], ["system", "light", "dark"]);
});

test("THEME_INIT_SCRIPT: single-source da chave e resistente a valor inválido", () => {
  // A chave de storage precisa aparecer literalmente no script anti-FOUC —
  // se divergir de THEME_STORAGE_KEY, o script lê uma chave que o provider
  // nunca escreve e o tema salvo é ignorado no primeiro paint.
  assert.ok(THEME_INIT_SCRIPT.includes(JSON.stringify(THEME_STORAGE_KEY)));
  // Fail-safe: valor cru inválido cai em "system" (nunca lança).
  assert.ok(THEME_INIT_SCRIPT.includes("system"));
  assert.ok(THEME_INIT_SCRIPT.includes("try"));
  assert.ok(THEME_INIT_SCRIPT.includes("prefers-color-scheme"));
});
