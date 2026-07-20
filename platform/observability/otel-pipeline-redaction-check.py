#!/usr/bin/env python3
"""
platform/observability/otel-pipeline-redaction-check.py

Gate estrutural TX-5 (redação de PII antes de qualquer export) para a
config do OTel Collector (platform/observability/otel-collector.yaml) —
chamado por `make platform-otel-validate`.

Motivação (achados `otel-gate-hardcoded-pipeline-names` e
`otel-metrics-pipeline-sem-redacao-e-sem-gate`, 30ª onda de auditoria
adversarial): a versão anterior deste gate era um `for pl in
"traces:..." "logs:..."` — uma ALLOWLIST HARDCODED de dois nomes de
pipeline. A spec do OTel permite pipelines nomeados `<tipo>/<id>` (ex.:
`traces/raw`) como pipelines de primeira classe com processors/exporters
próprios; um pipeline desses, criado sem passar pela redação (ex.:
`processors: [memory_limiter, batch]`, sem transform/redact-pii nem
allowlist), nunca era inspecionado — o gate reportava "OK" com spans
crus (IP, e-mail) saindo direto para Tempo. O pipeline "metrics" nunca
era sequer mencionado no loop: zero linhas de código o visitavam.

Este gate é DEFAULT-DENY: enumera TODOS os pipelines realmente definidos
em `service.pipelines` (o que estiver na config, seja qual for o nome) —
nunca uma lista literal mantida à mão. Para cada pipeline, deriva o TIPO
de sinal a partir do prefixo antes de "/" (ou do nome inteiro, se não
houver "/"). A OTel spec só define três tipos de sinal — "traces",
"metrics", "logs" — e só há redação definida para esses três; qualquer
outro tipo (typo, sinal novo tipo "profiles" ainda não coberto, etc.)
REPROVA por construção, sem exceção nem allowlist de nomes.

Para cada pipeline de tipo conhecido, exige em `.processors`:
  1. `transform/redact-pii` — CABEADO na lista .processors *e* com um
     bloco de statements não-vazio para o TIPO DE SINAL daquele pipeline
     especificamente (ver EFEITO, não só MEMBRESIA, abaixo).
  2. um processor cujo nome comece com `redaction/allowlist-<tipo>` — a
     allowlist fail-closed final daquele tipo de sinal (permite reuso do
     mesmo processor por vários pipelines do mesmo tipo, ou um allowlist
     mais específico tipo `redaction/allowlist-metrics-experimental`,
     desde que prefixado corretamente).

MEMBRESIA, não mera presença no arquivo: um processor definido em
`processors:` mas nunca listado em `service.pipelines.<p>.processors`
não protege ninguém (fail-open silencioso) — por isso este gate lê a
lista `.processors` de CADA pipeline, não greps genéricos no arquivo.

EFEITO, não só MEMBRESIA na lista de processors (achado
`otel-transform-membresia-sem-efeito`, 31ª onda de remediação): o
processor `transform/redact-pii` é um ÚNICO objeto YAML com três blocos
de statements INDEPENDENTES — `trace_statements`, `metric_statements`,
`log_statements` (nomenclatura da própria spec do processor `transform`
do OTel: singular-do-tipo + "_statements"). Um pipeline de metrics pode
"conter" `transform/redact-pii` em `.processors` (passando a checagem de
membresia) e mesmo assim não ter NENHUMA regra de redação de fato
aplicada a datapoints, se `metric_statements` estiver ausente ou vazio —
a mesma tautologia presença-não-é-efeito que a 25ª onda corrigiu no gate
`otel-collector-has-redaction`. Este gate portanto também abre a
definição de `transform/redact-pii` em `processors:` (não apenas conta
a string na lista) e exige que o bloco `<singular-do-tipo>_statements`
correspondente ao tipo de sinal do pipeline exista, seja uma lista
não-vazia, e que pelo menos uma entrada tenha `.statements` não-vazio —
senão o processor está listado, mas oco para aquele sinal.

Uso:
    python3 platform/observability/otel-pipeline-redaction-check.py <config.yaml>
Saída: "OK" (lista os pipelines cobertos) + exit 0 se todos os pipelines
passam; lista de violações em stderr + exit 1 caso contrário. Não
substitui `otelcol validate --config` (validação semântica via Docker,
gate separado) nem a checagem de `allow_all_keys` (feita à parte no
Makefile sobre o arquivo inteiro — já é default-deny por natureza, pois
conta TODAS as ocorrências, não uma lista de processors por nome).
"""
import pathlib
import sys

try:
    import yaml
except ImportError:
    print(
        "ERRO: PyYAML ausente (python3 -c 'import yaml' falhou). "
        "Necessário para ler o pipelines do OTel Collector.",
        file=sys.stderr,
    )
    sys.exit(1)

# Único tipos de sinal com pipeline de redação TX-5 definido nesta config.
# NÃO adicionar um nome aqui sem adicionar também o par transform/allowlist
# correspondente em otel-collector.yaml — isto é o contrato, não um detalhe.
KNOWN_SIGNAL_TYPES = ("traces", "metrics", "logs")


def pipeline_signal_type(pipeline_name: str) -> str:
    """'traces' -> 'traces'; 'traces/raw' -> 'traces'; 'foo' -> 'foo'."""
    return pipeline_name.split("/", 1)[0]


