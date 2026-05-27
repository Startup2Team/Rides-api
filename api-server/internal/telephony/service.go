package telephony

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/workspace/ride-platform/config"
)

// Service wraps Africa's Talking SMS and masked-number APIs.
type Service struct {
	cfg    *config.Config
	client *http.Client
	log    zerolog.Logger
}

func New(cfg *config.Config, log zerolog.Logger) *Service {
	return &Service{
		cfg:    cfg,
		client: &http.Client{Timeout: 10 * time.Second},
		log:    log,
	}
}

const atSMSEndpoint = "https://api.africastalking.com/version1/messaging"

// SendOTP sends a 6-digit OTP to the given E.164 phone number via Africa's Talking.
func (s *Service) SendOTP(ctx context.Context, phone, otp string) error {
	if s.cfg.AT.APIKey == "" || s.cfg.AT.Username == "" {
		s.log.Warn().Msg("Africa's Talking credentials not configured — skipping SMS send")
		return nil
	}

	message := fmt.Sprintf("Your %s verification code is: %s. Valid for 10 minutes. Do not share it.", "RidePlatform", otp)

	form := url.Values{}
	form.Set("username", s.cfg.AT.Username)
	form.Set("to", phone)
	form.Set("message", message)
	if s.cfg.AT.SenderID != "" {
		form.Set("from", s.cfg.AT.SenderID)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, atSMSEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("telephony: build request: %w", err)
	}
	req.Header.Set("apiKey", s.cfg.AT.APIKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("telephony: send sms: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telephony: AT error %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// GetMaskedNumber returns the Africa's Talking masking number for a ride.
// This is a static number from config — AT routes the call through it
// so neither party sees the other's real phone number.
func (s *Service) GetMaskedNumber(ctx context.Context, rideID string) (string, error) {
	if s.cfg.AT.MaskingNumber == "" {
		return "", fmt.Errorf("telephony: masking number not configured")
	}
	// In production: call AT Phone Number Masking API to create a session.
	// For v1, the masking number is a shared number per region.
	return s.cfg.AT.MaskingNumber, nil
}
