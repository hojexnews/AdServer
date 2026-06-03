# Time de Desenvolvimento — AdServer (Hojex News)

Subagentes especializados (project-local) que cobrem todas as camadas e preocupações
cross-cutting do AdServer. Cada persona é ancorada nos documentos normativos
([documentação técnica](../../docs/documentacao-tecnica.md) `DA-1…DA-12`/`CA-1…CA-9`,
[stack](../../docs/stack-tecnologico.md) `TX-1…TX-6` + roadmap) e na regra de ouro do
projeto: **começar enxuto e correto; tecnologia pesada só sob medição**.

## Como invocar
Use a ferramenta **Agent** com `subagent_type: <name>`. A maioria é convocada
automaticamente pela `description` quando o trabalho casa. Os três **read-only**
(`security-reviewer`, `privacy-compliance-auditor`) e o `parity-golden-test-guardian`
devem ser chamados **antes de mergear/cutover**. Orquestração e sequenciamento ficam
com o `tech-lead-architect`.

## Roster

| Persona | Papel | Camada / decisões | Fase | Modelo | Modo |
|---|---|---|---|---|---|
| [tech-lead-architect](tech-lead-architect.md) | Arquiteto-chefe: sequencia fases, abre ADRs, faz o gating por medição | Transversal · regra de ouro · §6 | 0→3 | opus | escreve |
| [decision-engine-engineer](decision-engine-engineer.md) | Motor de decisão Go (hot path) | §2.1 · DA-2/3/4/5/6/8/9 · TX-4 | 1 | sonnet | escreve |
| [schema-contracts-steward](schema-contracts-steward.md) | Contrato de eventos único (Protobuf/Buf) | proto/+contracts/ · TX-1 | 0→3 | sonnet | escreve |
| [data-platform-engineer](data-platform-engineer.md) | Redpanda · ClickHouse · Iceberg | §2.2 · DA-7 · TX-6 | 1 | sonnet | escreve |
| [money-ledger-guardian](money-ledger-guardian.md) | Tipo Money, Asset Registry, ledger, billing | §2.6 · TX-2 · DA-10 · CA-7 | 1→3 | sonnet | audita/escreve |
| [ml-optimization-engineer](ml-optimization-engineer.md) | pCTR/pCVR, serving, bandits, pacing, MLflow | §2.3 · DA-4 · TX-4 | 2 | sonnet | escreve |
| [copilot-llm-engineer](copilot-llm-engineer.md) | Copiloto Claude · LangGraph+HITL · RAG | §2.4 · TX-3/5 | 2 | sonnet | escreve |
| [frontend-bff-engineer](frontend-bff-engineer.md) | Console Next.js + BFF (fronteira de ACL) | §2.5 · CA-1/4/6/7 · TX-2/3 | 1→2 | sonnet | escreve |
| [payments-crypto-engineer](payments-crypto-engineer.md) | Fiat/cripto, ChainConnector, AEV/BND | §2.6 · §3 | 3 | sonnet | escreve |
| [platform-infra-engineer](platform-infra-engineer.md) | DevOps/SRE: EKS/Tofu/ArgoCD/Cilium/OTel/OpenBao | platform/ · §2.7 · TX-5 | 0→3 | sonnet | escreve* |
| [privacy-compliance-auditor](privacy-compliance-auditor.md) | Privacy by Design como gate | TX-5 · DA-11 · CA-8 · EU AI Act | 0→3 | opus | **read-only** |
| [security-reviewer](security-reviewer.md) | Isolamento por tenant, ACL, prompt-injection | TX-3 · CA-1 | 0→3 | opus | **read-only** |
| [parity-golden-test-guardian](parity-golden-test-guardian.md) | Golden tests, shadow, dual-run, CA-1…CA-9 | §5 · CA-1…CA-9 | 1→3 | sonnet | escreve |

\* `platform-infra-engineer` escreve código de infra, mas **não aplica em cloud** nem executa ações destrutivas sem aprovação humana.

## Mapa por fase do roadmap

- **Fase 0 (Fundações, ✅ camada de contratos):** schema-contracts-steward, money-ledger-guardian, platform-infra-engineer, privacy-compliance-auditor, security-reviewer, tech-lead-architect.
- **Fase 1 (MVP de paridade):** decision-engine-engineer, data-platform-engineer, money-ledger-guardian, frontend-bff-engineer, parity-golden-test-guardian (+ auditores).
- **Fase 2 (ML + copiloto):** ml-optimization-engineer, copilot-llm-engineer, frontend-bff-engineer (+ auditores).
- **Fase 3 (IA avançada + cripto):** ml-optimization-engineer, payments-crypto-engineer, platform-infra-engineer (células PCI/AML) (+ auditores).

## Gates antes de mergear / cutover
1. `make verify` verde (buf TX-1 + no-float TX-2) — owner: platform-infra-engineer.
2. `security-reviewer` e `privacy-compliance-auditor` sem CRITICAL/HIGH abertos.
3. `money-ledger-guardian` sem vazamento de float em código que toca dinheiro.
4. `parity-golden-test-guardian`: golden/shadow/dual-run dentro da tolerância antes de cutover.

## Princípios invioláveis (resumo, todos compartilham)
- `float` proibido para dinheiro (TX-2); multi-moeda sem conversão automática (DA-10).
- Compatibilidade **BACKWARD** no schema de eventos (TX-1).
- Sem PII / sem IP bruto nos eventos (TX-5/DA-11).
- **Cascata é a autoridade final** (DA-3); a IA só re-rankeia dentro do estrato com fail-open (TX-4).
- Faturável reconcilia contra o lakehouse Iceberg, nunca contra streaming; agregação batch horária (DA-7), UI nunca soma "ao vivo" com "≤1h".
