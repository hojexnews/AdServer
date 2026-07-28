# Contexto-âncora — loop de desenvolvimento do AdServer (Hojex News)

> Arquivo compartilhado por **todos** os comandos de `.claude/commands/`.
> Ele existe para que cada prompt do loop seja curto e para que os invariantes
> tenham **uma** fonte. Se algo aqui divergir do repo, o **repo vence** — e
> corrigir este arquivo faz parte da onda que descobriu a divergência.

---

## 1. Estado-âncora (verificar de 1ª mão, nunca assumir)

- **Fases 0–3: código-completas e provadas na `main`.** Não há engenharia represada.
- **G0 (pré-condições de go-live sem infra): CÓDIGO-COMPLETO 7/7** desde a 23ª onda
  (`main` `b4cb624`). Ver `docs/plano-desenvolvimento-por-addon.md` §5 → G0.
- **Próximo movimento real = G1 (cutover de infra), GATED por aprovação humana explícita.**
  Nenhum `tofu apply`, cutover, provisionamento cloud ou injeção de segredo real
  acontece de forma autônoma. Se o passo seguinte for G1/G2/G3/G4, o entregável é
  **checklist + código pronto**, não a execução.
- **Portanto, o trabalho de teclado disponível hoje é:**
  1. **Varreduras de integridade da malha de gates** (o modo declarado pós-G0 —
     ondas 24ª…31ª): achar gate tautológico, oco, órfão da CI, escopo estreito,
     falso-RED e doc-lie; e o que essas varreduras revelam de **código faltando**.
  2. **Residuais registrados** ao fim de cada onda no README e no plano §5.
  3. Itens sob gatilho **só quando o número/spec que os destrava existir** (S1…S8).

**Antes de qualquer coisa, derive o estado atual — não confie neste texto:**

```bash
git log --oneline -8
git status --porcelain
grep -n "ª onda" README.md | tail -4
```

**Numeração de ondas:** a última onda commitada é a **32ª**. A colisão que este
arquivo registrava está **reconciliada**: o trabalho da outra sessão (metade BFF da
31ª + perfil BETA/ADR-0005 + console, commits `f952ae5`/`73cd2e4`/`0caeb84`) foi
absorvido pelo fecho da 32ª, que também registrou a **31ª no plano §5** — ela vivia
só no README até então. Confirme a numeração real antes de escrever qualquer registro.

**Sessão concorrente:** este repo já sofreu colisão entre sessões. Antes de editar,
cheque `git status`, `ps aux | grep -i claude` e mtimes dos arquivos-alvo. Se houver
outra sessão viva, **escopo sem colisão + backup em patch** — e **nunca interrompa um
sweep no meio**: `TaskStop` entre "mutar" e "restaurar" deixa sonda de mutação no
código (lição da 31ª).

---

## 2. Fontes normativas e regra de ancoragem

| Documento | IDs que ele governa |
|---|---|
| `docs/documentacao-tecnica.md` | `DA-1…DA-12` (decisões), `§4.1…§4.10` (contratos), `CA-1…CA-9` (aceitação) |
| `docs/stack-tecnologico.md` | `TX-1…TX-6`, `§2.1…§2.7`, `§3`, `§4`, `§5` |
| `docs/adr/0001…0004` | incrementos `I0…I4` (Fase 1), `J0…J6` (Fase 2), `K0…K8` (Fase 3) |
| `docs/plano-desenvolvimento-por-addon.md` | `§3.1…§3.10` (planos por addon), `§4` (gates), `§5` (G0…G4, S1…S8), `§6` (ressalvas doc↔código) |
| `docs/ops/go-live-runbook.md` | `§1…§8` (ativação) |
| `README.md` | log de ondas (fonte de-facto), incrementos I5 |

**Regras de citação (violá-las é produzir doc-lie):**
- Toda decisão cita a **seção verificada**, com o documento qualificado:
  `stack §2.3`, não `§2.3`. Ref bare é doc-lie — a 30ª fechou 2 dessas.
- **ADR é decisão arquivada.** Divergência entre ADR e código se resolve corrigindo
  a **ref** ou abrindo **ADR sucessor** — nunca emendando o ADR retroativamente.
