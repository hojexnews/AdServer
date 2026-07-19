// eslint.config.mjs — gate raiz TX-2 (no-float) para o STEP TypeScript do
// workflow no-float.yml. NAO e o eslint "de projeto" do bff/web (cada um tem
// o seu proprio eslint.config.* em bff/eslint.config.js e
// web/console/eslint.config.mjs) — este arquivo existe SO para o gate CI de
// dinheiro (TX-2), escopado por allowlist explicita aos arquivos TS de
// dinheiro REAIS do BFF (money.ts/payments.ts/*payments*.ts — a convencao
// real do repo e nome de ARQUIVO, nao diretorio 'money/'). Ver
// contracts/lint/no-float.md §2 e .github/workflows/no-float.yml.
//
// Resolve o parser TypeScript a partir de bff/node_modules (ja instalado via
// `npm ci --prefix bff` no proprio step do workflow, que ja o faz para o job
// bff-ci) em vez de duplicar uma arvore de node_modules na raiz do repo.
import { createRequire } from "node:module";
import path from "node:path";

const require = createRequire(
  path.join(import.meta.dirname, "bff", "package.json")
);
const tsParser = require("@typescript-eslint/parser");

// GLOB por CONVENCAO DE NOME (nao allowlist estatica): casa qualquer arquivo
// TS de dinheiro REAL do BFF cujo NOME contenha money/ledger/billing/payments
// (a convencao real do repo e nome de ARQUIVO money.ts/payments.ts dentro de
// bff/src/{schemas,routers,adapters}, nunca um diretorio money/). Deriva da
// MESMA convencao que o `git ls-files ... | grep -E '(money|ledger|billing|
// payments)'` do step TS em .github/workflows/no-float.yml — de proposito um
// GLOB e nao uma lista fixa: um arquivo de dinheiro NOVO (ex.: refunds-
// payments.ts) e coberto AUTOMATICAMENTE, sem drift silencioso (senao o step
// passaria o arquivo novo ao ESLint que responderia "no matching config" e o
// CI ficaria verde sem lintar TX-2 — falso-positivo-em-espera). Restrito a
// bff/src; gen/**, node_modules/** e web/** ficam fora (web tem seu proprio
// gate em web/console/eslint.config.mjs). Ver contracts/lint/no-float.md §2.
const MONEY_GLOBS = [
  "bff/src/**/*money*.ts",
  "bff/src/**/*ledger*.ts",
  "bff/src/**/*billing*.ts",
  "bff/src/**/*payments*.ts",
];

export default [
  // Ignore global de higiene: nunca lintar codigo gerado/dependencias/testes
  // (os *.test.ts usam numeros nao-monetarios legitimamente; o step do
  // workflow tambem os exclui via `grep -v '\.test\.ts$'`).
  { ignores: ["gen/**", "**/node_modules/**", "**/*.test.ts"] },
  {
    files: MONEY_GLOBS,
    languageOptions: {
      parser: tsParser,
      parserOptions: {
        sourceType: "module",
        ecmaVersion: "latest",
      },
    },
    rules: {
      // TX-2 regra 1 (contracts/lint/no-float.md §2): parseFloat proibido em
      // dinheiro.
      "no-restricted-globals": [
        "error",
        {
          name: "parseFloat",
          message:
            "parseFloat proibido em dinheiro (TX-2): use decimal.js/bigint.",
        },
      ],
      // TX-2 regra 3: `Number(...)` proibido em dinheiro.
      // TX-2 regra 4: literal float (ex.: `12.34`) proibido em dinheiro.
      //
      // NOTA: contracts/lint/no-float.md §2 documenta o selector
      // `CallExpression[callee.object.name='Number']`, que so casa
      // `Number.foo(x)` (MemberExpression) e NUNCA pega a chamada direta
      // `Number(x)` (callee e um Identifier simples, sem `.object`).
      // Corrigido abaixo para `callee.name='Number'`.
      "no-restricted-syntax": [
        "error",
        {
          selector: "CallExpression[callee.name='Number']",
          message:
            "Number(...) proibido em dinheiro (TX-2): use decimal.js/bigint.",
        },
        {
          selector: "Literal[raw=/^[0-9]+\\.[0-9]+$/]",
          message:
            "literal float proibido em dinheiro (TX-2): use string decimal + decimal.js.",
        },
      ],
    },
  },
];
