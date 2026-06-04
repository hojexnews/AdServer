# Architecture Decision Records (ADR)

Decisões arquiteturais **com consequência** ficam registradas aqui, uma por
arquivo, numeradas e imutáveis. Um ADR não é editado depois de `Aceito`: se a
decisão muda, cria-se um **novo** ADR que **supersede** o anterior (e marca-se o
antigo como `Substituído por ADR-N`).

## Por que ADRs neste projeto

Os documentos normativos (`docs/documentacao-tecnica.md`, `docs/stack-tecnologico.md`)
descrevem o **alvo**. Os ADRs registram as **escolhas pontuais** que resolvem
pendências e bifurcações — em especial as marcadas como "decisão de produto
bloqueante" e as "perguntas que destravam decisões" (stack §6). Cada ADR cita as
decisões-âncora (`DA-n`) e transversais (`TX-n`) que o sustentam.

## Status possíveis

`Proposto` · `Aceito` · `Substituído por ADR-N` · `Descartado`

## Índice

| ADR | Título | Status | Âncoras |
|-----|--------|--------|---------|
| [0001](0001-near-real-time-nao-e-requisito-v1.md) | Near-real-time (1–5s) não é requisito de v1/v2; frescor "ao vivo" vem do ClickHouse, faturável continua batch horário | Aceito | DA-7, TX-1, §2.2, §5 |
| [0002](0002-fase-1-sequenciamento-e-layout.md) | Layout do monorepo (módulo Go único), perguntas abertas (BFF/latência/capping/atribuição/volume) e sequenciamento da Fase 1 | Aceito | TX-1…5, DA-3/6/7/10, CA-1…7, §2.1/2.2/2.6/4/6 |
| [0003](0003-fase-2-sequenciamento-ml-copiloto.md) | Sequenciamento da Fase 2 (ML re-ranker dentro da cascata + copiloto), layout (`internal/ranker`/sidecar/`ml/`/`services/copilot`/`db/vector`) e cookieless (§6 q.5) | Aceito | TX-1…5, DA-3/4/6/7/11, CA-1…9, §2.2/2.3/2.4/2.5/4/6 |

## Como criar um novo ADR

1. Copie [`template.md`](template.md) para `NNNN-titulo-em-kebab.md` (próximo número).
2. Preencha contexto, decisão, alternativas e consequências.
3. Abra PR; ao mergear, status vira `Aceito` e a linha entra no índice acima.
