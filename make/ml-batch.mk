# make/ml-batch.mk — Fragmento de Makefile para ml/pacing/ e ml/fraud/ (J6).
# Dono: ml-optimization-engineer.
# Incluido pelo root Makefile via -include make/*.mk.
#
# Convencao (make/README.md):
#   - Nao reusar nomes de alvo do root Makefile.
#   - Declarar .PHONY para todos os alvos.
#   - Prefixar com 'ml-batch-'.
#   - ml/ope/ e make/ml.mk sao de outro incremento (nao tocar).

ML_VENV         := ml/.venv/bin/python
ML_ROOT         := ml
ML_PACING_DIR   := ml/pacing
ML_FRAUD_DIR    := ml/fraud
ML_REGISTRY_URI := file:$(ML_ROOT)/registry/mlruns

.PHONY: ml-batch-test ml-batch-test-pacing ml-batch-test-fraud \
        ml-batch-test-unsup \
        ml-batch-train-ivt ml-batch-pacing-dryrun ml-batch-help \
        ml-batch-no-float

## ml-batch-test: roda todos os testes de pacing + fraude supervisionada + fraude nao-supervisionada (gate J6/K2)
ml-batch-test: ml-batch-test-pacing ml-batch-test-fraud ml-batch-test-unsup
	@echo "ml-batch-test: OK — pacing, fraud e unsup passaram."

## ml-batch-test-pacing: pytest de ml/pacing/ (controlador proporcional DA-4)
ml-batch-test-pacing:
	@echo "== ml-batch-test-pacing =="
	@set -o pipefail; $(ML_VENV) -m pytest $(ML_PACING_DIR)/test_pacing.py -v \
		--tb=short -q 2>&1 | tail -20
	@echo "ml-batch-test-pacing: OK"

## ml-batch-test-fraud: pytest de ml/fraud/ (classificador IVT TX-6)
ml-batch-test-fraud:
	@echo "== ml-batch-test-fraud =="
	@set -o pipefail; $(ML_VENV) -m pytest $(ML_FRAUD_DIR)/test_fraud.py -v \
		--tb=short -q 2>&1 | tail -30
	@echo "ml-batch-test-fraud: OK"

## ml-batch-test-unsup: pytest de ml/fraud/test_unsup.py (IVT nao-supervisionado K2: IF + AE)
ml-batch-test-unsup:
	@echo "== ml-batch-test-unsup (K2 — IF + Autoencoder) =="
	@set -o pipefail; PYTHONPATH=. $(ML_VENV) -m pytest $(ML_FRAUD_DIR)/test_unsup.py -v \
		--tb=short -q 2>&1 | tail -40
	@echo "ml-batch-test-unsup: OK"

## ml-batch-train-ivt: treina o classificador IVT e registra no MLflow
ml-batch-train-ivt:
	@echo "== ml-batch-train-ivt (ivt_classifier) =="
	$(ML_VENV) $(ML_FRAUD_DIR)/train_ivt.py \
		--mlflow-tracking-uri $(ML_REGISTRY_URI) \
		--run-name ivt_lgb_v1
	@echo "ml-batch-train-ivt: OK — modelo registrado como 'ivt_classifier'."

## ml-batch-pacing-dryrun: executa um ciclo de pacing em dry-run (sem Redis)
ml-batch-pacing-dryrun:
	@echo "== ml-batch-pacing-dryrun =="
	$(ML_VENV) $(ML_PACING_DIR)/pacing_job.py --dry-run
	@echo "ml-batch-pacing-dryrun: OK"

## ml-batch-no-float: verifica ausencia de float monetario em ml/pacing/ e ml/fraud/ (TX-2)
ml-batch-no-float:
	@echo "== ml-batch-no-float (TX-2) =="
	@fail=0; \
	for f in $(ML_PACING_DIR)/*.py $(ML_FRAUD_DIR)/*.py; do \
		[ -f "$$f" ] || continue; \
		if grep -nE '\bfloat\b.*minor_units|minor_units.*\bfloat\b' "$$f"; then \
			echo "  POSSIVEL VIOLACAO TX-2 em $$f"; \
			fail=1; \
		fi; \
	done; \
	[ "$$fail" -eq 0 ] && echo "ml-batch-no-float: OK" || exit 1

## ml-batch-help: lista os alvos de ml/pacing + ml/fraud
ml-batch-help:
	@echo ""
	@echo "Alvos de ml/pacing + ml/fraud (J6, ml-optimization-engineer):"
	@grep -E '^## ml-batch-' make/ml-batch.mk | sed 's/^## //' | \
		awk -F': ' '{printf "  \033[1m%-35s\033[0m %s\n", $$1, $$2}'
	@echo ""