def statements_key_for_signal(sig_type: str) -> str:
    """Nomenclatura da própria spec do processor `transform` do OTel:
    'traces' -> 'trace_statements', 'metrics' -> 'metric_statements',
    'logs' -> 'log_statements' — sempre singular-do-tipo + '_statements'.
    Fonte única: os três tipos em KNOWN_SIGNAL_TYPES sempre terminam em
    's' na spec do OTel (não é um mapa mantido à mão)."""
    return f"{sig_type.rstrip('s')}_statements"


def transform_processor_has_effect_for(
    processors_def: dict, sig_type: str
) -> tuple[bool, str]:
    """Verifica EFEITO (não só membresia): o processor `transform/redact-pii`,
    tal como DEFINIDO em `processors:`, precisa ter um bloco de statements
    não-vazio para o tipo de sinal `sig_type`, com pelo menos uma entrada
    cujo `.statements` também seja uma lista não-vazia. Retorna
    (tem_efeito, motivo_se_nao)."""
    transform_def = processors_def.get("transform/redact-pii")
    if not isinstance(transform_def, dict):
        return False, (
            "transform/redact-pii está na lista .processors do pipeline mas "
            "NÃO está definido (ou não é um mapping) em processors: — "
            "membresia sem definição real"
        )

    key = statements_key_for_signal(sig_type)
    blocks = transform_def.get(key)
    if not isinstance(blocks, list) or not blocks:
        return False, (
            f"transform/redact-pii não tem `{key}` (ou está vazio) — "
            f"o processor está cabeado no pipeline de {sig_type!r} mas SEM "
            f"nenhuma regra de redação aplicada a esse tipo de sinal "
            f"(presença na lista .processors sem efeito real)"
        )

    for block in blocks:
        if isinstance(block, dict):
            stmts = block.get("statements")
            if isinstance(stmts, list) and len(stmts) > 0:
                return True, ""

    return False, (
        f"transform/redact-pii tem `{key}` mas nenhuma entrada tem "
        f".statements não-vazio — bloco de contexto presente e oco"
    )


def check(config_path: pathlib.Path) -> tuple[list[str], list[str]]:
    """Retorna (errors, covered). `covered` lista os nomes de pipeline
    inspecionados (independente de terem passado ou não) — usado só na
    mensagem de sucesso, para deixar explícito o que o gate de fato viu."""
    errors: list[str] = []
    covered: list[str] = []

    try:
        doc = yaml.safe_load(config_path.read_text())
    except yaml.YAMLError as e:
        return [f"YAML inválido em {config_path}: {e}"], covered

    if not isinstance(doc, dict):
        return [f"{config_path} não é um mapping YAML no nível raiz"], covered

    pipelines = ((doc.get("service") or {}).get("pipelines")) or {}
    if not isinstance(pipelines, dict) or not pipelines:
        return (
            [f"service.pipelines ausente ou vazio em {config_path} — nada para validar"],
            covered,
        )

    processors_def = doc.get("processors") or {}
    if not isinstance(processors_def, dict):
        processors_def = {}

    for name, cfg in pipelines.items():
        covered.append(name)
        sig_type = pipeline_signal_type(name)
        processors = (cfg or {}).get("processors") or []
        if not isinstance(processors, list):
            errors.append(f"pipeline {name!r}: .processors não é uma lista")
            continue

        if sig_type not in KNOWN_SIGNAL_TYPES:
            errors.append(
                f"pipeline {name!r}: tipo de sinal {sig_type!r} não tem nenhuma "
                f"redação TX-5 definida nesta config (tipos cobertos: "
                f"{', '.join(KNOWN_SIGNAL_TYPES)}) — REJEITADO por default-deny "
                f"(default-deny: todo pipeline precisa de par transform/allowlist "
                f"explícito, não há allowlist de nomes de pipeline)"
            )
            continue

        if "transform/redact-pii" not in processors:
            errors.append(
                f"ERRO TX-5: pipeline {name!r} sem transform/redact-pii na lista "
                f".processors (não apenas definido em processors:, mas CABEADO "
                f"na pipeline)"
            )
        else:
            has_effect, reason = transform_processor_has_effect_for(processors_def, sig_type)
            if not has_effect:
                errors.append(f"ERRO TX-5: pipeline {name!r}: {reason}")

        allowlist_prefix = f"redaction/allowlist-{sig_type}"
        has_allowlist = any(
            p == allowlist_prefix or p.startswith(allowlist_prefix + "-")
            for p in processors
            if isinstance(p, str)
        )
        if not has_allowlist:
            errors.append(
                f"ERRO TX-5: pipeline {name!r} sem processor {allowlist_prefix}* "
                f"na lista .processors"
            )

    return errors, covered


def main() -> int:
    if len(sys.argv) != 2:
        print(f"uso: {sys.argv[0]} <otel-collector.yaml>", file=sys.stderr)
        return 2

    config_path = pathlib.Path(sys.argv[1])
    if not config_path.is_file():
        print(f"ERRO: arquivo não encontrado: {config_path}", file=sys.stderr)
        return 1

    errors, covered = check(config_path)

    if errors:
        print("== otel-pipeline-redaction-check: FALHOU ==", file=sys.stderr)
        for e in errors:
            print(f"ERRO: {e}", file=sys.stderr)
        return 1

    print(
        "OK — todos os pipelines de service.pipelines ("
        + ", ".join(sorted(covered))
        + f") têm transform/redact-pii + redaction/allowlist-<tipo> cabeados "
        f"em .processors ({config_path})."
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
