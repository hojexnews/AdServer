// Hook de resolução de módulos usado SOMENTE por middleware.test.ts (via
// node:module `register()`). NÃO é um teste (não casa `*.test.ts`, ver
// make/web.mk::web-test) e NÃO é importado por nenhum código de produção.
//
// O console roda testes com o runner nativo do Node (node:test) sem
// bundler — mas dois padrões usados no código de produção só resolvem sob
// o bundler do Next.js (webpack/turbopack), não sob a resolução ESM padrão
// do Node:
//   1. `import ... from "next/server"` (subpath sem extensão explícita;
//      o pacote "next" não declara "exports", então o Node exige o
//      arquivo real "next/server.js").
//   2. `import ... from "@/lib/..."` (alias de path do tsconfig, só
//      entendido por bundlers — não existe em runtime puro do Node).
//
// Este hook resolve SOMENTE esses dois casos, sem alterar nenhum arquivo
// de produção (middleware.ts continua importando "next/server" e "@/*"
// normalmente — o que webpack/turbopack já resolvem sem este hook).

const SRC_DIR = new URL("../", import.meta.url); // web/console/src/

export async function resolve(specifier, context, nextResolve) {
  if (specifier === "next/server") {
    return nextResolve("next/server.js", context);
  }
  if (specifier.startsWith("@/")) {
    const target = new URL(`${specifier.slice(2)}.ts`, SRC_DIR);
    return nextResolve(target.href, context);
  }
  return nextResolve(specifier, context);
}
