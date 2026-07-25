---
description: Planejamento amplo do projeto — valida/atualiza o plano por addon e abre o próximo quando o atual fecha
argument-hint: "[escopo opcional: addon, fase ou 'tudo']"
---

**Contexto obrigatório:** @.claude/prompts/contexto-ancora.md — leia antes de agir.

# 1. Planejamento do Projeto

Trabalhe com os subagentes necessários para manter um plano de desenvolvimento **amplo
e detalhado para cada addon**, dividido em etapas claras, cada etapa atribuída ao
subagente dono e cada decisão relacionada à seção correspondente da documentação.
**Se o plano atual já estiver concluído, inicie o próximo.**

Escopo desta rodada: **$ARGUMENTS** (vazio = todos os addons).

## Regra de entrada — o plano JÁ EXISTE

`docs/plano-desenvolvimento-por-addon.md` é o documento canônico (fan-out de 13
subagentes, §3.1…§3.10 + §4 gates + §5 próximo plano + §6 ressalvas). **Não o
reescreva do zero.** O trabalho é: validar contra o código, fechar o que fechou,
abrir o que vem — como **patch**, preservando a numeração `E1…En` de cada escada.

## Etapas

1. **Estado de 1ª mão.** Derive o estado real (`git log`, `git status`, README log de
   ondas, §5 do plano). Registre divergências entre o que o plano afirma e o que o
   código mostra — elas são achados, não ruído.

2. **Fan-out de validação por addon** — um agente por família, em paralelo, cada um
   com o dono da tabela do §3 do contexto-âncora. Cada agente responde, **por etapa
   `En` do seu addon**:
   - status real (`concluída` / `próxima` / `gated`) com **evidência** (arquivo:linha,
     commit, ou comando de gate que prova);
   - se `concluída`: qual gate executável prova hoje — nome nominal do alvo `make`;
   - se `gated`: o que exatamente destrava (infra viva **ou** gatilho mensurável);
   - refs de doc citadas na etapa: **verificadas** (seção existe, diz o que a etapa
     alega) ou **quebradas**.

3. **Adjudicação (`tech-lead-architect`).** Consolide, resolva conflitos entre addons,
   e decida o sequenciamento. Aplique a regra de ouro: **começar enxuto e correto;
   tecnologia pesada só sob número medido anexado a ADR sucessor.**

4. **Fecho ou abertura de plano.**
   - Se o plano corrente do §5 ainda tem item **código-endereçável**: liste-os em
     ordem de execução com dono e gate, e pare — o `/proximo-passo` executa.
   - Se o plano corrente está **concluído** (é o caso de G0, 7/7): abra o próximo
     explicitamente. O próximo movimento real é **G1 (cutover de infra), gated por
     aprovação humana** — o entregável de código é **checklist + preparo**, nunca a
     execução do cutover. Em paralelo, o trabalho de teclado legítimo é a próxima
     **onda de varredura de integridade de gate** (`/varredura`).

5. **Patch no documento.** Atualize `§3.x`, `§5` e `§6` com o que mudou. Marque
   `✅ concluída` **apenas** com gate executável citado nominalmente. Registre
   residuais e ressalvas doc↔código novas em §6.

## Saída esperada

- Tabela `addon → etapa → status → evidência → dono → gate`.
- Delta contra o plano anterior (o que mudou de status e por quê).
- **Plano corrente**: nome, itens ordenados, donos, gates, e o que o encerra.
- Ressalvas doc↔código novas (para o `/coerencia`).
- Nenhuma marcação de conclusão sem gate que rode hoje.
