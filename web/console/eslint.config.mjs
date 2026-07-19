// ESLint flat config — Next.js 16 nativo (G0/frontend E10, alinhamento §2.5).
// O Next 16 removeu `next lint`; o gate agora é `eslint` direto (npm run lint).
// eslint-config-next 16 publica configs FLAT (`./core-web-vitals`) — usá-las via
// FlatCompat.extends quebra ("circular structure"), então importamos a flat nativa.
import { defineConfig, globalIgnores } from "eslint/config";
import nextVitals from "eslint-config-next/core-web-vitals";

// GLOB por CONVENCAO DE NOME (mesma filosofia do MONEY_GLOBS do eslint.config.mjs
// raiz do repo, escopo BFF), MAIS toda pagina do App Router — casa:
//   1. nome de arquivo com money/ledger/billing/payments (lib/, components/ui/
//      fora de src/app/, ex.: web/console/src/lib/money.ts,
//      web/console/src/components/ui/money-display.tsx); e
//   2. TODO src/app/**/*.tsx — qualquer pagina do console e formulario/dashboard
//      potencial de preco vindo do BFF (rate, budget, bid, cpm/cpc/cpa...). O
//      glob por NOME sozinho (so item 1) deixava passar paginas cujo arquivo se
//      chama so "page.tsx" sem "money"/"billing" no nome — FP #2 (29ª onda):
//      src/app/campaigns/page.tsx captura Rate (rateAmount, string DECIMAL) e
//      escapava por completo das regras de Number(.../literal-float abaixo
//      (so a regra global de parseFloat rodava; prova:
//      `npx eslint --print-config src/app/campaigns/page.tsx` antes do fix
//      mostrava 1 seletor, contra 3 em src/lib/money.ts). MONEY_TSX_EXCLUDE
//      abaixo existe para uma excecao pontual e documentada — vazia por
//      padrao, pois nao ha hoje nenhuma pagina sob src/app/ comprovadamente
//      livre de dado monetario.
// Ver contracts/lint/no-float.md §2, web/console/src/lib/money.ts,
// web/console/src/components/ui/money-display.tsx, web/console/src/app/billing/**,
// e o backstop por CONTEUDO scripts/ci/no-float-ts.sh (defesa em profundidade:
// esta config cobre por GLOB, aquele script cobre por CO-OCORRENCIA textual
// independente de glob).
const MONEY_TSX_EXCLUDE = [];

const MONEY_GLOBS = [
  "src/**/*money*.ts",
  "src/**/*money*.tsx",
  "src/**/*ledger*.ts",
  "src/**/*ledger*.tsx",
  "src/**/*billing*.ts",
  "src/**/*billing*.tsx",
  "src/**/*payments*.ts",
  "src/**/*payments*.tsx",
  "src/app/billing/**/*.ts",
  "src/app/billing/**/*.tsx",
  "src/app/**/*.tsx",
  ...MONEY_TSX_EXCLUDE.map((glob) => `!${glob}`),
];

const eslintConfig = defineConfig([
  ...nextVitals,
  {
    rules: {
      // Money safety: proíbe parseFloat em qualquer contexto (TX-2/DA-10 — dinheiro
      // é sempre string DECIMAL via decimal.js, nunca float no cliente).
      "no-restricted-syntax": [
        "error",
        {
          selector: "CallExpression[callee.name='parseFloat']",
          message:
            "parseFloat é proibido. Use decimal.js para valores monetários (TX-2/DA-10).",
        },
      ],
    },
  },
  {
    // TX-2 regras 3/4 (contracts/lint/no-float.md §2), escopadas aos arquivos de
    // dinheiro do console — Number(...) e literal float eram os 2 de 4 checks
    // documentados que NENHUM gate aplicava aqui (só o parseFloat acima, global,
    // rodava). NOTA: um bloco `files:` posterior no array substitui por INTEIRO
    // (não mescla) o array de options de uma regra já definida por outro bloco
    // que também case o arquivo (confirmado empiricamente: ESLint flat config
    // faz "last matching wins" por NOME de regra, não merge de array) — por
    // isso o seletor parseFloat é REPETIDO aqui, senão ficaria "esquecido" nos
    // arquivos de dinheiro assim que este bloco mais específico entrasse em vigor.
    files: MONEY_GLOBS,
    rules: {
      "no-restricted-syntax": [
        "error",
        {
          selector: "CallExpression[callee.name='parseFloat']",
          message:
            "parseFloat é proibido. Use decimal.js para valores monetários (TX-2/DA-10).",
        },
        {
          selector: "CallExpression[callee.name='Number']",
          message:
            "Number(...) é proibido em dinheiro (TX-2/DA-10). Use decimal.js (Decimal) para conversão de exibição, nunca para aritmética.",
        },
        {
          selector: "Literal[raw=/^[0-9]+\\.[0-9]+$/]",
          message:
            "Literal float é proibido em dinheiro (TX-2/DA-10): use string DECIMAL + decimal.js.",
        },
      ],
    },
  },
  // Substitui os ignores default do eslint-config-next (a flat config não os aplica sozinha).
  globalIgnores([".next/**", "out/**", "build/**", "next-env.d.ts"]),
]);

export default eslintConfig;
