#!/usr/bin/env python3
"""
scripts/ci/data-integration-test.py

Executa data/clickhouse/tests/tenant_isolation_test.sql contra um ClickHouse
REAL (achado #10, 31a onda).

PROBLEMA QUE ISTO FECHA (orfandade):
  O arquivo .sql existia e documentava, em comentarios, o resultado esperado
  de cada query ("-- EXPECT: 0 rows"), mas NENHUM alvo make nem workflow o
  executava. Alem disso, mesmo se alguem rodasse
  `clickhouse-client < tenant_isolation_test.sql` manualmente, isso NAO
  provaria isolamento: os SELECT count() do arquivo apenas IMPRIMEM um
  numero — nao afirmam nada por si sos, e o arquivo roda inteiro como o
  usuario default/admin (sem trocar de usuario), entao as row-policies
  `TO tenant_role` nem chegam a ser exercitadas. Um "make target que so
  imprime o arquivo e confere ausencia de erro SQL" seria um gate OCO:
  passaria mesmo com isolamento cross-tenant quebrado.

O QUE ESTE RUNNER FAZ:
  Le o .sql linha a linha e executa statement-a-statement via
  `clickhouse-client`, honrando diretivas embutidas como comentarios SQL
  adicionais (documentadas no cabecalho do proprio tenant_isolation_test.sql):

    -- @user: <username-literal-do-CREATE-USER-do-proprio-arquivo> | admin
        Troca o usuario de impersonacao (--user/--password do
        clickhouse-client) para os statements seguintes, ate a proxima
        diretiva @user. 'admin' = sem --user/--password (usuario
        default/admin do cliente). As senhas sao extraidas do PROPRIO
        arquivo (CREATE USER ... IDENTIFIED BY '...'), nunca duplicadas
        aqui — uma unica fonte de verdade.

    -- @expect: eq0 | ge1 | eq:'<valor>' | eq:NULL
        Afirma o resultado do PROXIMO statement (deve ser um SELECT que
        retorna uma unica linha/coluna). Falha alto se o valor retornado
        nao bater com o esperado.

  FALHA ALTO (exit != 0), sem modo skip/stub silencioso, quando:
    - clickhouse-client nao esta no PATH;
    - nao ha conectividade com um servidor ClickHouse real;
    - qualquer statement de SETUP/TEARDOWN falha;
    - qualquer asserção @expect nao bate com o valor retornado;
    - o parsing nao produz NENHUM statement (sentinela anti-vazio —
      indica bug no parser, nao ausencia real de statements);
    - o arquivo .sql nao contem NENHUMA diretiva '-- @expect:' (sentinela
      anti-vazio de asserção, BLOCO D / 32a onda: sem isso, um stub de
      clickhouse-client que responde com sucesso a QUALQUER query faz o
      runner "passar" tendo executado statements mas afirmado ZERO coisas —
      o antigo 'statements_run == 0' nao pega esse caso, porque
      statements_run conta TODO statement, com ou sem @expect associado);
    - o numero de asserções EFETIVAMENTE avaliadas (uma comparação real
      contra o stdout retornado, nao apenas a existência da diretiva) e zero,
      OU diverge do numero de diretivas '-- @expect:' declaradas no proprio
      arquivo (indica @expect orfao — nunca consumido por um statement
      seguinte — ou perdido por bug de parsing).

Uso:
  CH_HOST=localhost CH_PORT=9000 python3 scripts/ci/data-integration-test.py
  (ou via `make data-integration-test`, que e o alvo canonico.)
"""
import os
import pathlib
import re
import shutil
import subprocess
import sys

TEST_SQL = pathlib.Path("data/clickhouse/tests/tenant_isolation_test.sql")

CH_HOST = os.environ.get("CH_HOST", "localhost")
CH_PORT = os.environ.get("CH_PORT", "9000")
CH_ADMIN_USER = os.environ.get("CH_ADMIN_USER")  # opcional; None = default do cliente
CH_ADMIN_PASSWORD = os.environ.get("CH_ADMIN_PASSWORD")  # opcional

