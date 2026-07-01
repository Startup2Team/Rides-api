package payment

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/workspace/ride-platform/config"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// Provider identifies the payment channel.
type Provider string

const (
	ProviderMTNMoMo Provider = "MTN_MOMO"
	ProviderAirtel  Provider = "AIRTEL_MONEY"
)

// Payment status values returned to callers (normalised across providers).
const (
	StatusPending = "PENDING"
	StatusSuccess = "SUCCESS"
	StatusFailed  = "FAILED"
)

// PaymentResult captures the outcome of a payment initiation.
type PaymentResult struct {
	TransactionID string
	Status        string // PENDING | SUCCESS | FAILED
	Message       string
}

// Service wraps MTN MoMo and Airtel Money payment APIs.
type Service struct {
	cfg  *config.Config
	log  zerolog.Logger
	http *http.Client

	mu        sync.Mutex
	mtnToken  string
	mtnExpiry time.Time
}

func New(cfg *config.Config, log zerolog.Logger) *Service {
	return &Service{
		cfg:  cfg,
		log:  log,
		http: &http.Client{Timeout: 20 * time.Second},
	}
}

// mtnConfigured reports whether the live MTN Collections credentials are all
// present. Without them the service stays inert (mock PENDING) so dev and
// pre-provisioning environments keep working.
func (s *Service) mtnConfigured() bool {
	return s.cfg.MoMo.APIUser != "" && s.cfg.MoMo.APIKey != "" && s.cfg.MoMo.SubscriptionKey != ""
}

// mtnBaseURL resolves the Collections host: explicit override, else by env.
func (s *Service) mtnBaseURL() string {
	if s.cfg.MoMo.BaseURL != "" {
		return strings.TrimRight(s.cfg.MoMo.BaseURL, "/")
	}
	if s.targetEnv() == "sandbox" {
		return "https://sandbox.momodeveloper.mtn.com"
	}
	return "https://proxy.momoapi.mtn.com"
}

// targetEnv is MTN's X-Target-Environment ("sandbox" or e.g. "mtnrwanda").
func (s *Service) targetEnv() string {
	if s.cfg.MoMo.Environment == "" {
		return "sandbox"
	}
	return s.cfg.MoMo.Environment
}

// currency is "EUR" in sandbox (MTN's only accepted sandbox currency) and the
// real currency in production. An explicit MOMO_CURRENCY always wins.
func (s *Service) currency() string {
	if s.cfg.MoMo.Currency != "" {
		return s.cfg.MoMo.Currency
	}
	if s.targetEnv() == "sandbox" {
		return "EUR"
	}
	return "RWF"
}

// RequestPayment initiates a mobile money collection request.
func (s *Service) RequestPayment(ctx context.Context, provider Provider, phone, momoCode string, amount float64, externalRef string) (*PaymentResult, error) {
	switch provider {
	case ProviderMTNMoMo:
		return s.requestMTNMoMo(ctx, phone, amount, externalRef)
	case ProviderAirtel:
		return s.requestAirtel(ctx, phone, amount, externalRef)
	default:
		return nil, fmt.Errorf("payment: unknown provider %q", provider)
	}
}

// QueryStatus checks the live status of a previously initiated collection,
// keyed by the reference id we passed as X-Reference-Id (our payment_ref).
// Returns a normalised SUCCESS | FAILED | PENDING.
func (s *Service) QueryStatus(ctx context.Context, provider Provider, referenceID string) (string, error) {
	switch provider {
	case ProviderMTNMoMo:
		return s.queryMTNMoMo(ctx, referenceID)
	default:
		// Airtel reconciliation not implemented — leave PENDING for manual review.
		return StatusPending, nil
	}
}

// ── MTN MoMo Collections ─────────────────────────────────────────────────────

// mtnAccessToken fetches and caches a Collections bearer token. MTN tokens last
// ~3600s; we refresh a minute early.
func (s *Service) mtnAccessToken(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mtnToken != "" && time.Now().Before(s.mtnExpiry) {
		return s.mtnToken, nil
	}

	url := s.mtnBaseURL() + "/collection/token/"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return "", err
	}
	basic := base64.StdEncoding.EncodeToString([]byte(s.cfg.MoMo.APIUser + ":" + s.cfg.MoMo.APIKey))
	req.Header.Set("Authorization", "Basic "+basic)
	req.Header.Set("Ocp-Apim-Subscription-Key", s.cfg.MoMo.SubscriptionKey)

	resp, err := s.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("momo: token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("momo: token http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", fmt.Errorf("momo: token decode: %w", err)
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("momo: empty access token")
	}
	ttl := tok.ExpiresIn
	if ttl <= 0 {
		ttl = 3600
	}
	s.mtnToken = tok.AccessToken
	s.mtnExpiry = time.Now().Add(time.Duration(ttl-60) * time.Second)
	return s.mtnToken, nil
}