- **`docs/documentacao-tecnica.md` §5 é canônico para os `CA-n`.** Legenda de 4
  estados (`[x]` / `[~]` / `[ ]` / `N/A-legado`) e a **regra inviolável: nada é
  marcado sem um gate executável que rode hoje, citado nominalmente.**
  Subrepresentar é aceitável; superrepresentar não.
- **Número em prosa apodrece.** Se escrever contagem de teste/arquivo/gate, derive-a
  do comando na hora e cite o comando; ou não escreva o número.

---

## 3. Roster — dono por addon e gate correspondente

| Addon / família | Dono (`subagent_type`) | Seção do plano | Gates canônicos |
|---|---|---|---|
| Contratos de eventos (proto/) | `schema-contracts-steward` | §3.1 | `make verify`, `proto-gen-check` |
| Motor de decisão (hot path Go) | `decision-engine-engineer` | §3.2 | `go-build/vet/test/lint`, `parity-golden` |
| Plataforma de dados / telemetria | `data-platform-engineer` | §3.3 | `data-validate`, `db-lint`, `db-test-all` |
| ML / ranking / fraude | `ml-optimization-engineer` | §3.4 | `ml-test`, `ml-calibration-test`, `ranker-onnx-fixtures` |
| Copiloto (Claude + LangGraph) | `copilot-llm-engineer` | §3.5 | `copilot-test` |
| Front-end + BFF | `frontend-bff-engineer` | §3.6 | `bff-ci`, `web-ci`, `web-a11y` |
| Pagamentos multi-trilho | `payments-crypto-engineer` | §3.7 | `db-test-compliance`, `go-test` |
| Infra / plataforma-base | `platform-infra-engineer` | §3.8 | `platform-validate` (6 checks) |
| Dinheiro / ledger / billing | `money-ledger-guardian` | §3.9 | `no-float` (6 guards), `db-test-ledger*`, `data-billing-test` |
| Paridade e testes | `parity-golden-test-guardian` | §3.10 | `parity-golden`, `parity-cutover-gate` |
| Sequenciamento / ADR / trade-off | `tech-lead-architect` | §1, §5, §6 | adjudicação |
| Segurança (read-only) | `security-reviewer` | §4.1 | sem CRITICAL/HIGH |
| Privacidade (read-only) | `privacy-compliance-auditor` | §4.2 | sem CRITICAL/HIGH |

Roster completo: `.claude/agents/README.md`.

---

## 4. Malha de gates — comandos reais (re-gate de 1ª mão)

```bash
make verify              # buf lint+format+build+breaking (TX-1) + no-float 6 guards (TX-2)
make go-build go-vet go-test go-lint
make go-test-integration # exige Postgres real; deriva pacotes por build tag
make parity-golden       # CA-2…CA-6 (parity-golden-short p/ ciclo curto)
make ml-test data-validate db-lint copilot-test
make bff-ci web-ci web-a11y
make platform-validate   # tofu + kubeconform + kyverno + otel + openbao + cell-consistency
make db-check-migration-pairing db-check-schema-list db-check-provisioners
make proto-gen-check     # fora do verify: depende de rede/plugins remotos
python3 scripts/ci/workflow-paths-mirror-check.py   # push.paths espelha pull_request.paths
# Banco de verdade (PG16 nativo, sem Docker) — o caminho que a 32ª consertou:
make dev-db-setup DEV_DB=<scratch>
DATABASE_URL="postgres://$(id -un)@/<scratch>?host=/var/run/postgresql&sslmode=disable" \
  make db-test db-test-compliance db-test-ledger db-test-ledger-immutability \
       db-test-vector db-test-stats go-test-integration
```

Notas que já causaram falso-verde/falso-RED:
- `make no-float` roda **6** guards (`scripts/ci/no-float-{proto,go,ts,py,sql,data-sql}.sh`)
  com sentinela `NO_FLOAT_SCRIPTS_EXPECTED := 6` no Makefile. **Escopo é DEFAULT-DENY**;
  a fonte normativa é `contracts/lint/no-float.md` §Escopo — **não reproduza a lista**.
