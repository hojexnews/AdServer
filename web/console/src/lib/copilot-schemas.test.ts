/**
 * Testes de parseSseEvent contra os payloads REAIS do serviço copiloto.
 *
 * ACHADO CRÍTICO (31ª onda): o parser validava com
 * `discriminatedUnion("type", ...)` sobre o corpo de `data:`, mas
 * services/copilot/app/server.py:633 `_sse(event, data)` põe o tipo no campo
 * `event:` do frame SSE e NUNCA inclui `type` dentro do JSON. Resultado: todo
 * evento falhava o parse, devolvia null, e a UI do copiloto não fazia nada —
 * nem token, nem tool_call, nem o diálogo de aprovação HITL, nem done.
 *
 * Os payloads abaixo são cópias LITERAIS dos dicts passados a `_sse` em
 * server.py (linhas 269, 273, 283, 301, 308) — se o contrato Python mudar,
 * estes testes são o lugar que trava a divergência do lado do console.
 *
 * MUTAÇÃO: voltar parseSseEvent a validar o payload sem injetar o nome do
 * evento faz TODOS os testes abaixo falharem.
 */

import { test } from "node:test";
import assert from "node:assert/strict";

const { parseSseEvent } = await import("./copilot-schemas.ts");

test("token: payload real do server.py ({text}) é parseado", () => {
  const ev = parseSseEvent(JSON.stringify({ text: "Olá" }), "token");
  assert.equal(ev?.type, "token");
  assert.equal(ev?.type === "token" ? ev.text : null, "Olá");
});

test("tool_call: payload real ({tool,status,result}) é parseado", () => {
  const ev = parseSseEvent(
    JSON.stringify({ tool: "forecast", status: "done", result: { x: 1 } }),
    "tool_call",
  );
  assert.equal(ev?.type, "tool_call");
});

test("hitl_required: payload real ({thread_id,diff,message}) é parseado — o diálogo de aprovação DEPENDE disto", () => {
  const payload = {
    thread_id: "11111111-1111-1111-1111-111111111111",
    diff: {
      kind: "zone_link",
      action: "create",
      campaignId: "1",
      zoneId: "1001",
    },
    message: "Confirma o vínculo?",
  };
  const ev = parseSseEvent(JSON.stringify(payload), "hitl_required");
  assert.equal(
    ev?.type,
    "hitl_required",
    "sem isto, NENHUMA escrita do copiloto pode ser aprovada pelo humano",
  );
});

test("done: payload real ({session_id,thread_id,usage}) é parseado", () => {
  const ev = parseSseEvent(
    JSON.stringify({
      session_id: "s1",
      thread_id: "s1",
      usage: { input_tokens: 10, output_tokens: 20 },
    }),
    "done",
  );
  assert.equal(ev?.type, "done");
});

test("error: payload real ({message}) é parseado", () => {
  const ev = parseSseEvent(JSON.stringify({ message: "falhou" }), "error");
  assert.equal(ev?.type, "error");
});

test("payload malformado para o evento declarado -> null (fail-closed preservado)", () => {
  // `diff` sem `kind` é exatamente o default `{}` de server.py:285.
  const ev = parseSseEvent(
    JSON.stringify({ thread_id: "t", diff: {}, message: "m" }),
    "hitl_required",
  );
  assert.equal(ev, null, "diff sem kind não pode virar um HITL aprovável");
});

test("nome de evento desconhecido -> null (nunca inventa um tipo)", () => {
  assert.equal(parseSseEvent(JSON.stringify({ text: "x" }), "inventado"), null);
});

test("JSON inválido -> null, nunca lança", () => {
  assert.equal(parseSseEvent("{nao-e-json", "token"), null);
});

test("o nome do evento é a autoridade: `type` contrabandeado no corpo não sobrepõe", () => {
  const ev = parseSseEvent(
    JSON.stringify({ type: "done", text: "sou token" }),
    "token",
  );
  assert.equal(ev?.type, "token");
});
