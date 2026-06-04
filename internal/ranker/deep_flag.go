// deep_flag.go — Fiacao minima para model_version deep no cliente Go do ranker.
//
// K1 / Fase 3 — ADR-0004 §A.
//
// PRINCIPIO (ADR-0004 §A / ADR-0003 §A):
//   O deep ranker entra como UMA NOVA VERSAO DE MODELO atras do MESMO ponto de
//   extensao internal/ranker / sidecar da Fase 2. Nao ha novo caminho na cascata;
//   nao ha nova interface; nao ha excecao de budget.
//
//   Esta fiacao fornece:
//     - IsDeepModelVersion: detecta model_version com prefixo "deep-" (identico
//       ao selector.py no sidecar Python).
//     - IsDeepEnabled: le a flag de ambiente DEEP_ENABLED (default false).
//     - ShouldUseDeep: combinacao de ambos — usado para logging/observabilidade
//       no handler de decisao (nao altera o caminho de scoring).
//
// INVARIANTES (nao negociaveis):
//   - DEEP_ENABLED=false por padrao. Golden tests da Fase 1/2 continuam verdes
//     com deep desligado: comportamento identico a cascata pura (fail-open).
//   - Budget TX-4 (5-8 ms p99) NAO ampliado. O timeout duro + fail-open em
//     ranker.go ja cobre o deep: se estourar, degrada para cascata pura.
//   - DA-3: o deep re-ordena DENTRO do estrato; a cascata e a autoridade final.
//   - Nenhuma dep Go nova adicionada (sem go.mod/go.sum modificados — K1 e
//     plumbing de flag/version puro).
//
// GATE K1 → K8:
//   - K1: DEEP_ENABLED=false; todo trafego usa GBDT ONNX-CPU.
//   - K8: DEEP_ENABLED=true; model_version "deep-*" aponta para Triton/GPU;
//     ativado somente apos prova de uplift A/B (promote_model.py).
//
// NENHUM codigo de scoring e alterado aqui. O MLRanker.Rank ja envia
// model_version para o sidecar via scoreRequest.ModelVersion. O sidecar
// (selector.py) decide o backend. O cliente Go nao precisa saber qual backend
// esta ativo — essa opacidade e o design correto (ADR-0004 §A: "substituicao
// de backend, nao reescrita de arquitetura").
package ranker

import "os"

// ---------------------------------------------------------------------------
// Constantes de prefixo
// ---------------------------------------------------------------------------

// DeepModelVersionPrefix e o prefixo que identifica uma versao de modelo deep.
// Convencao: model_version "deep-*" rota para o backend Triton/GPU no sidecar.
// Exemplos: "deep-1.0.0", "deep-two-tower-v1", "deep-k1-scaffold".
// Espelha a logica de selector.py (Python) e e a UNICA definicao do prefixo no
// codigo Go — sem condicional espalhada.
const DeepModelVersionPrefix = "deep-"

// ---------------------------------------------------------------------------
// IsDeepModelVersion
// ---------------------------------------------------------------------------

// IsDeepModelVersion retorna true se model_version tem o prefixo "deep-",
// indicando que o sidecar deve rotear para o backend Triton/GPU.
//
// Com DEEP_ENABLED=false (default), esta funcao e usada apenas para logging
// e observabilidade — o sidecar ignora o prefixo e usa o GBDT.
// Com DEEP_ENABLED=true (K8), o sidecar usa esta convencao para rotear.
func IsDeepModelVersion(modelVersion string) bool {
	return len(modelVersion) >= len(DeepModelVersionPrefix) &&
		modelVersion[:len(DeepModelVersionPrefix)] == DeepModelVersionPrefix
}

// ---------------------------------------------------------------------------
// IsDeepEnabled
// ---------------------------------------------------------------------------

// IsDeepEnabled le a variavel de ambiente DEEP_ENABLED.
//
// Retorna true somente se DEEP_ENABLED="true" (case-insensitive: "true" apenas).
// DEFAULT: false (DEEP_ENABLED nao definida ou qualquer outro valor).
//
// O valor e lido a cada chamada (nao cacheado) para permitir reconfiguracao
// dinamica em testes sem reiniciar o processo.
func IsDeepEnabled() bool {
	return os.Getenv("DEEP_ENABLED") == "true"
}

// ---------------------------------------------------------------------------
// ShouldUseDeep
// ---------------------------------------------------------------------------

// ShouldUseDeep retorna true se o deep ranker esta ativo para este request.
//
// Condicoes (AND):
//   1. DEEP_ENABLED=true
//   2. model_version tem prefixo "deep-"
//
// Uso: o decision handler pode logar ShouldUseDeep para observabilidade
// (ex.: distinguir decisoes GBDT de decisoes deep no Decision proto).
// NAO altera o caminho de scoring — o MLRanker.Rank ja envia model_version
// para o sidecar que decide o backend.
//
// Com DEEP_ENABLED=false (default), sempre retorna false — identico a
// cascata pura, golden tests verdes.
func ShouldUseDeep(modelVersion string) bool {
	return IsDeepEnabled() && IsDeepModelVersion(modelVersion)
}
