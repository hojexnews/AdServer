# make/ml.mk — ML pipeline targets (J3/J4, ADR-0003 §G)
#
# Dono: ml-optimization-engineer (J3 / J4 / J6)
# NAO criar make/ml-batch.mk (reservado ao incremento paralelo J6).
# NAO tocar em ml/pacing nem ml/fraud (outro incremento e dono).
#
# Incluido automaticamente pelo root Makefile via `-include make/*.mk`.
#
# PYTHON: usa o venv em ml/.venv/. Nao instala dependencias automaticamente.
#   Para adicionar dependencias: ml/.venv/bin/pip install <pkg>
#   e registrar no relatorio de entrega (nao ha requirements.txt gerenciado pelo make).
#
# ALVOS (J3):
#   ml-ope-test         — pytest dos estimadores OPE (IPS/SNIPS/DR)
#   ml-features-test    — pytest da paridade Python/Go de featurizacao
#   ml-training-test    — pytest do pipeline de treino (dados sinteticos)
#   ml-calibration-test — pytest da calibracao isotonica
#   ml-test             — todos os testes ML acima em sequencia
#   ml-test-fast        — apenas OPE + features (rapido, sem treino)
#
# ALVOS (J4):
#   ml-promote-test     — pytest do gate de promocao (recusa sem uplift; aceita com uplift)
#   ml-j4-test          — promote-test (gate de producao J4)
#
# INVARIANTES DE GATE (J3/J4):
#   ml-ope-test:     DEVE ser verde antes de qualquer avaliacao de uplift.
#   ml-features-test: DEVE ser verde antes de promover qualquer versao de modelo.
#   ml-promote-test: DEVE ser verde antes de ativar AB_ENABLED=true em producao.
#                    Garante que o gating por codigo recusa promocao sem prova.

PYTHON_ML  := ml/.venv/bin/python
PYTEST_ML  := ml/.venv/bin/python -m pytest

## ml-ope-test: testes dos estimadores OPE (IPS/SNIPS/DR + filtro ml_fail_open)
ml-ope-test:
	PYTHONPATH=. $(PYTEST_ML) ml/ope/test_ope.py -v

## ml-features-test: paridade de featurizacao Python/Go
ml-features-test:
	PYTHONPATH=. $(PYTEST_ML) ml/features/ -v

## ml-training-test: pipeline de treino com dados sinteticos
ml-training-test:
	PYTHONPATH=. $(PYTEST_ML) ml/training/test_training_parity.py -v

## ml-calibration-test: calibracao isotonica (sem dados reais — usa sinteticos).
## exit 5 = pytest "no tests collected" -> OK; qualquer outro exit != 0 = falha real -> propaga.
ml-calibration-test:
	@PYTHONPATH=. $(PYTEST_ML) ml/calibration/ -v; rc=$$?; \
	if [ $$rc -eq 0 ] || [ $$rc -eq 5 ]; then \
		echo "[ml-calibration-test] OK (rc=$$rc)"; \
	else \
		echo "[ml-calibration-test] FALHOU (rc=$$rc)"; exit $$rc; \
	fi

## ml-promote-test: gate de promocao J4 (recusa sem uplift; aceita com uplift valido)
## INVARIANTE: deve ser verde antes de ativar AB_ENABLED=true em producao.
ml-promote-test:
	PYTHONPATH=. $(PYTEST_ML) ml/registry/test_promote.py -v

## ml-j4-test: testes de gate de producao J4 (apenas promote — rapido)
ml-j4-test: ml-promote-test
	@echo "ml-j4-test: COMPLETO. Gate de promocao J4 passou."

## ml-test-fast: OPE + features + promote (gate minimo de CI, sem treino)
ml-test-fast: ml-ope-test ml-features-test ml-promote-test
	@echo "ml-test-fast: COMPLETO."

## ml-test: todos os testes ML (OPE + features + treino + calibracao + promote)
ml-test: ml-ope-test ml-features-test ml-training-test ml-calibration-test ml-promote-test
	@echo ""
	@echo "ml-test: COMPLETO. Todos os testes ML passaram (J3+J4)."

## ml-deep-test: pytest do deep ranker K1 (TwoTowerDCNv2 + paridade ONNX)
## NOTA DE CUSTO: ~43 s (PyTorch + ONNX export). Alvo dedicado, fora do
## agregado ml-test, para nao inflar o gate rapido de CI. Obrigatorio no
## Passo 5 do runbook antes de ativar DEEP_ENABLED (K8 gate).
ml-deep-test:
	@echo "== ml-deep-test (K1 — TwoTowerDCNv2, ~43s) =="
	PYTHONPATH=. $(PYTEST_ML) ml/deep/test_deep.py -v
	@echo "ml-deep-test: OK"

.PHONY: ml-ope-test ml-features-test ml-training-test ml-calibration-test \
        ml-promote-test ml-j4-test ml-test-fast ml-test ml-deep-test