USER_DIRECTIVE_RE = re.compile(r"^--\s*@user:\s*(\S+)\s*$")
EXPECT_DIRECTIVE_RE = re.compile(r"^--\s*@expect:\s*(eq0|ge1|eq:.+?)\s*$")
CREATE_USER_RE = re.compile(
    r"CREATE\s+USER\s+IF\s+NOT\s+EXISTS\s+`?([\w.\-]+)`?\s+IDENTIFIED\s+BY\s+'([^']+)'",
    re.I,
)


class _FakeResult:
    """Substitui subprocess.CompletedProcess quando a chamada nem chega a rodar
    (ex.: clickhouse-client removido do PATH em tempo de execucao)."""

    def __init__(self, returncode, stdout="", stderr=""):
        self.returncode = returncode
        self.stdout = stdout
        self.stderr = stderr


def fail(msg: str) -> None:
    print(f"ERRO: {msg}", file=sys.stderr)
    sys.exit(1)


def clickhouse_client_available() -> bool:
    return shutil.which("clickhouse-client") is not None


def base_args(username, password):
    args = ["clickhouse-client", "--host", CH_HOST, "--port", str(CH_PORT)]
    if username:
        args += ["--user", username]
    if password:
        args += ["--password", password]
    return args


def check_connectivity() -> None:
    args = base_args(CH_ADMIN_USER, CH_ADMIN_PASSWORD) + ["--query", "SELECT 1"]
    try:
        result = subprocess.run(args, capture_output=True, text=True, timeout=10)
    except FileNotFoundError:
        fail("clickhouse-client nao encontrado no PATH (checagem tardia).")
        return
    except subprocess.TimeoutExpired:
        fail(f"timeout conectando ao ClickHouse em {CH_HOST}:{CH_PORT}.")
        return
    if result.returncode != 0 or result.stdout.strip() != "1":
        fail(
            f"nao foi possivel conectar a um ClickHouse real em "
            f"{CH_HOST}:{CH_PORT}. data-integration-test NAO tem modo "
            f"stub/skip — suba um ClickHouse real (ex.: clickhouse-server "
            f"local, ou service container de CI) e rode de novo, ou exporte "
            f"CH_HOST/CH_PORT apontando para uma instancia acessivel. "
            f"stderr: {result.stderr.strip()!r}"
        )


def extract_user_passwords(sql_text: str):
    return dict(CREATE_USER_RE.findall(sql_text))


def run_statement(stmt: str, username, password):
    args = base_args(username, password) + ["--query", stmt]
    try:
        return subprocess.run(args, capture_output=True, text=True, timeout=30)
    except FileNotFoundError:
        return _FakeResult(1, stderr="clickhouse-client desapareceu do PATH em tempo de execucao.")
    except subprocess.TimeoutExpired:
        return _FakeResult(1, stderr=f"timeout (30s) executando statement: {stmt[:120]!r}")


def strip_trailing_comment(raw_line: str) -> str:
    idx = raw_line.find("--")
    return raw_line if idx == -1 else raw_line[:idx]


