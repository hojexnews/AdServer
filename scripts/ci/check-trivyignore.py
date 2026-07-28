#!/usr/bin/env python3
"""
scripts/ci/check-trivyignore.py

Gate executável para platform/observability/.trivyignore (válvula de
escape auditável do Trivy, decisão da 30ª/31ª onda — ver o cabeçalho do
próprio arquivo e o comentário TRIVYIGNORES em
.github/workflows/supply-chain.yml).

Achado que este gate fecha (32ª onda, achado `trivyignore-regras-so-em-
prosa`): o cabeçalho do .trivyignore enuncia 4 regras — expiração
`exp:YYYY-MM-DD` OBRIGATÓRIA sendo a principal — mas nenhum script ou
job as impunha. Uma promessa em prosa sem gate executável é exatamente a
forma de defeito que esta série de ondas combate; pior, uma entrada
EXPIRADA continuava suprimindo o CVE indefinidamente, porque nada lia a
data. Este script é o gate que faltava.

Regras impostas (DEFAULT-DENY: toda linha que não seja comentário ou
branco é tratada como uma entrada de exceção e tem de satisfazer TODAS):

  1. Formato: <ID> exp:YYYY-MM-DD (ID = CVE-XXXX-YYYYY ou GHSA-xxxx-
     xxxx-xxxx — os dois formatos nativamente aceitos pelo `trivyignores`
     do Trivy). Comentário inline após '#' é permitido e ignorado.
  2. Expiração: a data em `exp:` não pode já ter passado na data de
     EXECUÇÃO do gate (lida do relógio do sistema — `date.today()`, sem
     data fixa no código). `exp_date < hoje` reprova.
  3. Justificativa: a linha física IMEDIATAMENTE ACIMA da entrada tem de
     ser um comentário (`#...`) não-vazio — a regra 1 do cabeçalho do
     arquivo ("a justificativa em um comentário na linha imediatamente
     acima").
  4. Uma linha por CVE/GHSA (regra 1 do cabeçalho): ID duplicado entre
     entradas ativas reprova.

Uso:
    python3 scripts/ci/check-trivyignore.py <caminho/.trivyignore>
Saída: "OK" + exit 0 se o arquivo não tem nenhuma entrada ativa, ou se
todas as entradas ativas passam nas 4 regras. Lista de violações em
stderr + exit 1 caso contrário.
"""
import datetime
import pathlib
import re
import sys

ID_PATTERN = re.compile(
    r"^(CVE-\d{4}-\d{4,7}|GHSA-[0-9a-z]{4}-[0-9a-z]{4}-[0-9a-z]{4})$",
    re.IGNORECASE,
)
EXP_TOKEN_PATTERN = re.compile(r"^exp:(\d{4}-\d{2}-\d{2})$")


def strip_inline_comment(line: str) -> str:
    """Remove um comentário inline ('# ...') preservando o código antes dele."""
    idx = line.find("#")
    if idx >= 0:
        return line[:idx]
    return line


