---
description: Fecha a onda — resolve erros restantes, limpa resíduos, re-gate de 1ª mão, barreira de guardiões, registro honesto, commit e push
argument-hint: "[número da onda, ex.: 32]"
---

**Contexto obrigatório:** @.claude/prompts/contexto-ancora.md — leia antes de agir.

# 8. Finalização dos erros pendentes

Trabalhe com os subagentes adequados para **resolver os erros restantes** e, em seguida,
**limpar os resíduos gerados pelas correções**. Confirme ao final que o addon está
**estável e limpo**.

Onda: **$ARGUMENTS** (vazio = derive do README; confira a colisão de numeração
registrada — o trabalho de console/`web-ci` deve ser a **32ª**).

## 1. Erros restantes

- Reúna tudo que ficou aberto: achados `MEDIUM`/`LOW` adiados, bloqueios de guardião
  ainda não remediados, residuais da onda anterior que esta onda tocou.
- Cada um: corrigir **a forma** e provar por mutação, ou **declarar residual** com
  motivo (infra ausente / gatilho / escopo) e registrá-lo — silêncio não é fecho.

## 2. Limpeza de resíduos

```bash
git status --porcelain          # nada além da entrega
git diff --stat
rg -n "TODO\(sweep\)|XXX-mutation|\.bak$|\.orig$" --hidden -g '!node_modules'
```

- **Sonda de mutação** esquecida no código (a 31ª deixou duas violações TX-2 vivas por
  um `TaskStop` no meio do sweep) — varra com `make no-float` **depois** de tornar o
  código novo visível ao índice (`git add` real; guards selecionam por `git ls-files`).
- Backup (`cp`) de mutação, arquivo temporário, script de investigação → scratchpad, não
  o repo.
- Código comentado, import não usado, log de depuração, contagem em prosa desatualizada.
- Artefato local **e remoto**: branch de trabalho, tag de teste, workflow-run órfão.

## 3. Re-gate de 1ª mão (obrigatório, saída colada)

```bash
make verify
make go-build go-vet go-test go-lint
make parity-golden-short
make ml-test data-validate db-lint copilot-test
make bff-ci web-ci
make platform-validate
make db-check-migration-pairing db-check-schema-list
```
Rode também os gates específicos do que a onda tocou (§4 do contexto-âncora).
**Auto-relato de subagente não substitui isto.**

## 4. Barreira de guardiões sobre o diff completo

`money-ledger-guardian` · `security-reviewer` · `privacy-compliance-auditor` ·
`parity-golden-test-guardian` · `tech-lead-architect` → **PASS, 0 CRITICAL/HIGH**.
Bloqueio é remediado **nesta onda**, e a remediação volta à barreira.

## 5. Registro honesto

- **README.md:** blockquote da Nª onda, no formato das anteriores — o achado principal,
  os demais (cada um com a mutação que o provou), o que a barreira de guardiões pegou,
  **lições novas**, gates de 1ª mão, e **residuais não-bloqueantes** para a próxima.
- **`docs/plano-desenvolvimento-por-addon.md` §5** (bloco "Sweeps pós-G0"): parágrafo
  correspondente.
- **§6** do plano: ressalvas doc↔código fechadas ou abertas.
- Nada marcado como concluído sem gate executável citado nominalmente.

## 6. Commit + push

Padrão do projeto: **commitar e empurrar ao `origin` ao fechar cada onda**, após gates
verdes (`hojexnews/AdServer`, SSH).

```bash
git add <arquivos da entrega>     # nunca 'git add -A'
git commit -m "fix(gates): Nª onda — <achados>; <guardiões>"
git push origin <branch>
```

## Confirmação final

Declare explicitamente: **addon estável e limpo** — com (a) gates verdes colados,
(b) guardiões PASS, (c) `git status` limpo, (d) registro publicado, (e) residuais
listados para a próxima onda. Se algum item falhar, **diga qual e por quê** em vez de
declarar o fecho.
