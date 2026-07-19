# Atualizar o head do PR #1 (fork `agenciastudio/AdServer`)

O trabalho canónico vive no ramo **`feat/console-brand-logo`** de
**`hojexnews/AdServer`** (origin, via SSH). O **PR #1**
(`hojexnews/AdServer#1`) é cross-fork — head =
`agenciastudio:feat/console-brand-logo` → base = `hojexnews:main` — por isso o
PR só reflete novos commits depois de o **ramo do fork** ser atualizado.

## Porque não dá para empurrar o fork de qualquer ambiente

O push do fork precisa de **duas** coisas ao mesmo tempo:

1. **Escrita em `agenciastudio/AdServer`** — a chave SSH tem de pertencer à conta
   `agenciastudio` (ou a um colaborador com escrita). Uma chave da conta
   `hojexnews` **não** escreve no fork (`permission denied`).
2. **Poder empurrar `.github/workflows/*`** — o ramo toca ficheiros de workflow.
   Um push **https** com token OAuth/PAT **sem o scope `workflow`** é **rejeitado**
   (`refusing to allow an OAuth App to … workflow … without workflow scope`).

→ **Empurrar por SSH** (contorna o problema do scope `workflow`) a partir de uma
máquina autenticada como `agenciastudio`.

## Opção A — avançar o head do fork (recomendada, fast-forward, sem `--force`)

O commit já está em `hojexnews`; só falta espelhá-lo no fork. Não é preciso
checkout nem mexer na working tree.

```bash
# 1) Confirmar que a chave SSH desta máquina é a da conta agenciastudio:
ssh -T git@github.com          # deve saudar "Hi agenciastudio!"

# 2) Num clone qualquer do AdServer, garantir o remoto canónico e buscar o ramo:
git remote add hojexnews git@github.com:hojexnews/AdServer.git 2>/dev/null || true
git fetch hojexnews feat/console-brand-logo

# 3) Empurrar o ramo (tal como está no canónico) para o fork, por SSH:
git push git@github.com:agenciastudio/AdServer.git \
  hojexnews/feat/console-brand-logo:refs/heads/feat/console-brand-logo
```

Nota: o push acima usa a **ref** `hojexnews/feat/console-brand-logo` (não um SHA
fixo), por isso fica correto mesmo que o ramo tenha avançado desde este documento.

### Verificar

```bash
# head do fork = head do canónico:
git ls-remote --heads git@github.com:hojexnews/AdServer.git   feat/console-brand-logo
git ls-remote --heads git@github.com:agenciastudio/AdServer.git feat/console-brand-logo
# os dois SHAs devem bater certo.

# o PR #1 passa a apontar para esse SHA:
gh pr view 1 --repo hojexnews/AdServer --json headRefOid,state,mergeable
```

Se **tiver** de usar https em vez de SSH, o PAT tem de incluir o scope
**`workflow`** — caso contrário os ficheiros `.github/workflows/*` fazem o push
falhar com o mesmo erro.

## Opção B — evitar o fork (se o acesso a `agenciastudio` estiver perdido)

Como `hojexnews/AdServer` (origin) já tem o ramo, e a conta `hojexnews` tem
escrita nesse repo, pode abrir um **PR same-repo** que não depende do fork:

```bash
# Autenticado como hojexnews (ou com escrita em hojexnews/AdServer):
gh pr create --repo hojexnews/AdServer \
  --base main --head feat/console-brand-logo \
  --title "Fase 3 + E11 zonas Hojex News" \
  --body  "Inclui as zonas reais por-placement do site (E11)."
gh pr close 1 --repo hojexnews/AdServer   # fecha o antigo cross-fork
```

Mais limpo a longo prazo se o fork for uma fonte recorrente de atrito.

---

**O trabalho já está seguro** em `hojexnews/AdServer` (ramo
`feat/console-brand-logo`) — isto é só para o PR/fork refletir o commit.
A Opção A é a de menor mudança (mantém o PR #1, só avança o head).
