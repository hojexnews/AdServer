---
description: Deriva e executa o próximo passo do plano corrente, com dono, gate e evidência
argument-hint: "[restringir a um addon, opcional]"
---

**Contexto obrigatório:** @.claude/prompts/contexto-ancora.md — leia antes de agir.

# 4. Continuidade do desenvolvimento

Trabalhe com os subagentes adequados para **executar as tarefas do próximo passo**.
Restrição desta rodada: **$ARGUMENTS** (vazio = derivar do plano).

## Como derivar "o próximo passo" (nesta ordem, pare no primeiro que existir)

1. **Onda em curso não fechada** — `git status` com trabalho não-commitado de onda:
   o próximo passo é **fechá-la** (`/coerencia` → `/fechar-onda`), não abrir outra.
   Verifique também a **colisão de numeração** registrada no README (o trabalho de
   console/`web-ci` deve virar a **32ª**).
2. **Residual registrado** ao fim da última onda (README + plano §5) que seja
   código-endereçável **sem infra**.
3. **Item `próxima/pendente`** na escada `E<n>` de algum addon (§3.x do plano).
4. **Próxima onda de varredura** de integridade de gate — o modo declarado pós-G0
   quando não há item de código pendente (`/varredura`).
5. **Nada acima existe** → o que resta é G1/G2/G3/G4 (infra) ou S1…S8 (gatilho):
   **não execute**. Entregue o checklist de pré-condições e **pare para aprovação
   humana**.

## Execução

- **Explique antes:** o que será feito, por quê agora, e **onde isso está previsto na
  documentação** (ref qualificada: `DA-n` / `§4.x` / `CA-n` / `TX-n` / `stack §2.x` /
  `ADR-000n §X` / plano §3.x-E<n>).
- **Atribua ao dono** (tabela §3 do contexto-âncora). Trabalho que cruza addons vira
  bundles em **arquivos disjuntos**, executados em paralelo; conflito de arquivo entre
  bundles é erro de decomposição, não de merge.
- **Todo incremento nasce com o gate que o prova.** Se o invariante não tem gate hoje,
  o gate faz parte do incremento — e precisa ser **provado não-tautológico por mutação**
  (`cp` backup → mutar → gate VERMELHO → `mv` restaurar).
- **Sem infra, sem chute:** o que depende de Postgres/ClickHouse/cluster real e não pode
  ser verificado fica marcado `run_verified=false`, documentado e escalado — nunca
  "corrigido às cegas".

## Confirmação antes de avançar

Não declare o passo concluído sem:
- gate do addon **verde, rodado de 1ª mão**, com a saída colada (auto-relato de
  subagente não conta);
- guardiões aplicáveis PASS sem CRITICAL/HIGH sobre o **diff**;
- o passo seguinte nomeado.

## Saída esperada

`passo escolhido → justificativa → ref de doc → dono → diff → gate + saída →
guardiões → próximo passo`.
