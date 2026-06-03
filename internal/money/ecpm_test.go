package money_test

import (
	"testing"

	"github.com/hojex/adserver/internal/money"
	moneyv1 "github.com/hojex/adserver/gen/go/adserver/money/v1"
)

func brl(amount int64) *moneyv1.Money {
	return &moneyv1.Money{AssetCode: "BRL", Amount: amount, Scale: 2}
}

func usd(amount int64) *moneyv1.Money {
	return &moneyv1.Money{AssetCode: "USD", Amount: amount, Scale: 2}
}

func TestCompare_EqualAmounts(t *testing.T) {
	cmp, err := money.Compare(brl(100), brl(100))
	if err != nil {
		t.Fatal(err)
	}
	if cmp != 0 {
		t.Errorf("expected 0, got %d", cmp)
	}
}

func TestCompare_LessThan(t *testing.T) {
	cmp, err := money.Compare(brl(50), brl(100))
	if err != nil {
		t.Fatal(err)
	}
	if cmp != -1 {
		t.Errorf("expected -1, got %d", cmp)
	}
}

func TestCompare_GreaterThan(t *testing.T) {
	cmp, err := money.Compare(brl(200), brl(100))
	if err != nil {
		t.Fatal(err)
	}
	if cmp != 1 {
		t.Errorf("expected 1, got %d", cmp)
	}
}

func TestCompare_CrossCurrencyError(t *testing.T) {
	_, err := money.Compare(brl(100), usd(100))
	if err == nil {
		t.Error("expected error for cross-currency compare (DA-10)")
	}
}

func TestCompare_DifferentScale(t *testing.T) {
	// 1.00 BRL (scale=2) vs 100 BRL (scale=0) — same real value.
	a := &moneyv1.Money{AssetCode: "BRL", Amount: 100, Scale: 2}
	b := &moneyv1.Money{AssetCode: "BRL", Amount: 1, Scale: 0}
	cmp, err := money.Compare(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if cmp != 0 {
		t.Errorf("expected equal values with different scale, got cmp=%d", cmp)
	}
}

func TestECPMMinorUnits_Basic(t *testing.T) {
	// R$10 revenue / 1000 impressions → eCPM R$10 (minor-units: 1000 centavos)
	result := money.ECPMMinorUnits(1000, 1000)
	if result != 1000 {
		t.Errorf("expected 1000, got %d", result)
	}
}

func TestECPMMinorUnits_ZeroImpressions(t *testing.T) {
	result := money.ECPMMinorUnits(500, 0)
	if result != 0 {
		t.Errorf("expected 0 on division by zero, got %d", result)
	}
}

func TestECPMMinorUnits_HalfDelivery(t *testing.T) {
	// R$5 revenue / 500 impressions → eCPM = (500 * 1000) / 500 = 1000 centavos
	result := money.ECPMMinorUnits(500, 500)
	if result != 1000 {
		t.Errorf("expected 1000, got %d", result)
	}
}

func TestIsZero(t *testing.T) {
	if !money.IsZero(brl(0)) {
		t.Error("expected IsZero(0) = true")
	}
	if money.IsZero(brl(1)) {
		t.Error("expected IsZero(1) = false")
	}
}

func TestIsPositive(t *testing.T) {
	if !money.IsPositive(brl(1)) {
		t.Error("expected IsPositive(1) = true")
	}
	if money.IsPositive(brl(0)) {
		t.Error("expected IsPositive(0) = false")
	}
}

func TestGreaterThan(t *testing.T) {
	ok, err := money.GreaterThan(brl(200), brl(100))
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expected 200 > 100")
	}
}

func BenchmarkCompare(b *testing.B) {
	a := brl(12345)
	c := brl(67890)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = money.Compare(a, c)
	}
}

func BenchmarkECPMMinorUnits(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = money.ECPMMinorUnits(123456, 9876)
	}
}
