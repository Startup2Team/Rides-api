package telephony

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/workspace/ride-platform/config"
)

// Service wraps the Pindo (pindo.io) SMS and Verify (2FA) APIs.
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

// SendOTP sends a 6-digit OTP to the given E.164 phone number via Pindo SMS.
func (s *Service) SendOTP(ctx context.Context, phone, otp string) error {
	// Bilingual (Kinyarwanda + English). GSM-7-safe; ~153 chars = 1 SMS segment.
	message := fmt.Sprintf(
		"Kode ya Rides y'ibanga ni %s. Ntugomba kuyisangiza cyangwa ngo uyereke undi muntu. Your Rides OTP is %s. Do not share or expose it with anyone.",
		otp, otp,
	)
	return s.sendSMS(ctx, phone, message)
}

// sendSMS delivers a plain SMS via Pindo, the only supported gateway.
func (s *Service) sendSMS(ctx context.Context, phone, message string) error {
	return s.sendPindoSMS(ctx, phone, message)
}

// SendMessage delivers an arbitrary plain-text SMS (not an OTP) to an E.164
// phone number via Pindo. Exported for callers outside auth that need a
// one-off notification — e.g. the waitlist confirmation text — without
// duplicating the Pindo transport.
func (s *Service) SendMessage(ctx context.Context, phone, message string) error {
	return s.sendSMS(ctx, phone, message)
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
