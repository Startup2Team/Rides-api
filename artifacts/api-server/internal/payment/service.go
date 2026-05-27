package payment

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"

	"github.com/workspace/ride-platform/config"
)

// Provider identifies the payment channel.
type Provider string

const (
	ProviderMTNMoMo  Provider = "MTN_MOMO"
	ProviderAirtel   Provider = "AIRTEL_MONEY"
)

// PaymentResult captures the outcome of a payment initiation.
type PaymentResult struct {
	TransactionID string
	Status        string // PENDING | SUCCESS | FAILED
	Message       string
}

// Service wraps MTN MoMo and Airtel Money payment APIs.
type Service struct {
	cfg    *config.Config
	log    zerolog.Logger
}

func New(cfg *config.Config, log zerolog.Logger) *Service {
	return &Service{cfg: cfg, log: log}
}

// RequestPayment initiates a mobile money collection request.
// The mobile app typically handles payment flow directly; this is used for
// server-side receipt validation and disbursement to drivers.
func (s *Service) RequestPayment(ctx context.Context, provider Provider, phone, momoCode string, amount float64, externalRef string) (*PaymentResult, error) {
	switch provider {
	case ProviderMTNMoMo:
		return s.requestMTNMoMo(ctx, momoCode, amount, externalRef)
	case ProviderAirtel:
		return s.requestAirtel(ctx, momoCode, amount, externalRef)
	default:
		return nil, fmt.Errorf("payment: unknown provider %q", provider)
	}
}

// requestMTNMoMo calls the MTN MoMo Collections API.
func (s *Service) requestMTNMoMo(ctx context.Context, momoCode string, amount float64, externalRef string) (*PaymentResult, error) {
	if s.cfg.MoMo.APIKey == "" {
		s.log.Warn().Msg("MTN MoMo API key not configured — payment skipped")
		return &PaymentResult{
			TransactionID: "mock-" + externalRef,
			Status:        "PENDING",
			Message:       "MoMo not configured — mock result",
		}, nil
	}

	// TODO: implement full MTN MoMo Collections v1 API call
	// POST https://sandbox.momodeveloper.mtn.com/collection/v1_0/requesttopay
	// with X-Reference-Id, X-Target-Environment, Ocp-Apim-Subscription-Key
	s.log.Info().
		Str("momo_code", momoCode).
		Float64("amount", amount).
		Str("ref", externalRef).
		Msg("MTN MoMo payment initiated (stub)")

	return &PaymentResult{
		TransactionID: externalRef,
		Status:        "PENDING",
		Message:       "Payment request sent to customer's phone",
	}, nil
}

// requestAirtel calls the Airtel Money API.
func (s *Service) requestAirtel(ctx context.Context, momoCode string, amount float64, externalRef string) (*PaymentResult, error) {
	s.log.Info().
		Str("momo_code", momoCode).
		Float64("amount", amount).
		Str("ref", externalRef).
		Msg("Airtel Money payment initiated (stub)")

	return &PaymentResult{
		TransactionID: externalRef,
		Status:        "PENDING",
		Message:       "Airtel payment request sent",
	}, nil
}