- O repo vive em um path **com espaço** (hoje `/home/agencia/Agencia Studio/AdServer`;
  já foi `/home/agencia/Hojex News/AdServer` — **derive o path, não o copie daqui**):
  recipes com binário absoluto **têm de ficar aspados** (24ª onda). O gate que impõe
  isso é `make-quoting-check`, e desde a 32ª ele mora em `repo-gates.yml` — antes
  estava em `buf.yml`, cujo `paths: proto/**` o deixava órfão para todo `make/*.mk`.
- Gates que selecionam por `git ls-files` são **cegos a arquivo untracked**. Torne o
  código novo visível ao índice (`git add` real) antes de alegar cobertura.

---

## 5. Barreira de guardiões (condição de merge)

Sobre o **diff completo** da onda, e não sobre auto-relato de quem escreveu:
`money-ledger-guardian`, `security-reviewer`, `privacy-compliance-auditor`,
`parity-golden-test-guardian`, `tech-lead-architect` — **PASS sem CRITICAL/HIGH**.

Histórico que justifica a barreira: em 3 ondas seguidas ela pegou falso-positivo
**criado pelo próprio sweep** (29ª: exclusão `non_money` por-linha; 30ª: migration
`0004` órfã dos runners; 31ª: scanner de IP não-default-deny decapitando IPv6).

---

## 6. Protocolos invioláveis (lições 24ª–31ª, cada uma paga com uma onda)

1. **Gate verde não é prova de gate real.** Só a **mutação que ele deveria pegar**
   prova. Todo achado de varredura carrega `run_verified=true` com o comando e a
   saída antes/depois.
2. **Protocolo de mutação:** `cp` backup → mutar → rodar o gate → `mv` restaurar.
   **Nunca** `git add -N` + `git checkout -- <arquivo>`: `-N` grava blob de **zero
   bytes** e o checkout **apaga** o conteúdo (30ª onda).
3. **Escopo: default-deny + allowlist explícita.** Escopar gate por nome de arquivo
   ou diretório "financeiro" foi a **classe dominante de falso-positivo em 3 ondas
   seguidas**. Escope ao **token/statement/linha lógica**, nunca a substring no
   corpus nem a exclusão por-linha-inteira.
4. **Corrija a FORMA, não a instância.** Lista hardcoded → glob derivado com
   sentinela anti-vazio; cópia de enum → derivação com exaustividade em compile-time;
   denylist de nomes → default-deny sobre o tipo. Corrigir só a instância garante
   reincidência (3 ocorrências já).
5. **Todo sweep gera seus próprios falsos-positivos.** Re-audite o **diff do sweep**
   antes de fechar.
6. **Auto-relato de subagente ≠ re-gate.** O fecho exige o re-gate rodado de 1ª mão,
   com a saída colada.
7. **Testar cópia ≠ testar produção.** Teste que valida reimplementação local é
   cobertura falsa — foi assim que o bug de produção do ledger (31ª, SQLSTATE 42P18)
   sobreviveu a 25 funções de teste.
8. **Silêncio = reprovação.** Em gate de paridade/cutover, ausência de divergência
   medida nunca é aprovação por omissão.
9. **Nenhuma ação destrutiva/remota em cloud sem aprovação humana explícita.**

---

## 7. Fecho de onda (obrigatório para declarar concluído)

1. Re-gate de 1ª mão verde (§4), saída colada.
2. Barreira de guardiões PASS 0 CRITICAL/HIGH (§5).
3. Registro honesto: blockquote da onda no `README.md` (após a 31ª) **e** parágrafo
   no `docs/plano-desenvolvimento-por-addon.md` §5 (bloco "Sweeps pós-G0"), com
   **lições novas** e **residuais não-bloqueantes** para a onda seguinte.
4. Limpeza de resíduo: sonda de mutação, backup, arquivo temporário, import não
   usado, código comentado, artefato local. `git status` limpo do que não é entrega.
5. **Commit + push ao `origin`** (`hojexnews/AdServer`, SSH) — padrão do projeto é
   commitar **e** empurrar ao fechar cada onda, após gates verdes. Mensagem no
   formato das anteriores: `fix(gates): Nª onda — <achados>; <guardiões>`.
