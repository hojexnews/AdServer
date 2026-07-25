---
description: Resolve incoerências e inconsistências de código — nomenclatura, estilo, duplicação, contratos entre funções, alinhamento com a arquitetura documentada
argument-hint: "[addon ou caminho; vazio = diff da onda corrente]"
---

**Contexto obrigatório:** @.claude/prompts/contexto-ancora.md — leia antes de agir.

# 7. Coerência e consistência de código

Trabalhe com os subagentes adequados para resolver **todos os erros, incoerências e
inconsistências** de código. Escopo: **$ARGUMENTS** (vazio = diff da onda corrente,
`git diff` + `git diff --staged` + untracked relevante).

## Dimensões (uma passada por dimensão, dono por addon)

1. **Nomenclatura** — convenção por linguagem: Go (`MixedCaps`, erros `Err*`, pacote
   minúsculo), TS (`camelCase`, componente `PascalCase`), Python (`snake_case`), SQL
   (`snake_case`), Protobuf (`PascalCase` msg, `snake_case` campo, `UPPER_SNAKE` enum).
   Nome de arquivo **nunca** é critério de escopo de gate — mas é critério de legibilidade.
2. **Estilo** — `gofmt`/`golangci-lint` (`.golangci.yml`), `eslint.config.mjs` (raiz) +
   o do console, `buf format`, formatação Python. Rodar o formatador é preferível a
   discutir estilo.
3. **Duplicações** — a classe mais cara aqui: **cópia sincronizada por comentário**.
   Enum copiado, lista de migrations hardcoded, escopo de gate replicado em prosa,
   reimplementação de lógica dentro do teste. Todas viram **derivação única**:
   glob/reflexão/`satisfies`/introspecção, com sentinela anti-vazio.
4. **Contratos entre funções** — assinatura, ordem de validação, erro propagado vs.
   engolido, `context` propagado, parâmetro que a query não usa (o bug de produção do
   ledger da 31ª: `$2` enviado sem o SQL referenciá-lo → SQLSTATE 42P18). Onde houver
   fronteira de módulo, o contrato tem teste **contra o código de produção**, não contra
   um stub.
5. **Alinhamento com a arquitetura documentada** — o código faz o que `DA-n` / `§4.x` /
   `TX-n` / `ADR-000n` dizem? Divergência tem dois desfechos legítimos: corrigir o
   código, ou corrigir a doc/abrir ADR sucessor. **Nunca** deixar os dois discordando.
6. **Coerência doc↔código** — ressalvas do plano §6, refs bare (`§2.3` sem documento),
   contagens em prosa, comentário que descreve comportamento que o código não tem.
   Ao consertar um bloco de doc por honestidade, **limpe o snippet inteiro** — o exemplo
   vizinho quebrado engana igual (lição da 29ª).

## Regras

- **Mudança de coerência não muda comportamento.** Se mudar, deixa de ser esta tarefa e
  vira incremento com gate próprio (`/executar`).
- Toda unificação de duplicata precisa provar que a derivação **cobre o que a cópia
  cobria** — mutar um caso que só a cópia pegava e ver o gate vermelho.
- Renomeação atravessa `gen/`, migrations e docs: confira com `rg` antes de fechar.
- Guardiões aplicáveis revisam o diff, mesmo sendo "só estilo" — a exclusão por-linha
  do no-float (29ª) entrou disfarçada de limpeza.

## Saída esperada

**Apresente um resumo das mudanças feitas ao final**, agrupado por dimensão:
`dimensão → arquivo:linha → o que estava incoerente → o que passou a ser → como foi
verificado`. Mais: gates verdes rodados de 1ª mão e ressalvas §6 fechadas ou abertas.
