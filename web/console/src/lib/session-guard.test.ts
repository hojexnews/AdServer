/**
 * Testes de session-guard.ts (G0/frontend E9 — fail-closed real do
 * middleware de sessão em produção, TX-3/CA-1).
 *
 * O console (web/console) não tem jest/vitest instalado — apenas
 * `tsc --noEmit` + `next lint` (ver make/web.mk::web-ci). Este arquivo usa
 * o test runner NATIVO do Node 24 (node:test + node:assert) e roda .ts
 * diretamente via type-stripping nativo do Node, sem transpilação nem
 * dependência de rede:
 *
 *   node --test web/console/src/lib/session-guard.test.ts
 *
 * Alvo `make web-test` (ver make/web.mk) invoca exatamente isto.
 */

import { test } from "node:test";
import assert from "node:assert/strict";
import { sessionConfigError, MIN_SESSION_SECRET_BYTES } from "./session-guard.ts";

test("producao sem segredo -> falha fechada (erro nao-nulo)", () => {
  const err = sessionConfigError("production", undefined);
  assert.notEqual(err, null);
  assert.match(err ?? "", /SESSION_SECRET ausente/);
});

test("producao com segredo vazio -> falha fechada (erro nao-nulo)", () => {
  const err = sessionConfigError("production", "");
  assert.notEqual(err, null);
});

test("producao com segredo valido (>=32 bytes) -> null (config OK)", () => {
  const validSecret = "a".repeat(MIN_SESSION_SECRET_BYTES);
  assert.equal(sessionConfigError("production", validSecret), null);
});

test("producao com segredo exatamente no limite (32 bytes) -> null", () => {
  const secret = "x".repeat(32);
  assert.equal(sessionConfigError("production", secret), null);
});

test("producao com segredo curto demais (<32 bytes) -> falha fechada", () => {
  const shortSecret = "short-secret";
  assert.ok(shortSecret.length < MIN_SESSION_SECRET_BYTES);
  const err = sessionConfigError("production", shortSecret);
  assert.notEqual(err, null);
  assert.match(err ?? "", /curto demais/);
});

test("producao com segredo multi-byte UTF-8 curto (chars < 32 mas bytes >= 32 nao se aplica aqui) -> valida por bytes, nao chars", () => {
  // 32 caracteres "é" (2 bytes UTF-8 cada) = 64 bytes, deve passar mesmo
  // tendo exatamente 32 "caracteres" (JS .length usa UTF-16 code units,
  // mas o guard usa TextEncoder — bytes UTF-8 reais).
  const secret = "é".repeat(32);
  assert.equal(sessionConfigError("production", secret), null);
});

test("dev (development) sem segredo -> null (dev-stub permitido, comportamento preservado)", () => {
  assert.equal(sessionConfigError("development", undefined), null);
});

test("test (NODE_ENV=test) sem segredo -> null (dev-stub permitido)", () => {
  assert.equal(sessionConfigError("test", undefined), null);
});

test("NODE_ENV indefinido sem segredo -> null (dev-stub permitido)", () => {
  assert.equal(sessionConfigError(undefined, undefined), null);
});

test("dev com segredo curto -> null (fora de producao, comprimento do segredo eh irrelevante)", () => {
  assert.equal(sessionConfigError("development", "short"), null);
});
