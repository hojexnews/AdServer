---
description: Produz documentação técnica de implementação nas 5 seções obrigatórias, ancorada nos IDs normativos
argument-hint: "<addon, incremento ou funcionalidade a documentar>"
---

**Contexto obrigatório:** @.claude/prompts/contexto-ancora.md — leia antes de agir.

# 2. Documentação técnica

Produza a documentação técnica para orientar o setor de desenvolvimento sobre:
**$ARGUMENTS**

Critérios obrigatórios: **clareza, coerência, coesão e concisão**, com todos os
detalhes técnicos necessários para a implementação.

## Estrutura obrigatória

1. **Objetivo** — o que o incremento entrega e para quem, em uma frase verificável.
2. **Escopo** — dentro e **fora** do escopo, explicitamente. O que fica de fora precisa
   dizer *sob qual gatilho* entraria.
3. **Decisões de arquitetura (com justificativa na documentação existente)** — cada
   decisão referencia o ID que a sustenta (`DA-n`, `TX-n`, `ADR-000n §X`, `stack §2.x`).
   Decisão **nova** que não deriva de nenhum ID existente **não vai no corpo do texto**:
   abre ADR sucessor em `docs/adr/` a partir de `docs/adr/template.md`.
4. **Interfaces / contratos** — assinaturas, schemas, envelopes de evento, DDL, rotas,
   flags e seus defaults. Contrato de evento é Protobuf sob `proto/` com compat
   **BACKWARD** (TX-1); dinheiro é `Money` int64 + scale, **nunca float** (TX-2).
5. **Critérios de aceitação** — cada critério mapeado a um `CA-n` existente **ou**
   declarado como novo, e **cada um com o comando de gate que o prova**. Critério sem
   comando executável é aspiração, e deve ser marcado como tal.

## Regras invioláveis desta tarefa

- **`docs/documentacao-tecnica.md` §5 é canônico para os `CA-n`.** A legenda de 4
  estados (`[x]` provado por gate citado nominalmente · `[~]` parcial com a lacuna
  declarada · `[ ]` sem gate · `N/A-legado` revogado com justificativa e sucessor) é
  obrigatória. **Nada é marcado sem gate que rode hoje.** Subrepresentar é aceitável;
  superrepresentar não.
- **Ref sempre qualificada pelo documento** (`stack §2.3`, nunca `§2.3`).
- **ADR não se emenda retroativamente** — divergência vira ADR sucessor.
- **Número em prosa apodrece**: contagem de teste/arquivo é derivada do comando na
  hora, com o comando citado ao lado; ou não é escrita.
- Reproduzir em prosa uma lista que vive em código (escopo de gate, lista de
  migrations, enum) **cria doc-lie por construção**. Aponte para a fonte única
  (ex.: `contracts/lint/no-float.md` §Escopo) em vez de copiar.

## Execução

- Convoque o **dono do addon** (tabela §3 do contexto-âncora) para o conteúdo técnico e
  o `tech-lead-architect` para ancoragem, sequenciamento e abertura de ADR.
- Antes de publicar, mande **um cético** verificar **cada ref citada** abrindo a seção:
  ela existe? diz o que o texto alega? Ref quebrada é bloqueio de publicação.
- Onde o documento tocar dinheiro, privacidade ou multi-tenancy, passe pelo guardião
  correspondente antes de fechar.

## Saída esperada

Documento em `docs/` (ou seção nova no documento existente) nas 5 seções, mais:
- tabela `critério → gate → estado` (com a legenda de 4 estados);
- lista de refs verificadas;
- ADR aberto, se houve decisão nova.
