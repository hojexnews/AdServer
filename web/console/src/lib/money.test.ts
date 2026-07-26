/**
 * Testes de formatMoney/formatCtr/formatCount (TX-2 / DA-10).
 *
 * ACHADO (31ª onda, console-money-formatting-zero-coverage): lib/money.ts é o
 * ÚNICO ponto por onde valor monetário chega à tela do anunciante, e não tinha
 * nenhum teste. O invariante "dinheiro é string DECIMAL, nunca float" era
 * afirmado só em comentário e no lint (que casa `Number(...)`/`parseFloat`,
 * não `.toNumber()`).
 *
 * O que estes testes travam:
 *   1. A string autoritativa NUNCA é alterada pela formatação.
 *   2. Moeda não-ISO (cripto) cai no fallback e imprime a string INTEGRAL,
 *      sem passar por float — é o caminho de USDC/AEV/BND.
 *   3. Precisão: valores com muitas casas/magnitude alta não são silenciosamente
 *      corrompidos no caminho de fallback.
 */

import { test } from "node:test";
import assert from "node:assert/strict";

const { formatMoney, formatCtr, formatCount } = await import("./money.ts");

test("formatMoney BRL: formata para exibição sem alterar a string autoritativa", () => {
  const money = { amount: "12.34", currency: "BRL" };
  const out = formatMoney(money, "pt-BR");

  assert.ok(out.includes("12"), `esperava o valor no texto, veio: ${out}`);
  assert.equal(money.amount, "12.34", "formatMoney NUNCA pode mutar o MoneyWire");
});

test("formatMoney cripto (USDC, não-ISO 4217): fallback imprime a string DECIMAL INTEGRAL", () => {
  // 6 casas — precisão nativa de USDC. Se algum dia isto virar float,
  // a asserção abaixo quebra.
  const out = formatMoney({ amount: "1.500000", currency: "USDC" }, "en-US");
  assert.equal(out, "USDC 1.5");
});

test("formatMoney cripto com 18 casas (padrão ERC-20): nenhum dígito é perdido", () => {
  // Um float (double) só tem ~15-17 dígitos significativos: se este valor
  // passasse por Number(), os últimos dígitos virariam lixo.
  // AEV tem 3 letras, logo NÃO cai no fallback: o Intl aceita qualquer código
  // bem-formado de 3 letras. Este é exatamente o caminho onde o `.toNumber()`
  // antigo corrompia o valor (31ª onda).
  const out = formatMoney({ amount: "123456.789012345678", currency: "AEV" }, "en-US");
  assert.ok(
    out.includes("123,456.789012"),
    `os dígitos significativos têm de vir do decimal exato, veio: ${out}`,
  );
});

test("formatMoney: magnitude alta em cripto não vira notação científica nem arredonda", () => {
  // > 2^53: com o `.toNumber()` antigo isto virava ...994.00 (valor ERRADO na
  // tela). Com a string, o Intl formata a partir do decimal exato.
  const out = formatMoney({ amount: "9007199254740993.000001", currency: "BND" }, "en-US");
  assert.ok(
    out.includes("9,007,199,254,740,993"),
    `magnitude acima de 2^53 tem de sobreviver exata, veio: ${out}`,
  );
  assert.ok(
    !out.includes("740,994"),
    `regressão de float: o valor foi arredondado para cima — ${out}`,
  );
  assert.ok(!out.includes("e+"), "nunca notação científica para dinheiro");
});

test("formatMoney: valor zero e negativo são representáveis", () => {
  assert.ok(formatMoney({ amount: "0", currency: "USDC" }, "en-US").includes("0"));
  assert.ok(formatMoney({ amount: "-5.25", currency: "USDC" }, "en-US").includes("5.25"));
});

test("formatCtr: string de taxa vira percentual (não é dinheiro, mas segue o padrão)", () => {
  const out = formatCtr("0.05234", "en-US");
  assert.ok(out.includes("5.23"), `esperava ~5.23%, veio: ${out}`);
  assert.ok(out.includes("%"));
});

test("formatCount: inteiros grandes usam notação compacta; pequenos não", () => {
  assert.ok(!formatCount(999, "en-US").includes("K"));
  assert.ok(/[MK]/.test(formatCount(2_500_000, "en-US")));
});
