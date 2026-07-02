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
	atSMSEndpoint         = "https://api.africastalking.com/version1/messaging"
	atWhatsAppEndpoint    = "https://content.africastalking.com/version1/messaging/whatsapp"
	pindoSMSEndpoint      = "https://api.pindo.io/v1/sms/"
	pindoVerifyEndpoint   = "https://api.pindo.io/v1/sms/verify"
	pindoVerifyCheckPoint = "https://api.pindo.io/v1/sms/verify/check"
)

// StartVerify asks Pindo to generate a PIN, deliver it to the phone (MTN or
// Airtel — Pindo routes by number), and returns the request_id used to check it
// later. Pindo owns the PIN lifecycle. Billed only on a successful check.
func (s *Service) StartVerify(ctx context.Context, phone string) (requestID string, err error) {
	if s.cfg.Pindo.APIToken == "" {
		return "", fmt.Errorf("telephony: pindo token not configured")
	}
	brand := s.cfg.Pindo.Brand
	if brand == "" {
		brand = "Rides"
	}
	raw, _ := json.Marshal(map[string]string{"brand": brand, "number": phone})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, pindoVerifyEndpoint, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.Pindo.APIToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("telephony: pindo verify start: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("telephony: pindo verify start %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out struct {
		RequestID string `json:"request_id"`
		Message   string `json:"message"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("telephony: pindo verify decode: %w", err)
	}
	if out.RequestID == "" {
		return "", fmt.Errorf("telephony: pindo verify: no request_id (%s)", strings.TrimSpace(string(body)))
	}
	return out.RequestID, nil
}

// CheckVerify validates the code the user entered against a prior StartVerify.
// Returns ok=false for a wrong/expired code (not an error), err only on transport
// or server failures.
func (s *Service) CheckVerify(ctx context.Context, requestID, code string) (ok bool, err error) {
	if s.cfg.Pindo.APIToken == "" {
		return false, fmt.Errorf("telephony: pindo token not configured")
	}
	raw, _ := json.Marshal(map[string]string{"code": code, "request_id": strings.TrimSpace(requestID)})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, pindoVerifyCheckPoint, bytes.NewReader(raw))
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.Pindo.APIToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")

	resp, err := s.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("telephony: pindo verify check: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	// 5xx = server problem (surface as error). 4xx = wrong/expired code (ok=false).
	if resp.StatusCode >= 500 {
		return false, fmt.Errorf("telephony: pindo verify check %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if resp.StatusCode >= 400 {
		return false, nil
	}
	var out struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &out)
	return strings.EqualFold(strings.TrimSpace(out.Message), "success"), nil
}

// SendOTP sends a 6-digit OTP to the given E.164 phone number, via whichever SMS
// provider is configured (SMS_PROVIDER: "pindo" or "africastalking").
func (s *Service) SendOTP(ctx context.Context, phone, otp string) error {
	// Bilingual (English + Kinyarwanda), code shown once. GSM-7-safe, ~155 chars
	// → a single SMS segment (~$0.01/OTP).
	message := fmt.Sprintf(
		"Your Rides verification code is %s valid for 10 minutes. Do not share or expose to anyone.\nIyi code imara iminota 10 ntuyisangize abandi ni iyawe gusa.",
		otp,
	)
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
