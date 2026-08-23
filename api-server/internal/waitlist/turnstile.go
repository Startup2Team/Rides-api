package waitlist

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const turnstileVerifyEndpoint = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

// TurnstileVerifier checks a Cloudflare Turnstile token server-side before
// the waitlist form is allowed to write to Postgres or trigger an SMS.
type TurnstileVerifier interface {
	// Configured reports whether a secret is set. When false, the caller
	// skips verification entirely (staging before real keys exist) — kept as
	// a separate method rather than a nil check so callers can't forget it.
	Configured() bool
	// Verify returns true if Cloudflare accepted the token. err is only for
	// transport/API failures, not a rejected token (which is ok=false).
	Verify(ctx context.Context, token, remoteIP string) (bool, error)
}

// CloudflareTurnstile is the production TurnstileVerifier.
type CloudflareTurnstile struct {
	secret string
	client *http.Client
}

func NewCloudflareTurnstile(secret string) *CloudflareTurnstile {
	return &CloudflareTurnstile{
		secret: secret,
		client: &http.Client{Timeout: 8 * time.Second},
	}
}

func (t *CloudflareTurnstile) Configured() bool {
	return t != nil && t.secret != ""
}

func (t *CloudflareTurnstile) Verify(ctx context.Context, token, remoteIP string) (bool, error) {
	if t.secret == "" {
		// Should not be reached — callers check Configured() first — but fail
		// closed rather than silently accepting if it ever is.
		return false, fmt.Errorf("turnstile: secret not configured")
	}

	form := url.Values{}
	form.Set("secret", t.secret)
	form.Set("response", token)
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, turnstileVerifyEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return false, fmt.Errorf("build turnstile request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := t.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("turnstile verify: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode >= 500 {
		return false, fmt.Errorf("turnstile verify %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return false, fmt.Errorf("decode turnstile response: %w", err)
	}
	return out.Success, nil
}
