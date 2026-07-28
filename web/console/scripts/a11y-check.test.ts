/**
 * Gate de acessibilidade mecânica (WCAG 2.2 AA) — G0/frontend E10.
 *
 * Alvo `make web-a11y` (ver make/web.mk). NÃO faz parte do `web-ci` rápido
 * (typecheck+lint+test) — este gate builda o Next.js e sobe um browser real,
 * é lento por natureza e roda em workflow CI separado (.github/workflows/a11y.yml).
 *
 * PRÉ-REQUISITO: `next build` já rodado com A11Y_HARNESS=1 (o alvo `make
 * web-a11y` faz isso ANTES de invocar este script — ver make/web.mk). Sem
 * isso, a rota /a11y-harness 404 (gate por env, ver src/app/a11y-harness/page.tsx)
 * e este teste falha ao tentar auditar uma página 404.
 *
 * FLUXO:
 *   1. Sobe `next start` (produção, serve o build estático já gerado) numa
 *      porta fixa local.
 *   2. Espera o servidor responder.
 *   3. Abre o Chrome do SISTEMA via puppeteer-core (NÃO baixa browser —
 *      executablePath explícito, CHROME_PATH ?? /usr/bin/google-chrome).
 *   4. Injeta axe-core via @axe-core/puppeteer e audita /a11y-harness contra
 *      as tags WCAG 2.0/2.1/2.2 nível A+AA.
 *   5. Falha (assert) se houver QUALQUER violação — imprime id/impact/help/
 *      targets de cada uma antes de falhar, para triagem rápida.
 *
 * Runner: node:test nativo do Node 24 (mesmo padrão de
 * src/lib/session-guard.test.ts — o console não tem jest/vitest). Este
 * arquivo fica FORA de src/ de propósito: `make web-test` (rápido, sem
 * rede/build) faz `find src -name '*.test.ts'` e não deve pegar este teste
 * lento — ele só roda via `make web-a11y`.
 *
 * DEPENDÊNCIAS (package.json):
 *   - "puppeteer-core" (NÃO "puppeteer") — não baixa Chromium, usa
 *     executablePath explícito. @axe-core/puppeteer declara peerDependency
 *     em "puppeteer" (nome fixo do pacote — não dá pra evitar a peer dep em
 *     si); sem tratamento, `npm install` resolve essa peer automaticamente
 *     baixando o pacote "puppeteer" COMPLETO (Chromium incluso, ~200MB) —
 *     exatamente o que este gate existe pra evitar. O
 *     `"overrides": { "puppeteer": "npm:puppeteer-core@..." }` no
 *     package.json faz qualquer resolução de "puppeteer" na árvore virar um
 *     alias do mesmo puppeteer-core já instalado — sem download de browser,
 *     sem pacote duplicado.
 *   - scripts/tsconfig.json + scripts/package.json ({"type":"module"}) —
 *     projeto TS isolado do tsconfig.json do app Next (que tem "dom" lib e
 *     é typecheckado por `make web-typecheck`). Isso evita colisão de tipos
 *     entre a cópia direta de puppeteer-core e a cópia aliased via
 *     "puppeteer" (mesma versão, mas TS trata campos #private de classes
 *     como nominais por instalação). `make web-a11y` roda
 *     `tsc --noEmit -p scripts/tsconfig.json` como pré-check rápido.
 */

import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { randomBytes } from "node:crypto";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { launch } from "puppeteer-core";
import { AxePuppeteer } from "@axe-core/puppeteer";

// @axe-core/puppeteer resolve axe-core internamente via um shim
// (esbuild __require Proxy) que falha em resolver o pacote quando este
// script roda como ESM nativo do Node (node:test com type-stripping) neste
// repo — provavelmente por causa do espaço no caminho absoluto do repo
// ("Hojex News"). Resolvemos e lemos o source do axe-core NÓS MESMOS (via
// createRequire local, sem o shim problemático) e passamos explicitamente
// no construtor de AxePuppeteer(page, source) — API pública documentada,
// evita depender do resolve automático interno.
const require = createRequire(import.meta.url);
const AXE_CORE_SOURCE = readFileSync(require.resolve("axe-core"), "utf8");

