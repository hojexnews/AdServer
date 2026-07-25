---
description: Conduz a próxima tarefa de código do Hojex Ads conforme a documentação — explica, mostra a previsão na doc, executa e confirma
argument-hint: "[tarefa específica, opcional]"
---

**Contexto obrigatório:** @.claude/prompts/contexto-ancora.md — leia antes de agir.

# 5. Condução da execução

Trabalhe com os subagentes adequados para executar a **próxima tarefa de
desenvolvimento de código** do **Hojex Ads** (a plataforma de anúncios publicitários),
**conforme a documentação**.

Tarefa desta rodada: **$ARGUMENTS** (vazio = derivar pelo `/proximo-passo`).

## Protocolo — os 3 tempos, em ordem, sem pular

### 1. Explique o que será feito
Uma descrição verificável: arquivos que serão tocados, comportamento que muda, flag e
default, e o que **não** muda. Se a tarefa toca dinheiro, privacidade, multi-tenancy ou
o hot path, diga isso explicitamente aqui — determina quais guardiões entram.

### 2. Mostre onde isso está previsto na documentação
Cite a seção, qualificada pelo documento, e **abra-a para conferir que ela diz o que
você alega**:
`docs/documentacao-tecnica.md` (`DA-n`, `§4.x`, `CA-n`) · `docs/stack-tecnologico.md`
(`TX-n`, `§2.x`) · `docs/adr/000{1..4}` (`I/J/K`) ·
`docs/plano-desenvolvimento-por-addon.md` (`§3.x-E<n>`, `§5`) · `docs/ops/go-live-runbook.md`.

**Se não estiver previsto em lugar nenhum, pare.** O caminho é `/doc-tecnica` (ou ADR
sucessor) primeiro — código sem âncora normativa é escopo inventado.

### 3. Execute e confirme o resultado antes de avançar
- Dono do addon implementa (tabela §3 do contexto-âncora).
- O incremento **inclui o gate que o prova**, e o gate é provado não-tautológico por
  **mutação** (`cp` backup → mutar → gate VERMELHO nomeando o defeito → `mv` restaurar).
- Re-gate de 1ª mão verde, saída colada (§4 do contexto-âncora).
- Guardiões aplicáveis PASS sem CRITICAL/HIGH sobre o diff.
- Só então: próxima tarefa.

## Invariantes que não se negociam durante a execução

- `float` proibido para dinheiro (TX-2); multi-moeda **sem conversão automática** (DA-10).
- Compat **BACKWARD** no schema de eventos (TX-1).
- Sem PII / sem IP bruto em evento, WAL ou log (TX-5 / DA-11).
- **Cascata é a autoridade final** (DA-3); ML/deep só re-rankeia dentro do estrato, com
  fail-open, **sem mudar tier nem faturável**.
- Faturável reconcilia contra **Iceberg**, nunca contra streaming (DA-7); a UI nunca soma
  "ao vivo" com "≤1h".
- ACL **só server-side** (CA-1); toda escrita do copiloto passa por **HITL**.
- Nenhum `tofu apply` / cutover / provisionamento cloud sem **aprovação humana explícita**.

## Se a sprint atual já estiver concluída

Então o trabalho é **verificar e solucionar falsos positivos**: abra a próxima onda de
varredura de integridade da malha de gates (`/varredura`). Esse é o modo declarado
pós-G0 — a malha de gates é o produto tanto quanto o código que ela guarda. Confirme
antes que o item não é, na verdade, *código faltando* mascarado por gate frouxo (foi o
achado da 30ª: calibração isotônica nunca lida no caminho servido).
