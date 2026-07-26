# Loop de desenvolvimento — prompts do AdServer (Hojex News)

Os 8 prompts do ciclo de desenvolvimento, materializados como **slash commands
project-local**, mais um driver que escolhe o estágio pelo estado do repo.

> **Começando?** Leia o **[guia prático](guia-loop.md)** — como rodar, o que
> esperar de cada volta, as dez regras, a barreira de guardiões, calibragem de
> custo e o checklist de fecho.

Todos compartilham o mesmo contexto-âncora: **[`contexto-ancora.md`](contexto-ancora.md)**
— estado do projeto, roster de subagentes, gates `make` reais, protocolos de mutação e
regras de ancoragem documental. Editar um invariante lá vale para os 9 comandos.

## Os comandos

| # | Comando | Papel | Quando |
|---|---|---|---|
| 1 | `/plano-projeto` | Planejamento amplo: valida o plano por addon contra o código e abre o próximo quando o atual fecha | plano do §5 encerrado |
| 2 | `/doc-tecnica` | Documentação técnica nas 5 seções obrigatórias (Objetivo · Escopo · Decisões · Interfaces · Aceitação) | escopo novo sem âncora normativa |
| 3 | `/plano-addon` | Plano de um addon — **inventário de reaproveitamento primeiro** | addon vai receber trabalho novo |
| 4 | `/proximo-passo` | Deriva e executa o próximo passo do plano corrente | continuidade |
| 5 | `/executar` | Conduz a tarefa: explica → mostra na doc → executa → confirma | trabalho de código do dia |
| 6 | `/varredura` | Varredura profunda de erros e falsos-positivos, provada por mutação | modo padrão pós-G0 |
| 7 | `/coerencia` | Nomenclatura, estilo, duplicação, contratos, alinhamento doc↔código | antes de fechar |
| 8 | `/fechar-onda` | Erros restantes + limpeza + re-gate + guardiões + registro + commit/push | fecho da onda |
| — | `/ciclo-dev` | **Driver**: escolhe o estágio pelo estado do repo e roda uma iteração completa | entrada do loop |

## Como rodar o loop

```text
/ciclo-dev                      # uma iteração, estágio escolhido pelo estado do repo
/ciclo-dev varredura            # força um estágio
/loop /ciclo-dev                # loop auto-pautado (o modelo decide a cadência)
/loop 30m /ciclo-dev            # loop com intervalo fixo
```

Rota padrão hoje (G0 código-completo, G1 gated por aprovação humana):
**`/varredura` → `/coerencia` → `/fechar-onda`** — a próxima onda de integridade da
malha de gates. A numeração da próxima onda é a **32ª** (ver a colisão registrada no
`README.md` do repo).

## Contratos que os 9 comandos impõem

- **Gate verde não é prova de gate real** — só a mutação que ele deveria pegar prova.
- **Auto-relato de subagente ≠ re-gate de 1ª mão.**
- **Barreira de 5 guardiões** sobre o diff completo, sem CRITICAL/HIGH.
- **Ancoragem obrigatória**: toda decisão cita a seção verificada, qualificada pelo
  documento (`stack §2.3`, nunca `§2.3`).
- **Nada é marcado como concluído sem gate que rode hoje.**
- **Nenhuma ação destrutiva/remota em cloud sem aprovação humana explícita.**
