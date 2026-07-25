---
description: Varredura profunda de erros e falsos-positivos — lista por gravidade, prova por mutação, corrige e re-verifica
argument-hint: "[addon ou família de gate; vazio = todas as famílias]"
---

**Contexto obrigatório:** @.claude/prompts/contexto-ancora.md — leia antes de agir.

# 6. Busca profunda e correção de erros

Trabalhe com os subagentes adequados para fazer uma **varredura profunda** em busca de
erros de **todos os tipos** — lógica, sintaxe, integração, dependências, testes,
configuração — e de **falsos-positivos da malha de gates**.

Alvo desta onda: **$ARGUMENTS** (vazio = todas as famílias).

> **Liste primeiro tudo o que encontrar, classificado por gravidade; só então corrija
> cada item, confirmando que a correção não introduz novos problemas.**

## Superfícies a varrer

As famílias derivam de `docs/plano-desenvolvimento-por-addon.md` §3.1…§3.10 + §4.1/§4.2,
mais **coerência doc↔código** (13ª família, adicionada na 30ª onda). **Re-derive a lista
do plano** em vez de confiar nesta:

`contratos/proto (TX-1)` · `no-float/dinheiro (TX-2)` · `ledger/billing/double-entry` ·
`segurança/multi-tenancy/ACL (TX-3, CA-1)` · `privacidade/PII/OTel/C2PA (TX-5, DA-11)` ·
`paridade/goldens (CA-1…CA-9)` · `decisão/hot path/capping/pacing (DA-3…DA-6, TX-4)` ·
`dados/ClickHouse/Iceberg/dedupe/IVT (DA-7)` · `ML/calibração/ONNX/OPE` ·
`copiloto/HITL/RAG/guardrails` · `front/BFF/a11y/sessão` ·
`plataforma/IaC/kyverno/OpenBao/supply-chain` · `coerência doc↔código`.

## Padrão de execução (o que as ondas 27ª–31ª provaram funcionar)

**Fase A — find (dono por família, em paralelo).** Cada dono procura, na sua família:
- gate **tautológico** (satisfeito por substring no corpus, por comentário, por
  reimplementação local em vez do código de produção);
- gate **oco** (sem sentinela de asserção; skip silencioso; dependência ausente que
  faz o teste ser SKIPPED e o job verde);
- gate **órfão da CI** (existe no `make`, não roda em nenhum workflow — ou roda só sob
  build tag que a CI não passa);
- **escopo estreito** (`scope-blindspot`: allowlist por nome de arquivo/diretório;
  regex por linha física; wrapper que esconde o tipo; enum copiado à mão);
- **falso-RED** (gate vermelho por motivo espúrio: path com espaço, binário ausente);
- **doc-lie** (doc promete gate/linter/regra que não existe ou não roda);
- e o que nada disso cobre: **código faltando** que a doc afirma existir.

**Fase B — cético (default-refute), obrigado a EXECUTAR a mutação.** Cada candidato vai
a um verificador independente cuja postura padrão é **refutar**. Ele só confirma com
`run_verified=true`: comando, saída **antes** (verde) e **depois da mutação**
(vermelho, nomeando o defeito). Protocolo: `cp` backup → mutar → rodar → `mv` restaurar.
**Nunca** `git add -N` + `git checkout --` (apaga o arquivo).

**Fase C — correção por bundles-dono em arquivos disjuntos.** Cada fix:
- corrige a **FORMA**, não a instância (lista hardcoded → glob derivado com sentinela;
  denylist de nomes → default-deny sobre o tipo; cópia de enum → derivação com
  exaustividade em compile-time);
- mantém o fix e **remove só a sonda** de mutação;
- é reprovado por mutação de novo, depois do fix (agora o gate pega).

**Fase D — barreira de guardiões sobre o diff COMPLETO da onda.** Os 5 (`money`,
`security`, `privacy`, `parity`, `tech-lead`) revisam o que o sweep produziu. **Todo
sweep gera seus próprios falsos-positivos** — foi a barreira que pegou a exclusão
por-linha do no-float (29ª), a migration órfã (30ª) e o scanner de IP que decapitava
IPv6 (31ª). Bloqueio de guardião é remediado **na mesma onda**.

**Fase E — re-gate de 1ª mão** (§4 do contexto-âncora), saída colada.

## Classificação de gravidade

| Nível | Critério |
|---|---|
| **CRITICAL** | distorce dinheiro, vaza dado entre tenants, expõe PII/IP, ou faz um gate aprovar o que deveria reprovar em invariante de produção |
| **HIGH** | gate não impõe o que promete; teste valida reimplementação; job verde sem verificar; bug que quebra caminho real sob infra |
| **MEDIUM** | cobertura estreita com bypass conhecido; doc-lie que induz decisão errada |
| **LOW** | higiene, ruído, resíduo, número em prosa desatualizado |

## Antes de fechar

- Nenhum achado é declarado sem `run_verified=true` (ou marcado explicitamente
  `run_verified=false` com o motivo de infra e a escalada ao tech-lead).
- **Torne o código novo visível ao índice** antes de alegar cobertura: os guards
  selecionam por `git ls-files` e são **cegos a arquivo untracked**.
- Nenhuma sonda de mutação sobrevive no working tree.
- Residuais não-bloqueantes ficam registrados para a onda seguinte.
