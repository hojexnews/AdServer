# make/copilot.mk — Targets de teste para services/copilot/ (J5/Fase 2).
# Dono: plataforma / copilot engineer.
# Incluido automaticamente pelo root Makefile via -include make/*.mk.
#
# Localmente: usa ml/.venv (ja tem as deps do copilot instaladas).
# No CI: usa setup-python + pip install -e services/copilot[dev]
#        (ver .github/workflows/ml.yml, job copilot-test).
#
# ALVO:
#   copilot-test  — pytest de services/copilot/tests/ (126 testes, J5)
#                   Inclui: gateway, model-router, schemas, security (HITL/auth).
#                   Gate obrigatorio antes de merge em qualquer PR que toque
#                   services/copilot/.

COPILOT_PYTHON := ml/.venv/bin/python
COPILOT_DIR    := services/copilot

.PHONY: copilot-test

## copilot-test: pytest de services/copilot/tests/ (gate J5 — 126 testes)
copilot-test:
	@echo "== copilot-test (J5 — gateway + model-router + schemas + security) =="
	PYTHONPATH=. $(COPILOT_PYTHON) -m pytest $(COPILOT_DIR)/tests/ -v
	@echo "copilot-test: OK"