func (s *Service) requestMTNMoMo(ctx context.Context, phone string, amount float64, externalRef string) (*PaymentResult, error) {
	if !s.mtnConfigured() {
		s.log.Warn().Msg("MTN MoMo not fully configured (need MOMO_API_USER/MOMO_API_KEY/MOMO_SUBSCRIPTION_KEY) — returning mock PENDING")
		return &PaymentResult{TransactionID: externalRef, Status: StatusPending, Message: "MoMo not configured — mock result"}, nil
	}

	token, err := s.mtnAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	payload := map[string]any{
		"amount":       fmt.Sprintf("%.0f", amount),
		"currency":     s.currency(),
		"externalId":   externalRef,
		"payer":        map[string]string{"partyIdType": "MSISDN", "partyId": normalizeMSISDN(phone)},
		"payerMessage": "Rides package purchase",
		"payeeNote":    "Rides package purchase",
	}
	raw, _ := json.Marshal(payload)

	url := s.mtnBaseURL() + "/collection/v1_0/requesttopay"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Reference-Id", externalRef) // must be a UUID; we pass payment_ref
	req.Header.Set("X-Target-Environment", s.targetEnv())
	req.Header.Set("Ocp-Apim-Subscription-Key", s.cfg.MoMo.SubscriptionKey)
	req.Header.Set("Content-Type", "application/json")
	if s.cfg.MoMo.CallbackURL != "" {
		req.Header.Set("X-Callback-Url", s.cfg.MoMo.CallbackURL)
	}

	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("momo: requesttopay: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	// 202 Accepted is the documented success: the prompt is sent to the payer.
	if resp.StatusCode != http.StatusAccepted {
		return nil, fmt.Errorf("momo: requesttopay http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	s.log.Info().Str("ref", externalRef).Float64("amount", amount).Msg("MTN MoMo RequestToPay accepted")
	return &PaymentResult{TransactionID: externalRef, Status: StatusPending, Message: "Payment request sent to customer's phone"}, nil
}

func (s *Service) queryMTNMoMo(ctx context.Context, referenceID string) (string, error) {
	if !s.mtnConfigured() {
		return StatusPending, nil
	}
	token, err := s.mtnAccessToken(ctx)
	if err != nil {
		return "", err
	}

	url := s.mtnBaseURL() + "/collection/v1_0/requesttopay/" + referenceID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Target-Environment", s.targetEnv())
	req.Header.Set("Ocp-Apim-Subscription-Key", s.cfg.MoMo.SubscriptionKey)

	resp, err := s.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("momo: status: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("momo: status http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out struct {
		Status string `json:"status"` // SUCCESSFUL | FAILED | PENDING
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("momo: status decode: %w", err)
	}
	switch strings.ToUpper(out.Status) {
	case "SUCCESSFUL", "SUCCESS":
		return StatusSuccess, nil
	case "FAILED", "REJECTED", "TIMEOUT", "EXPIRED":
		return StatusFailed, nil
	default:
		return StatusPending, nil
	}
}

func (s *Service) requestAirtel(ctx context.Context, phone string, amount float64, externalRef string) (*PaymentResult, error) {
	if s.cfg.Env == "production" && s.cfg.Payments.Enabled {
		return nil, apperrors.New(http.StatusNotImplemented, "AIRTEL_NOT_READY", "Airtel Money is not production-ready")
	}
	s.log.Info().Str("ref", externalRef).Float64("amount", amount).Msg("Airtel Money payment initiated (stub)")
	return &PaymentResult{TransactionID: externalRef, Status: StatusPending, Message: "Airtel payment request sent (stub)"}, nil
}

// normalizeMSISDN strips spaces, dashes and a leading '+' so the number is the
// bare international MSISDN MTN expects (e.g. 250788123456).
func normalizeMSISDN(phone string) string {
	r := strings.NewReplacer("+", "", " ", "", "-", "", "(", "", ")", "")
	return r.Replace(phone)
}
