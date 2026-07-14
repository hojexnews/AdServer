"""
Regressão do CPM (sweep item 6 / MONEY-01): a semântica de FLOOR do
BILLING.md §4.1 — mille = floor(impressions / 1000); billing = mille * rate.
A fração sub-mille NÃO é faturada aqui (fica para o próximo bucket). O código
antigo fazia (impressions/1000)*rate, faturando a fração (999 impressões → 2.00
em vez de 0.00; 5500 → 11.00 em vez de 10.00).

Só stdlib (decimal). Rodável como script (assert + __main__) e via pytest.
"""

import importlib.util
import os
import sys
from decimal import Decimal

_HERE = os.path.dirname(os.path.abspath(__file__))
# Carrega o módulo por caminho (evita depender de pyiceberg/pacote); registra em
# sys.modules porque @dataclass faz introspecção via cls.__module__.
_spec = importlib.util.spec_from_file_location(
    "billing_batch_hourly", os.path.join(_HERE, "billing_batch_hourly.py")
)
bbh = importlib.util.module_from_spec(_spec)
sys.modules["billing_batch_hourly"] = bbh
_spec.loader.exec_module(bbh)

BRL = bbh.AssetInfo("BRL", scale=2)
USDC = bbh.AssetInfo("USDC", scale=6)


def test_cpm_floor_semantics():
    # (impressions, rate, esperado) = floor(imp/1000) * rate  (BILLING.md §4.1)
    cases = [
        (5500, Decimal("2"), "10.00"),  # 5 milhares completos, NÃO 11.00
        (999, Decimal("2"), "0.00"),    # sub-mille → 0, NÃO 2.00
        (1500, Decimal("2"), "2.00"),   # 1 milhar completo
        (1000, Decimal("2"), "2.00"),
        (1999, Decimal("2"), "2.00"),
        (0, Decimal("2"), "0.00"),
    ]
    for imp, rate, want in cases:
        got = bbh.calc_cpm_amount(imp, rate, BRL)
        assert str(got) == want, f"imp={imp} rate={rate}: got {got}, want {want}"


def test_cpm_no_partial_mille_billed():
    # Nenhuma fração de milhar é faturada: qualquer imp em [0, 999] → 0.
    for imp in (0, 1, 500, 999):
        assert bbh.calc_cpm_amount(imp, Decimal("3.50"), BRL) == Decimal("0.00")


def test_cpm_usdc_scale6():
    # mille inteiro * rate, quantizado ao scale 6 do USDC.
    assert str(bbh.calc_cpm_amount(2500, Decimal("1.5"), USDC)) == "3.000000"


if __name__ == "__main__":
    test_cpm_floor_semantics()
    test_cpm_no_partial_mille_billed()
    test_cpm_usdc_scale6()
    print("test_billing_batch_hourly: OK — CPM floor semantics (BILLING.md §4.1)")
