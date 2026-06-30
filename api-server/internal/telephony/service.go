package telephony

import (
	"bytes"
	"context"
	"encoding/json"
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

const (
	atSMSEndpoint      = "https://api.africastalking.com/version1/messaging"
	atWhatsAppEndpoint = "https://content.africastalking.com/version1/messaging/whatsapp"
	pindoSMSEndpoint   = "https://api.pindo.io/v1/sms/"
)

// SendOTP sends a 6-digit OTP to the given E.164 phone number, via whichever SMS
// provider is configured (SMS_PROVIDER: "pindo" or "africastalking").
func (s *Service) SendOTP(ctx context.Context, phone, otp string) error {
	message := fmt.Sprintf("Your %s verification code is: %s. Valid for 10 minutes. Do not share it.", "Rides", otp)
	return s.sendSMS(ctx, phone, message)
}

// sendSMS routes a plain SMS to the active provider.
func (s *Service) sendSMS(ctx context.Context, phone, message string) error {
	if strings.EqualFold(s.cfg.SMSProvider, "pindo") {
		return s.sendPindoSMS(ctx, phone, message)
	}
	return s.sendAfricasTalkingSMS(ctx, phone, message)
}

// sendPindoSMS sends an SMS via Pindo (pindo.io) — the Rwanda-local gateway.
// POST https://api.pindo.io/v1/sms/  with a Bearer token and {to, text, sender}.
func (s *Service) sendPindoSMS(ctx context.Context, phone, message string) error {
	if s.cfg.Pindo.APIToken == "" {
		s.log.Warn().Msg("Pindo API token not configured — skipping SMS send")
		if s.cfg.Env == "production" {
			return fmt.Errorf("telephony: pindo token not configured")
		}
		return nil
	}

	payload := map[string]string{"to": phone, "text": message}
	if s.cfg.Pindo.Sender != "" {
		payload["sender"] = s.cfg.Pindo.Sender
	}
	raw, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, pindoSMSEndpoint, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("telephony: build pindo request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.Pindo.APIToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("telephony: pindo send: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telephony: pindo error %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// sendAfricasTalkingSMS sends an SMS via the Africa's Talking messaging API.
func (s *Service) sendAfricasTalkingSMS(ctx context.Context, phone, message string) error {
	if s.cfg.AT.APIKey == "" || s.cfg.AT.Username == "" {
		s.log.Warn().Msg("Africa's Talking credentials not configured — skipping SMS send")
		if s.cfg.Env == "production" {
			return fmt.Errorf("telephony: Africa's Talking credentials not configured")
		}
		return nil
	}

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

// SendOTPWhatsApp delivers an OTP message via Africa's Talking WhatsApp Business API.
// Only used as a dev-mode fallback — requires AT_WHATSAPP_ENABLED=true and a
// registered AT_WHATSAPP_SENDER number. Failures are non-fatal in non-production.
func (s *Service) SendOTPWhatsApp(ctx context.Context, phone, otp string) error {
	if s.cfg.AT.APIKey == "" || s.cfg.AT.Username == "" {
		s.log.Warn().Msg("Africa's Talking credentials not configured — skipping WhatsApp OTP")
		return nil
	}

	message := fmt.Sprintf("Your Taravelis verification code is: *%s*\n\nValid for 10 minutes. Do not share it.", otp)

	// AT WhatsApp API uses JSON body, not form-encoded.
	type waBody struct {
		Username    string `json:"username"`
		PhoneNumber string `json:"phoneNumber"`
		Message     string `json:"message"`
		From        string `json:"from,omitempty"`
	}
	body := waBody{
		Username:    s.cfg.AT.Username,
		PhoneNumber: phone,
		Message:     message,
	}
	if s.cfg.AT.WhatsAppSender != "" {
		body.From = s.cfg.AT.WhatsAppSender
	}

	jsonBytes, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("telephony: marshal whatsapp body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, atWhatsAppEndpoint, bytes.NewReader(jsonBytes))
	if err != nil {
		return fmt.Errorf("telephony: build whatsapp request: %w", err)
	}
	req.Header.Set("apiKey", s.cfg.AT.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("telephony: send whatsapp: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telephony: AT WhatsApp error %d: %s", resp.StatusCode, string(b))
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