def check(lines: list[str], today: datetime.date) -> tuple[list[str], int]:
    """Retorna (errors, entries_checked)."""
    errors: list[str] = []
    seen_ids: dict[str, int] = {}
    entries_checked = 0

    for idx, raw in enumerate(lines):
        stripped = raw.strip()

        if stripped == "":
            continue
        if stripped.startswith("#"):
            continue

        # Linha ativa (não-branco, não-comentário) = entrada de exceção.
        lineno = idx + 1
        entries_checked += 1

        code_part = strip_inline_comment(raw).strip()
        if code_part == "":
            # Linha continha só o que sobrou de um '#' no meio de espaço —
            # não deveria acontecer (já teria caído no startswith('#')
            # acima), mas se acontecer é uma entrada vazia == inválida.
            errors.append(f"linha {lineno}: entrada vazia/ilegível: {raw!r}")
            continue

        tokens = code_part.split()

        # Regra 1a: primeiro token tem de ser um CVE/GHSA reconhecido.
        vuln_id = tokens[0]
        if not ID_PATTERN.match(vuln_id):
            errors.append(
                f"linha {lineno}: ID de vulnerabilidade não reconhecido "
                f"{vuln_id!r} — esperado CVE-XXXX-YYYYY ou GHSA-xxxx-xxxx-xxxx"
            )
            continue

        # Regra 1b: exp:YYYY-MM-DD obrigatório em algum token seguinte.
        exp_token = next(
            (t for t in tokens[1:] if t.lower().startswith("exp:")), None
        )
        if exp_token is None:
            errors.append(
                f"linha {lineno}: {vuln_id} sem sufixo `exp:YYYY-MM-DD` "
                f"obrigatório (regra 2 do cabeçalho do .trivyignore) — "
                f"linha: {code_part!r}"
            )
            continue

        m = EXP_TOKEN_PATTERN.match(exp_token)
        if not m:
            errors.append(
                f"linha {lineno}: {vuln_id} tem `exp:` malformado "
                f"({exp_token!r}) — esperado exp:YYYY-MM-DD"
            )
            continue

        exp_str = m.group(1)
        try:
            exp_date = datetime.date.fromisoformat(exp_str)
        except ValueError:
            errors.append(
                f"linha {lineno}: {vuln_id} tem data inválida em `exp:` "
                f"({exp_str!r})"
            )
            continue

        # Regra 2: expiração.
        if exp_date < today:
            errors.append(
                f"linha {lineno}: {vuln_id} EXPIROU em {exp_date.isoformat()} "
                f"(hoje: {today.isoformat()}) — renove com um novo `exp:` e "
                f"justificativa revisada, ou remova a linha; até lá o CVE "
                f"deve voltar a bloquear (fail-closed é o comportamento "
                f"correto, regra 4 do cabeçalho)"
            )

        # Regra 3: justificativa na linha física imediatamente acima.
        prev_line = lines[idx - 1].strip() if idx > 0 else ""
        if not prev_line.startswith("#") or prev_line.lstrip("#").strip() == "":
            errors.append(
                f"linha {lineno}: {vuln_id} sem justificativa comentada na "
                f"linha imediatamente acima (regra 1 do cabeçalho do "
                f".trivyignore) — a linha {lineno - 1} tem de ser um "
                f"comentário `# ...` não-vazio explicando por que o CVE não "
                f"se aplica ou por que o risco é aceito"
            )

        # Regra 4: uma linha por CVE/GHSA — ID duplicado entre entradas ativas.
        vuln_id_norm = vuln_id.upper()
        if vuln_id_norm in seen_ids:
            errors.append(
                f"linha {lineno}: {vuln_id} duplicado — já declarado na "
                f"linha {seen_ids[vuln_id_norm]} (regra 1 do cabeçalho: "
                f"UMA linha por CVE)"
            )
        else:
            seen_ids[vuln_id_norm] = lineno

    return errors, entries_checked


def main() -> int:
    if len(sys.argv) != 2:
        print(f"uso: {sys.argv[0]} <.trivyignore>", file=sys.stderr)
        return 2

    path = pathlib.Path(sys.argv[1])
    if not path.is_file():
        print(f"ERRO: arquivo não encontrado: {path}", file=sys.stderr)
        return 1

    text = path.read_text(encoding="utf-8")
    lines = text.split("\n")
    today = datetime.date.today()

    errors, entries_checked = check(lines, today)

    if errors:
        print("== check-trivyignore: FALHOU ==", file=sys.stderr)
        for e in errors:
            print(f"ERRO: {e}", file=sys.stderr)
        return 1

    if entries_checked == 0:
        print(f"OK — {path}: nenhuma entrada ativa (fail-closed puro, nada a validar).")
    else:
        print(
            f"OK — {path}: {entries_checked} entrada(s) ativa(s), todas com "
            f"exp:YYYY-MM-DD válido e não-expirado (hoje: {today.isoformat()}), "
            f"justificativa comentada e sem duplicata."
        )
    return 0


if __name__ == "__main__":
    sys.exit(main())
