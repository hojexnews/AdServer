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

## Como criar um novo ADR

1. Copie [`template.md`](template.md) para `NNNN-titulo-em-kebab.md` (próximo número).
2. Preencha contexto, decisão, alternativas e consequências.
3. Abra PR; ao mergear, status vira `Aceito` e a linha entra no índice acima.
