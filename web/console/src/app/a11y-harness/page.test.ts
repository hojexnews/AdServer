/**
 * Testes do GATE DE PRODUÇÃO de /a11y-harness (G0/frontend E10).
 *
 * FP #15 (29ª onda de correção de falsos-positivos, hollow/MEDIUM): o
 * invariante "sem A11Y_HARNESS=1 no build, a rota 404 sempre — inclusive em
 * produção real" era afirmado em page.tsx (comentário), make/web.mk,
 * .github/workflows/a11y.yml e harness-client.tsx, mas NENHUM job de CI
 * exercitava o caminho NEGATIVO: web-a11y builda com A11Y_HARNESS=1 e só
 * afirma 200 (caminho positivo); web-test nunca sobia a rota. Uma mutação
 * `if (process.env.A11Y_HARNESS !== "1")` → `if (false)` removeria o gate
 * por completo sem que NENHUM teste pegasse (comprovado abaixo, revertido).
 *
 * ESTE arquivo chama a FUNÇÃO DE PRODUÇÃO REAL (A11yHarnessPage, o default
 * export de ./page.tsx) — não uma cópia/reimplementação do predicado — e
 * afirma que ela lança o erro de notFound() do next/navigation quando
 * A11Y_HARNESS não é exatamente "1", e NÃO lança quando é "1".
 *
 * PROBLEMA DE INFRAESTRUTURA DE TESTE (por que isto não é um simples
 * `import "./page.ts"` como session-guard.test.ts): o console não tem
 * jest/vitest (ver make/web.mk); node:test roda .ts nativamente via
 * type-stripping, mas NÃO sabe transformar JSX/.tsx (page.tsx é .tsx e
 * contém `<A11yHarness />` na última linha). middleware.test.ts resolve um
 * problema parecido para middleware.ts (que é .ts, sem JSX) com um resolve
 * hook mínimo (test-node-loader-hook.mjs); aqui o problema é maior (JSX real
 * + um import (`./harness-client`) cuja árvore transitiva puxa
 * react-querybuilder (com um `import "*.css"`) e vários componentes do
 * design system — pesado e irrelevante para ESTE gate, que só testa a
 * guarda de topo de página).
 *
 * FIX deste arquivo: compila o CONTEÚDO REAL de page.tsx (lido do disco, não
 * duplicado) com o MESMO compilador que o Next usa para essa página em
 * produção — `next/dist/build/swc` (SWC nativo via @next/swc-*, já uma
 * dependência obrigatória do `next` instalado) — depois de duas
 * substituições textuais MÍNIMAS, ambas por razão de AMBIENTE (Node puro
 * fora do bundler), não de comportamento:
 *   1. `"next/navigation"` → `"next/navigation.js"` — o pacote `next` não
 *      declara `exports` no package.json (mesmo motivo documentado em
 *      test-node-loader-hook.mjs para `next/server`); a resolução ESM do
 *      Node exige a extensão explícita, o bundler já resolve para o mesmo
 *      arquivo (next/navigation.js é literalmente `module.exports =
 *      require("./dist/client/components/navigation")`, o alvo real).
 *      `notFound()` em si não depende de nenhum contexto de request do
 *      Next — é só `new Error(digest)` + throw (ver
 *      node_modules/next/dist/client/components/not-found.js) — chamá-la
 *      fora de uma renderização real é seguro e determinístico.
 *   2. o import de `./harness-client` (árvore pesada, irrelevante para o
 *      gate) é substituído por um STUB local de mesmo nome
 *      (`function A11yHarness() { return null; }`) — o teste NUNCA
 *      renderiza o componente (só verifica se a função de página lança OU
 *      retorna um elemento React; criar o elemento via `jsx(A11yHarness,{})`
 *      não invoca o corpo do componente).
 * O resultado compilado é escrito em web/console/.next/test-tmp/ (já
 * ignorado pelo git — ver .gitignore raiz `web/console/.next/`) para que a
 * resolução de módulo bare-specifier do Node (react/jsx-runtime,
 * next/navigation.js) encontre web/console/node_modules subindo os
 * diretórios pais; removido em `after()` com fallback síncrono em
 * `process.on("exit")`.
 *
 * MUTAÇÃO-PROVA (rodada manualmente, não commitada): trocar
 * `if (process.env.A11Y_HARNESS !== "1")` por `if (false)` em page.tsx faz
 * o teste "sem A11Y_HARNESS -> lança 404" FALHAR (ver 29ª onda).
 */

import { test, before, after } from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import crypto from "node:crypto";
import { pathToFileURL } from "node:url";

