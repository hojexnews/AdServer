# GitOps — Argo CD (§2.7)

Argo CD é a fonte de implantação: o cluster converge para o que está no git.
Padrão **app-of-apps** — aplica-se **um** manifesto e o resto segue.

## Estrutura

```
gitops/
├── bootstrap/
│   ├── project.yaml        # AppProject "platform": limita repos/destinos/recursos
│   └── app-of-apps.yaml    # Application raiz → aponta p/ apps/
└── apps/                   # um Application por componente (ordenado por prefixo)
    ├── 00-namespaces.yaml
    ├── 10-network-policies.yaml   # Cilium deny-all + allow-dns
    └── 20-kyverno-policies.yaml
```

## Bootstrap (uma vez, após instalar o Argo CD)

```bash
kubectl apply -f platform/gitops/bootstrap/project.yaml
kubectl apply -f platform/gitops/bootstrap/app-of-apps.yaml
# A partir daqui, Argo CD sincroniza apps/ automaticamente (prune + selfHeal).
```

> **Substituir** `repoURL` (`https://github.com/hojex/adserver.git`) pela URL real
> em todos os manifestos antes do apply.

## A crescer (Fase 1)

- `30-observability.yaml` (OTel Collector + VM/Loki/Tempo/Grafana via Helm).
- `40-openbao.yaml` (OpenBao + políticas de [`../secrets/openbao/`](../secrets/openbao/)).
- Apps de aplicação (delivery, collectors, BFF) com waves de sync ordenadas.