const SCRIPT_DIR = path.dirname(fileURLToPath(import.meta.url));
const WEB_CONSOLE_DIR = path.resolve(SCRIPT_DIR, "..");
const NEXT_BIN = path.join(WEB_CONSOLE_DIR, "node_modules", ".bin", "next");

const PORT = Number(process.env.A11Y_PORT ?? 4319);
const BASE_URL = `http://127.0.0.1:${PORT}`;
const CHROME_PATH = process.env.CHROME_PATH ?? "/usr/bin/google-chrome";

// Tags WCAG 2.0/2.1/2.2 nível A + AA (mandato: WCAG 2.2 AA).
const AXE_TAGS = ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"];

// Páginas auditadas (wave 30, achado a11y-ci-harness-only-real-pages-unaudited).
//
// Antes desta correção, PAGES_TO_AUDIT só continha "/a11y-harness" (rota
// sintética, client-only, fixtures estáticas dos componentes do design
// system isolados) — as 9 rotas REAIS do console (2332+ linhas de JSX real:
// formulários RHF+Zod, tabelas, o dashboard com gráfico Recharts, o builder
// de segmentação real) nunca eram renderizadas nem auditadas pelo axe-core
// em CI nenhum.
//
// COBERTURA HONESTA (o que este gate PROVA e o que NÃO prova):
//   Este runner NÃO sobe o BFF. ATENÇÃO — o motivo MUDOU na 31ª onda e este
//   comentário foi corrigido junto (achado R1-a11y-doclie-rewrite-rationale):
//   a justificativa anterior dizia que "next.config.ts só reescreve /api/trpc/*
//   quando NODE_ENV!=='production'". Essa condicional NÃO EXISTE MAIS: o
//   proxy para o BFF passou a ser um route handler
//   (src/app/api/trpc/[trpc]/route.ts) que vale em TODO ambiente, porque a
//   ausência dele em produção deixava o console inteiro sem control-plane.
//   O motivo real, hoje, é mais simples: este runner não INICIA um processo do
//   BFF nem um Postgres, então /api/trpc/* alcança o route handler e recebe
//   502 (upstream inalcançável). Subir a stack inteira aqui transformaria um
//   gate de acessibilidade em teste de integração — fora do escopo deste alvo.
//   Consequência mensurável por página:
//     - /rules e /copilot: NÃO dependem de nenhuma query tRPC para o
//       primeiro render — o formulário do builder (RHF+Zod, <select> de
//       vetor, cada campo do form) e o layout do chat renderizam por
//       INTEIRO e são auditados de verdade. Foi precisamente a ausência
//       desta cobertura que deixou passar a regressão de contraste do <h1>
//       de /rules (ver a mutação de regressão abaixo).
//     - /billing: os headings/seções (h1, h2, aria-labelledby) renderizam
//       incondicionalmente; só o CONTEÚDO de cada seção fica atrás de
//       isLoading/error (que também são auditados, aninhados).
//     - /advertisers, /banners, /campaigns, /sites, /zones, /dashboard:
//       fazem `if (isLoading) return <LoadingState/>` / `if (error) return
//       <ErrorState/>` ANTES do JSX específico da página — sem BFF, a
//       query tRPC falha (retry:1, ver components/providers.tsx) e a
//       página estaciona no ErrorState compartilhado. Isso AINDA é uma
//       auditoria real (prova que o wrapper de rota + ErrorState daquela
//       página específica são acessíveis — o mesmo tipo de regressão de
//       contraste/estrutura pode viver no wrapper), mas NÃO alcança o JSX
//       de tabela/formulário do caminho de sucesso. Não finjo cobertura
//       além disso: fechar esse restante exige um BFF real acessível a
//       partir de `next start` (infra pendente — ver next.config.ts) e fica
//       para uma wave futura.
const PAGES_TO_AUDIT = [
  "/a11y-harness",
  "/advertisers",
  "/campaigns",
  "/banners",
  "/sites",
  "/zones",
  "/rules",
  "/dashboard",
  "/billing",
  "/copilot",
];

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function waitForServer(url: string, timeoutMs = 60_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  let lastError: unknown = null;
  while (Date.now() < deadline) {
    try {
      const res = await fetch(url, { signal: AbortSignal.timeout(2_000) });
      // Qualquer resposta HTTP (mesmo 404) significa que o servidor subiu.
      if (res.status > 0) return;
    } catch (err) {
      lastError = err;
    }
    await sleep(500);
  }
  throw new Error(
    `next start não respondeu em ${timeoutMs}ms em ${url}. Último erro: ${String(lastError)}`
  );
}