const CONSOLE_DIR = path.resolve(import.meta.dirname, "..", "..", "..");
const PAGE_SOURCE_PATH = path.resolve(import.meta.dirname, "page.tsx");
const SWC_ENTRY_PATH = path.join(
  CONSOLE_DIR,
  "node_modules",
  "next",
  "dist",
  "build",
  "swc",
  "index.js"
);
const TMP_DIR = path.join(CONSOLE_DIR, ".next", "test-tmp");

type Swc = {
  loadBindings: () => Promise<unknown>;
  transformSync: (
    src: string,
    opts: Record<string, unknown>
  ) => { code: string };
};

type CompiledPage = {
  default: () => unknown;
};

let compiledPage: CompiledPage;
let compiledTmpFile: string | undefined;

function cleanupTmpFile(): void {
  if (compiledTmpFile) {
    try {
      fs.rmSync(compiledTmpFile, { force: true });
    } catch {
      // best-effort — não deve derrubar o processo de teste
    }
    compiledTmpFile = undefined;
  }
}
process.on("exit", cleanupTmpFile);

/**
 * Compila o page.tsx REAL do disco (não uma cópia) via SWC nativo do Next e
 * importa o módulo resultante. Ver docstring do arquivo para o porquê de
 * cada uma das duas substituições textuais.
 */
async function loadRealA11yHarnessPage(): Promise<CompiledPage> {
  const rawSource = fs.readFileSync(PAGE_SOURCE_PATH, "utf8");

  const navigationFixed = rawSource.replace(
    /(["'])next\/navigation\1/,
    '"next/navigation.js"'
  );
  assert.notEqual(
    navigationFixed,
    rawSource,
    "page.tsx não importa mais de 'next/navigation' como esperado — atualize este teste"
  );

  const harnessClientImportPat =
    /import\s*\{\s*A11yHarness\s*\}\s*from\s*["']\.\/harness-client["'];?/;
  assert.match(
    navigationFixed,
    harnessClientImportPat,
    "page.tsx não importa mais { A11yHarness } de './harness-client' como esperado — atualize este teste"
  );
  const stubbedSource = navigationFixed.replace(
    harnessClientImportPat,
    "function A11yHarness() { return null; }"
  );

  const swcModule = (await import(pathToFileURL(SWC_ENTRY_PATH).href)) as {
    default: Swc;
  };
  const swc = swcModule.default;
  await swc.loadBindings();

  const { code } = swc.transformSync(stubbedSource, {
    filename: "page.tsx",
    jsc: {
      parser: { syntax: "typescript", tsx: true },
      target: "es2022",
      transform: { react: { runtime: "automatic" } },
    },
    module: { type: "es6" },
  });

  fs.mkdirSync(TMP_DIR, { recursive: true });
  compiledTmpFile = path.join(
    TMP_DIR,
    `a11y-harness-page.compiled.${crypto.randomUUID()}.mjs`
  );
  fs.writeFileSync(compiledTmpFile, code, "utf8");

  return (await import(pathToFileURL(compiledTmpFile).href)) as CompiledPage;
}

before(async () => {
  compiledPage = await loadRealA11yHarnessPage();
});

after(() => {
  cleanupTmpFile();
});

/** Lança e tem o digest de notFound() (next/navigation)? */
function assertThrowsNotFound(fn: () => unknown): void {
  assert.throws(fn, (err: unknown) => {
    const digest = (err as { digest?: unknown } | null)?.digest;
    return typeof digest === "string" && digest.startsWith("NEXT_HTTP_ERROR_FALLBACK;404");
  });
}

for (const badValue of [undefined, "0", "true", "development"] as const) {
  test(`A11Y_HARNESS=${JSON.stringify(badValue)} -> rota 404 (notFound real lançado)`, () => {
    const prev = process.env.A11Y_HARNESS;
    if (badValue === undefined) delete process.env.A11Y_HARNESS;
    else process.env.A11Y_HARNESS = badValue;
    try {
      assertThrowsNotFound(() => compiledPage.default());
    } finally {
      if (prev === undefined) delete process.env.A11Y_HARNESS;
      else process.env.A11Y_HARNESS = prev;
    }
  });
}

test('A11Y_HARNESS="1" -> NÃO lança (rota habilitada)', () => {
  const prev = process.env.A11Y_HARNESS;
  process.env.A11Y_HARNESS = "1";
  try {
    const result = compiledPage.default();
    assert.notEqual(result, undefined);
  } finally {
    if (prev === undefined) delete process.env.A11Y_HARNESS;
    else process.env.A11Y_HARNESS = prev;
  }
});
