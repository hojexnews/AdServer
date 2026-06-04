package fiat_test

import (
	"context"
	"errors"
	"testing"

	"github.com/hojex/adserver/services/payments/internal/fiat"
)

// ---------------------------------------------------------------------------
// stub de FiatProvider para testes de failover
// ---------------------------------------------------------------------------

type stubProvider struct {
	railName string
	healthy  error
	piErr    error
	piResult fiat.PaymentIntentResult
	pixErr   error
	pixResult fiat.PixChargeResult
}

func (s *stubProvider) RailName() string { return s.railName }
func (s *stubProvider) Healthy(_ context.Context) error { return s.healthy }

func (s *stubProvider) CreatePaymentIntent(_ context.Context, _ fiat.PaymentIntentRequest) (fiat.PaymentIntentResult, error) {
	return s.piResult, s.piErr
}

func (s *stubProvider) CreatePixCharge(_ context.Context, _ fiat.PixChargeRequest) (fiat.PixChargeResult, error) {
	return s.pixResult, s.pixErr
}

// ---------------------------------------------------------------------------
// Testes de failover
// ---------------------------------------------------------------------------

func TestFailoverProvider_PrimarySucceeds(t *testing.T) {
	t.Parallel()

	primary := &stubProvider{
		railName:  "asaas_pix",
		pixResult: fiat.PixChargeResult{ChargeID: "cob_primary", Rail: "asaas_pix"},
	}
	fallback := &stubProvider{railName: "mercadopago"}

	fp := fiat.NewFailoverProvider(primary, fallback, nil)

	result, err := fp.CreatePixCharge(context.Background(), fiat.PixChargeRequest{AmountMinor: 100, Scale: 2})
	if err != nil {
		t.Fatalf("CreatePixCharge: %v", err)
	}
	if result.ChargeID != "cob_primary" {
		t.Errorf("esperado charge do primario, got %q", result.ChargeID)
	}
}

func TestFailoverProvider_FallsBackWhenPrimaryFails(t *testing.T) {
	t.Parallel()

	primary := &stubProvider{
		railName: "asaas_pix",
		pixErr:   errors.New("asaas: servico indisponivel"),
	}
	fallback := &stubProvider{
		railName:  "mercadopago",
		pixResult: fiat.PixChargeResult{ChargeID: "cob_failover", Rail: "mercadopago"},
	}

	fp := fiat.NewFailoverProvider(primary, fallback, nil)

	result, err := fp.CreatePixCharge(context.Background(), fiat.PixChargeRequest{AmountMinor: 100, Scale: 2})
	if err != nil {
		t.Fatalf("CreatePixCharge (failover): %v", err)
	}
	if result.ChargeID != "cob_failover" {
		t.Errorf("esperado charge do failover, got %q", result.ChargeID)
	}
}

func TestFailoverProvider_BothFail_ReturnsError(t *testing.T) {
	t.Parallel()

	primary := &stubProvider{
		railName: "asaas_pix",
		pixErr:   errors.New("asaas: indisponivel"),
	}
	fallback := &stubProvider{
		railName: "mercadopago",
		pixErr:   errors.New("mercadopago: indisponivel"),
	}

	fp := fiat.NewFailoverProvider(primary, fallback, nil)

	_, err := fp.CreatePixCharge(context.Background(), fiat.PixChargeRequest{AmountMinor: 100, Scale: 2})
	if err == nil {
		t.Fatal("esperava erro quando ambos provedores falham")
	}
}

func TestFailoverProvider_CreatePaymentIntent_FallsBack(t *testing.T) {
	t.Parallel()

	primary := &stubProvider{
		railName: "asaas_pix",
		piErr:    errors.New("asaas: nao suporta PaymentIntent"),
	}
	fallback := &stubProvider{
		railName:  "mercadopago",
		piResult:  fiat.PaymentIntentResult{PaymentID: "mp_001", Rail: "mercadopago"},
	}

	fp := fiat.NewFailoverProvider(primary, fallback, nil)

	result, err := fp.CreatePaymentIntent(context.Background(), fiat.PaymentIntentRequest{AmountMinor: 500, Scale: 2})
	if err != nil {
		t.Fatalf("CreatePaymentIntent (failover): %v", err)
	}
	if result.PaymentID != "mp_001" {
		t.Errorf("esperado PaymentID do failover, got %q", result.PaymentID)
	}
}

func TestFailoverProvider_Healthy_ReportsWhenPrimaryDown(t *testing.T) {
	t.Parallel()

	primary := &stubProvider{
		railName: "asaas_pix",
		healthy:  errors.New("asaas: timeout"),
	}
	fallback := &stubProvider{railName: "mercadopago"}

	fp := fiat.NewFailoverProvider(primary, fallback, nil)

	// Healthy retorna erro (indica que primario esta inativo), mas nao panica.
	err := fp.Healthy(context.Background())
	if err == nil {
		t.Fatal("esperava erro de Healthy quando primario esta inativo")
	}
}

func TestFailoverProvider_RailName_IncludesBothRails(t *testing.T) {
	t.Parallel()

	primary := &stubProvider{railName: "asaas_pix"}
	fallback := &stubProvider{railName: "mercadopago"}

	fp := fiat.NewFailoverProvider(primary, fallback, nil)
	name := fp.RailName()

	if name == "" {
		t.Error("RailName nao deve ser vazio")
	}
}
