---
description: Driver do loop de desenvolvimento — escolhe o estágio certo pelo estado do repo e executa uma iteração completa
argument-hint: "[forçar estágio: plano | doc | addon | passo | executar | varredura | coerencia | fechar]"
---

**Contexto obrigatório:** @.claude/prompts/contexto-ancora.md — leia antes de agir.

# Ciclo de desenvolvimento — uma iteração

Você é o condutor do loop. **Escolha o estágio pelo estado real do repo**, execute-o até
o fim, e termine dizendo qual será o próximo estágio e por quê.

Estágio forçado nesta iteração: **$ARGUMENTS** (vazio = decidir pela árvore abaixo).

## Passo 1 — Estado de 1ª mão (sempre)

```bash
git log --oneline -8
git status --porcelain
git branch --show-current
grep -n "ª onda" README.md | tail -4
```

Cheque também **sessão concorrente** (`ps aux | grep -i claude`, mtimes dos alvos). Se
houver outra sessão viva: escopo sem colisão, backup em patch, e **não interrompa sweep
alheio no meio**.

## Passo 2 — Árvore de decisão (pare no primeiro que casar)

| Condição | Estágio | Comando |
|---|---|---|
| Working tree com trabalho de onda **não fechado** | finalizar | `/coerencia` → `/fechar-onda` |
| Guardião **bloqueou** e a remediação não voltou à barreira | remediar | `/fechar-onda` (fase 4) |
| Existe **residual de código** registrado na última onda | executar | `/executar` |
| Existe item `próxima/pendente` numa escada `E<n>` do plano §3.x | executar | `/proximo-passo` |
| Plano corrente **concluído** e sem item de código | varrer | `/varredura` |
| Doc↔código divergente (ressalva §6 aberta) | coerência | `/coerencia` |
| Escopo novo sem âncora normativa | documentar | `/doc-tecnica` → `/plano-addon` |
| Plano do §5 encerrado por inteiro | replanejar | `/plano-projeto` |
| Só restam G1…G4 (infra) ou S1…S8 (gatilho) | **PARAR** | entregar checklist e pedir aprovação humana |

**Estado-âncora atual:** G0 código-completo (7/7); próximo movimento real = **G1, gated
por aprovação humana**. Portanto, na ausência de residual de código, a rota padrão do
loop é **`/varredura` → `/coerencia` → `/fechar-onda`** — a onda seguinte de integridade
da malha de gates.

## Passo 3 — Executar o estágio

Siga o comando escolhido **na íntegra**, incluindo:

- atribuição ao subagente dono (tabela §3 do contexto-âncora);
- ancoragem de cada decisão na seção de doc **verificada**;
- prova por **mutação** de todo gate que a iteração criar ou alegar;
- re-gate de **1ª mão** com saída colada;
- barreira de guardiões sobre o diff, sem CRITICAL/HIGH.

## Passo 4 — Fechar a iteração

Termine sempre com:

```text
Estágio executado: <qual> — <por quê, dado o estado>
Entregue: <diff resumido>
Gates: <comandos + resultado, 1ª mão>
Guardiões: <PASS/BLOQUEIO por guardião>
Residuais: <lista para a próxima iteração>
Próximo estágio: <qual> — <gatilho que o seleciona>
```

## Limites do loop (não os cruze sozinho)

- Nenhum `tofu apply`, cutover, provisionamento cloud ou injeção de segredo real.
- Nenhum merge para `main` que não tenha passado pela barreira de guardiões.
- Nenhuma marcação de `CA-n`/etapa como concluída sem gate que rode **hoje**.
- Nenhuma tecnologia pesada (Flink, Triton/GPU, TigerBeetle, Fireblocks, oráculo, Feast)
  sem o número medido anexado a um ADR sucessor.
- Se a iteração não tem trabalho legítimo, **diga isso e pare** — inventar escopo é a
  falha mais cara num repo código-completo.
