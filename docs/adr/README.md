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
| [0005](0005-perfil-beta-dependencia-unica-postgres.md) | Perfil BETA de dependência única (Postgres): telemetria, capping e stats "ao vivo" atrás das interfaces existentes, para tornar a plataforma testável ponta-a-ponta sem Docker/Redpanda/ClickHouse — sem tocar o caminho faturável (DA-7) | Aceito | TX-2/3/5, DA-6/7/11, CA-1/5/6, §4.7/4.8, stack §2.2/2.5, ADR-0001/0002 |
| [0004](0004-fase-3-sequenciamento-ia-avancada-cripto.md) | Sequenciamento da Fase 3 (deep ranking gated por uplift A/B + fraude não-superv. + cripto/AEV-BND fora do hot path), `ChainConnector`/custódia Safe→Fireblocks, layout (`services/payments`/`internal/chainconnector`/`ml/deep`/`db/compliance`/células PCI+AML) e as 10 perguntas de §3 (defaults + gatilhos) | Aceito | TX-1…6, DA-3/4/7/10/11, CA-1…9, §2.3/2.6/2.7/3/4/5 |

## Como criar um novo ADR

1. Copie [`template.md`](template.md) para `NNNN-titulo-em-kebab.md` (próximo número).
2. Preencha contexto, decisão, alternativas e consequências.
3. Abra PR; ao mergear, status vira `Aceito` e a linha entra no índice acima.