test(
  "a11y-harness: 0 violações WCAG 2.2 AA (axe-core via Chrome do sistema)",
  { timeout: 120_000 },
  async (t) => {
    // ---------------------------------------------------------------------
    // 1. Sobe `next start` servindo o build já gerado (com A11Y_HARNESS=1)
    //
    // `next start` roda em NODE_ENV=production, o que aciona o fail-closed
    // do middleware de sessão (G0/frontend E9, TX-3/CA-1): SEM SESSION_SECRET
    // válido (>=32 bytes), TODA rota casada pelo matcher retorna 500 — por
    // design, não é um bug a contornar. Este runner supre um SESSION_SECRET
    // EFÊMERO E ALEATÓRIO (gerado em memória, nunca persistido/logado, válido
    // só para este processo `next start` local/localhost que é morto ao fim
    // do teste) — isto SATISFAZ o guard de produção corretamente, em vez de
    // enfraquecê-lo ou contorná-lo com um servidor estático hand-rolled que
    // pularia o middleware/roteamento real do Next (perdendo fidelidade e,
    // pior, mascarando uma futura regressão real no middleware).
    // /a11y-harness não exige sessão (AUTH_REQUIRED_PATTERNS só cobre
    // /api/copilot/* e /api/trpc/*) — o segredo aqui só destrava o guard de
    // configuração do topo do middleware, não autentica nada.
    const server = spawn(NEXT_BIN, ["start", "-p", String(PORT)], {
      cwd: WEB_CONSOLE_DIR,
      stdio: ["ignore", "pipe", "pipe"],
      env: {
        ...process.env,
        SESSION_SECRET: randomBytes(32).toString("base64"),
      },
    });

    let serverOutput = "";
    server.stdout?.on("data", (chunk) => {
      serverOutput += String(chunk);
    });
    server.stderr?.on("data", (chunk) => {
      serverOutput += String(chunk);
    });

    t.after(async () => {
      server.kill("SIGTERM");
      // Dá tempo do processo encerrar antes do runner sair.
      await sleep(200);
    });

    await waitForServer(`${BASE_URL}/a11y-harness`).catch((err) => {
      throw new Error(`${err.message}\n\n--- next start output ---\n${serverOutput}`);
    });

    // ---------------------------------------------------------------------
    // 2. Abre o Chrome do sistema via puppeteer-core (sem download de browser)
    // ---------------------------------------------------------------------
    const browser = await launch({
      executablePath: CHROME_PATH,
      headless: true,
      args: ["--no-sandbox", "--disable-dev-shm-usage", "--disable-gpu"],
    });
    t.after(() => browser.close());

    const allViolations: Array<{ page: string; violation: import("axe-core").Result }> = [];

    for (const pagePath of PAGES_TO_AUDIT) {
      const page = await browser.newPage();
      const response = await page.goto(`${BASE_URL}${pagePath}`, {
        waitUntil: "networkidle0",
      });

      assert.ok(
        response && response.status() === 200,
        `esperava HTTP 200 em ${pagePath} (gate A11Y_HARNESS=1 no build), recebeu ${response?.status()}`
      );

      const results = await new AxePuppeteer(page, AXE_CORE_SOURCE)
        .withTags(AXE_TAGS)
        .analyze();

      for (const violation of results.violations) {
        allViolations.push({ page: pagePath, violation });
      }

      await page.close();
    }

    // ---------------------------------------------------------------------
    // 2b. Focus-trap do modal HITL (SC 2.4.3 / aria-modal). O axe-core faz
    //     análise ESTÁTICA de DOM e NÃO simula Tab — ausência de focus-trap
    //     passa despercebida. Aqui dirigimos o teclado de verdade: focamos o
    //     1º focável do diálogo e afirmamos que Tab/Shift+Tab mantêm o foco
    //     DENTRO do `[role="dialog"]` (o modal HITL do harness), nunca escapando
    //     para o fundo. Guarda de regressão do fix de G0/frontend E10.
    // ---------------------------------------------------------------------
    {
      const page = await browser.newPage();
      await page.goto(`${BASE_URL}/a11y-harness`, { waitUntil: "networkidle0" });

      const hasDialog = await page.$('[role="dialog"]');
      assert.ok(hasDialog, "o harness deve montar o modal HITL (role=dialog)");

      // Foca o primeiro botão focável do diálogo (API do puppeteer com seletor —
      // roda no browser, sem referenciar `document` no TS do script).
      await page.focus('[role="dialog"] button:not([disabled])');

      // O callback roda NO BROWSER; o tsconfig do script (Node) não tem lib DOM
      // — e adicioná-la colidiria com os globais fetch/Response do @types/node —
      // então acessamos os globais do browser via globalThis tipado como any.
      const focusInsideDialog = () =>
        page.evaluate(() => {
          const doc = (globalThis as { document?: unknown }).document as
            | {
                querySelector(sel: string): { contains(n: unknown): boolean } | null;
                activeElement: unknown;
              }
            | undefined;
          const d = doc?.querySelector('[role="dialog"]');
          return !!(d && doc?.activeElement && d.contains(doc.activeElement));
        });

      assert.ok(await focusInsideDialog(), "foco inicial deveria estar dentro do modal HITL");

      // Tab várias vezes (> nº de focáveis do diálogo) — o trap deve ciclar.
      for (let i = 0; i < 6; i++) {
        await page.keyboard.press("Tab");
        assert.ok(
          await focusInsideDialog(),
          `focus-trap ausente: o foco escapou do modal HITL após Tab #${i + 1}`
        );
      }
      // Shift+Tab no sentido inverso.
      for (let i = 0; i < 6; i++) {
        await page.keyboard.down("Shift");
        await page.keyboard.press("Tab");
        await page.keyboard.up("Shift");
        assert.ok(
          await focusInsideDialog(),
          `focus-trap ausente: o foco escapou do modal HITL após Shift+Tab #${i + 1}`
        );
      }

      await page.close();
    }

    // ---------------------------------------------------------------------
    // 3. Reporta cada violação (id/impact/help/targets) antes de falhar
    // ---------------------------------------------------------------------
    if (allViolations.length > 0) {
      console.error(`\n=== axe-core: ${allViolations.length} violação(ões) encontrada(s) ===\n`);
      for (const { page: pagePath, violation } of allViolations) {
        console.error(`[${pagePath}] ${violation.id} (impact=${violation.impact})`);
        console.error(`  help: ${violation.help}`);
        console.error(`  helpUrl: ${violation.helpUrl}`);
        for (const node of violation.nodes) {
          console.error(`  target: ${JSON.stringify(node.target)}`);
          console.error(`  html: ${node.html}`);
          if (node.failureSummary) {
            console.error(`  failureSummary: ${node.failureSummary.replace(/\n/g, " ")}`);
          }
        }
        console.error("");
      }
    }

    assert.equal(
      allViolations.length,
      0,
      `${allViolations.length} violação(ões) de acessibilidade WCAG 2.2 AA encontrada(s) — ver detalhes acima`
    );
  }
);