def main() -> int:
    if not TEST_SQL.exists():
        fail(f"{TEST_SQL} nao encontrado.")
        return 1

    if not clickhouse_client_available():
        fail(
            "clickhouse-client nao encontrado no PATH. data-integration-test "
            "requer um cliente ClickHouse real — este alvo NUNCA pula em "
            "silencio quando a ferramenta esta ausente."
        )
        return 1

    check_connectivity()

    sql_text = TEST_SQL.read_text()
    passwords = extract_user_passwords(sql_text)
    if not passwords:
        fail(
            f"nenhum 'CREATE USER IF NOT EXISTS ... IDENTIFIED BY' encontrado "
            f"em {TEST_SQL} — extract_user_passwords quebrado ou arquivo "
            f"reescrito sem os usuarios de fixture (sentinela anti-vazio)."
        )
        return 1

    current_user = None
    current_password = None
    pending_expect = None  # None | ("eq0",) | ("ge1",) | ("eq", valor_str)
    buffer_lines = []
    failures = []
    statements_run = 0
    # BLOCO D (32a onda) — sentinela de nao-vacuidade por asserção:
    #   expected_assertion_count: quantas diretivas '-- @expect: ...' o
    #     PROPRIO arquivo declara (derivado do arquivo, nao hardcoded).
    #   assertions_evaluated: quantas dessas diretivas resultaram em uma
    #     comparação REAL (stdout retornado vs. valor esperado), contada
    #     dentro do branch que faz a comparação — nao apenas "a diretiva
    #     existe" nem "o statement rodou". Um stub de clickhouse-client que
    #     responde com sucesso a qualquer coisa NAO basta para inflar este
    #     contador: a comparação tem de rodar de fato.
    expected_assertion_count = 0
    assertions_evaluated = 0

    for raw_line in sql_text.splitlines():
        stripped = raw_line.strip()

        m_user = USER_DIRECTIVE_RE.match(stripped)
        if m_user:
            alias = m_user.group(1)
            if alias == "admin":
                current_user, current_password = CH_ADMIN_USER, CH_ADMIN_PASSWORD
            elif alias in passwords:
                current_user, current_password = alias, passwords[alias]
            else:
                fail(
                    f"diretiva '-- @user: {alias}' nao corresponde a nenhum "
                    f"'CREATE USER IF NOT EXISTS `{alias}` IDENTIFIED BY ...' "
                    f"no proprio arquivo {TEST_SQL}."
                )
                return 1
            continue

        m_expect = EXPECT_DIRECTIVE_RE.match(stripped)
        if m_expect:
            expected_assertion_count += 1
            if pending_expect is not None:
                fail(
                    f"diretiva '-- @expect:' encontrada enquanto a asserção "
                    f"anterior ({pending_expect}) ainda estava pendente — "
                    f"ela nunca foi consumida por um statement seguinte "
                    f"(asserção orfã / perdida em {TEST_SQL})."
                )
                return 1
            spec = m_expect.group(1)
            if spec == "eq0":
                pending_expect = ("eq0",)
            elif spec == "ge1":
                pending_expect = ("ge1",)
            else:
                value = spec[len("eq:"):].strip()
                if value.startswith("'") and value.endswith("'") and len(value) >= 2:
                    value = value[1:-1]
                pending_expect = ("eq", value)
            continue

        if stripped.startswith("--") or stripped == "":
            # Comentario decorativo (nao-diretiva) ou linha em branco: nao
            # entra no buffer do statement (evita quebrar --query com uma
            # linha de comentario solta no meio de um statement multilinha).
            continue

        code_part = strip_trailing_comment(raw_line)
        buffer_lines.append(code_part)

        if code_part.rstrip().endswith(";"):
            stmt = "\n".join(buffer_lines).strip()
            buffer_lines = []
            statements_run += 1

            result = run_statement(stmt, current_user, current_password)
            who = current_user or "admin/default"

            if result.returncode != 0:
                failures.append(
                    f"statement falhou (user={who}): {stmt[:200]!r}\n"
                    f"    stderr: {result.stderr.strip()}"
                )
            elif pending_expect is not None:
                # Conta a asserção como EFETIVAMENTE avaliada aqui — dentro do
                # ramo que compara o stdout real contra o valor esperado, nao
                # no ponto em que a diretiva foi apenas lida do arquivo. Um
                # stub que retorna returncode==0 para tudo ainda cai aqui (o
                # bloco acima so filtra falhas de execução), mas a comparação
                # abaixo roda de verdade contra o valor retornado.
                assertions_evaluated += 1
                actual = result.stdout.strip()
                kind = pending_expect[0]
                ok = False
                if kind == "eq0":
                    ok = actual == "0"
                elif kind == "ge1":
                    try:
                        ok = int(actual) >= 1
                    except ValueError:
                        ok = False
                elif kind == "eq":
                    expected = pending_expect[1]
                    actual_norm = "" if actual in ("\\N", "NULL") else actual
                    expected_norm = "" if expected == "NULL" else expected
                    ok = actual_norm == expected_norm
                if not ok:
                    failures.append(
                        f"ASSERCAO FALHOU (user={who}, expect={pending_expect}): "
                        f"valor retornado={actual!r}\n    statement: {stmt[:200]!r}"
                    )

            pending_expect = None

    if buffer_lines and "".join(buffer_lines).strip():
        failures.append(
            f"statement inacabado (sem ';') no fim do arquivo: "
            f"{' '.join(buffer_lines).strip()[:120]!r}"
        )

    if pending_expect is not None:
        failures.append(
            f"diretiva '-- @expect: {pending_expect}' no fim do arquivo nunca "
            f"foi consumida por um statement (asserção orfã / perdida em "
            f"{TEST_SQL})."
        )

    if statements_run == 0:
        fail(
            f"nenhum statement executado a partir de {TEST_SQL} — parsing "
            f"quebrado (sentinela anti-vazio)."
        )
        return 1

    # BLOCO D (32a onda) — sentinela de nao-vacuidade por asserção. Fecha o
    # achado: "statements_run == 0" NAO pega o caso de um stub de
    # clickhouse-client que responde com sucesso a qualquer query — nesse
    # cenario statements_run > 0 (o INSERT/CREATE USER de fixture "roda"),
    # mas NENHUMA comparação real (@expect) e avaliada, e o runner imprimiria
    # PASS mesmo sem ter afirmado nada sobre isolamento cross-tenant.
    if expected_assertion_count == 0:
        fail(
            f"{TEST_SQL} nao contem NENHUMA diretiva '-- @expect: ...' — "
            f"parsing quebrado ou arquivo reescrito sem asserções (sentinela "
            f"anti-vazio de asserção). Um runner que so executa statements "
            f"sem afirmar nada NAO prova isolamento cross-tenant (TX-3)."
        )
        return 1

    if failures:
        print(
            f"data-integration-test: {len(failures)} falha(s) de "
            f"{statements_run} statement(s):",
            file=sys.stderr,
        )
        for f in failures:
            print(f"  - {f}", file=sys.stderr)
        return 1

    # Sentinelas de nao-vacuidade por asserção (BLOCO D, 32a onda) — so
    # alcançadas quando NENHUMA falha de execução/asserção foi reportada
    # acima (ou seja, o client — real ou stub — "disse sim" para tudo). E
    # exatamente esse o cenário em que um stub permissivo faria o runner
    # antigo (que so checava 'statements_run == 0') imprimir PASS sem ter
    # afirmado nada de fato sobre isolamento cross-tenant.
    if assertions_evaluated == 0:
        fail(
            f"{expected_assertion_count} diretiva(s) '-- @expect:' foram "
            f"declaradas em {TEST_SQL}, mas ZERO asserções foram "
            f"efetivamente avaliadas durante a execução (sentinela "
            f"anti-vazio de asserção). Um clickhouse-client (real ou stub) "
            f"que só executa statements sem que nenhuma comparação real "
            f"tenha rodado NAO prova nada sobre isolamento cross-tenant "
            f"(TX-3) — mesmo que o exit code de cada statement seja 0."
        )
        return 1

    if assertions_evaluated != expected_assertion_count:
        fail(
            f"{TEST_SQL} declara {expected_assertion_count} diretiva(s) "
            f"'-- @expect:', mas apenas {assertions_evaluated} foram "
            f"efetivamente avaliadas (comparação real contra o stdout "
            f"retornado). A diferença indica asserção perdida por bug de "
            f"parsing no runner. Sentinela anti-vazio de asserção "
            f"(BLOCO D, 32a onda)."
        )
        return 1

    print(
        f"data-integration-test: PASS ({statements_run} statements, "
        f"{assertions_evaluated} asserções @expect avaliadas, "
        f"isolamento cross-tenant TX-3 confirmado contra ClickHouse real)"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
