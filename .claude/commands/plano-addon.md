---
description: Plano detalhado de um addon — mapeia o que já existe e o que será reaproveitado ANTES de propor código novo
argument-hint: "<addon: schema | decision | data | ml | copiloto | frontend | payments | platform | money | parity>"
---

**Contexto obrigatório:** @.claude/prompts/contexto-ancora.md — leia antes de agir.

# 3. Planejamento de Addon — **$ARGUMENTS**

Trabalhe com os subagentes adequados para gerar um plano de desenvolvimento amplo e
detalhado **para este addon**, dividido em etapas claras.

> **Antes de propor qualquer implementação nova, mapeie os addons existentes e indique
> explicitamente quais serão reaproveitados e como.** Este passo é obrigatório e vem
> primeiro. Neste repo, "implementar do zero o que já existe" é a falha mais cara: as
> Fases 0–3 estão código-completas — quase tudo que parece faltar **já tem dono, arquivo
> e gate**.

## Etapa 0 (obrigatória) — Inventário de reaproveitamento

Antes de escrever uma linha de plano, produza a tabela de reuso. Superfícies a varrer:

| Superfície | Onde olhar |
|---|---|
| Contratos de evento | `proto/adserver/**`, `gen/go`, `gen/ts`, `contracts/` |
| Hot path Go | `internal/**` (cascade, ranker, capping, geo, configload, ledger, privacyscan), `services/**` |
| Dados / analytics | `data/**`, `db/**` (migrations, seed, schemas) |
| ML | `ml/**` (features, training, calibration, ope), `services/ranker-sidecar/**` |
| Copiloto | `bff/src/routers/copilot.ts`, pacotes Python do copiloto |
| Front/BFF | `web/console/src/**`, `bff/src/**` |
| Plataforma | `platform/**`, `deploy/**`, `.github/workflows/**` |
| Gates e DX | `make/*.mk`, `scripts/ci/**`, `contracts/lint/**` |

Para cada item candidato do plano novo, a tabela responde:
`necessidade → já existe? (arquivo:linha) → reaproveita como? (chamar / estender /
parametrizar / nada a fazer) → o que falta de fato`.

**Regra:** só entra no plano o que sobrar depois desta subtração. Se a subtração zerar
o addon, **diga isso** — é um resultado válido e frequente aqui.

## Etapas do plano

1. **Ler a escada existente** do addon em `docs/plano-desenvolvimento-por-addon.md`
   §3.x. As etapas já são numeradas `E1…En` — **continue a numeração**, não reinicie.
2. **Convocar o dono** (tabela §3 do contexto-âncora) para propor as etapas restantes,
   cada uma com: objetivo, arquivos tocados, dependências (inter-addon incluídas, ver
   §2 do plano), **gate que a prova**, e status (`próxima` / `gated`).
3. **Ancorar cada decisão** na seção correspondente da documentação (`DA-n`, `§4.x`,
   `CA-n`, `TX-n`, `stack §2.x`, `ADR-000n §X`) — ref qualificada pelo documento.
4. **Separar rigorosamente** os três tipos de pendência:
   - **código** (endereçável agora, sem infra) → é o único que vira trabalho hoje;
   - **ativação** (bloqueada por infra viva) → vira item de checklist de G1/G2;
   - **gatilho** (bloqueada por número medido ou spec) → vira sucessor S-n, com o
     número que o destrava explicitado. Nunca antecipar.
5. **Revisão adversarial:** um cético verifica cada "não existe ainda" da Etapa 0 com
   `grep`/`rg` de 1ª mão. Alegação de ausência não verificada é bloqueio.
6. **Gates do addon** listados nominalmente (coluna da tabela §3), mais os guardiões
   transversais aplicáveis (dinheiro / segurança / privacidade / paridade).

## Saída esperada

- **Tabela de reaproveitamento** (Etapa 0) — primeiro, sempre.
- Escada `E<n>…E<n+k>` com dono, dependências, ref de doc e gate por etapa.
- Classificação código / ativação / gatilho.
- Patch proposto para `§3.x` do plano canônico.
