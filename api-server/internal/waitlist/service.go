package waitlist

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/logger"
)

// notifyTimeout bounds the best-effort confirmation SMS+email that runs in a
// detached goroutine after Submit returns — long enough to cover a slow
// Pindo call (10s telephony timeout) plus an SMTP dial, short enough that a
// hung third party can't leak goroutines indefinitely.
const notifyTimeout = 15 * time.Second

// SMSSender is the minimal telephony capability the waitlist service needs —
// satisfied by *telephony.Service.SendMessage. A narrow interface so tests
// can mock it without touching Pindo.
type SMSSender interface {
	SendMessage(ctx context.Context, phone, message string) error
}

// Repo is the persistence the service needs — satisfied by *Repository.
// Declared as an interface (mirrors internal/packages' Repo pattern) so unit
// tests can exercise Service.Submit's business logic (dedupe, notify-once)
// with an in-memory fake instead of a live Postgres connection.
type Repo interface {
	Create(ctx context.Context, in CreateInput) (*Signup, bool, error)
	List(ctx context.Context, f ListFilter) ([]*Signup, int, error)
}

// ErrTurnstileFailed is returned when Turnstile verification is configured
// and the token is missing/invalid/rejected by Cloudflare.
var ErrTurnstileFailed = apperrors.New(http.StatusForbidden, "TURNSTILE_FAILED", "human verification failed, please try again")

// confirmationSMS is bilingual (English + Kinyarwanda), short enough to stay
// a single GSM-7 segment.
const confirmationSMS = "You're on the Rides waitlist! We'll text you when we launch in your area. / Wiyandikishije kuri Rides! Tuzakumenyesha igihe tuzatangira aho uba."

const confirmationEmailSubject = "You're on the Rides waitlist"

func confirmationEmailHTML(name string) string {
	// name is user-supplied (the waitlist form's "name" field) and lands
	// verbatim in an HTML email body — escape it so "<script>..." or a stray
	// "<img onerror=...>" can't execute in whatever mail client renders it.
	return fmt.Sprintf(`<p>Hi %s,</p><p>You're on the Rides waitlist. We'll email and text you as soon as we launch in your area.</p><p>Thanks for your interest in Rides.</p>`, html.EscapeString(name))
}

type Service struct {
	repo      Repo
	sms       SMSSender
	turnstile TurnstileVerifier
	mailer    Mailer
	log       zerolog.Logger
	// isProduction is used only to decide how loudly to log a missing
	// TURNSTILE_SECRET at startup — Turnstile itself is fail-open in Submit
	// (see the comment there), so this no longer gates request behavior.
	isProduction bool
}

// NewService wires the waitlist service. isProduction should be
// cfg.Env == "production" — it only controls the severity of the startup log
// when TURNSTILE_SECRET is unset; Submit() is fail-open in every environment
// (see the Turnstile block in Submit for why).
func NewService(repo Repo, sms SMSSender, turnstile TurnstileVerifier, mailer Mailer, log zerolog.Logger, isProduction bool) *Service {
	if isProduction && (turnstile == nil || !turnstile.Configured()) {
		log.Warn().Msg("waitlist: TURNSTILE_SECRET is not set in production — submissions without a token will be allowed through (fail-open); bot/spam risk is bounded by rate limits only")
	}
	return &Service{repo: repo, sms: sms, turnstile: turnstile, mailer: mailer, log: log, isProduction: isProduction}
}

// SubmitInput is the handler-decoded, not-yet-normalized public request.
type SubmitInput struct {
	Role             string
	Name             string
	Phone            string
	Email            *string
	Area             *string
	VehicleType      *string
	ReferredBy       *string
	ConsentLaunch    bool
	ConsentMarketing bool
	Source           *string
	TurnstileToken   string
}

// Submit validates and persists a waitlist signup, then best-effort notifies
// the person by SMS (always) and email (if provided + SMTP configured).
//
// Returns created=false when (role, phone) already existed — the caller
// treats that as an idempotent success (same referral code, no repeat SMS).
func (s *Service) Submit(ctx context.Context, in SubmitInput, remoteIP string) (signup *Signup, created bool, err error) {
	if !in.ConsentLaunch {
		return nil, false, apperrors.New(http.StatusBadRequest, "CONSENT_REQUIRED", "consent_launch must be true")
	}
	if in.Role == RoleDriver && (in.VehicleType == nil || strings.TrimSpace(*in.VehicleType) == "") {
		return nil, false, apperrors.New(http.StatusBadRequest, "VEHICLE_TYPE_REQUIRED", "vehicle_type is required for drivers")
	}

	// Phone is optional (mirrors email): only normalize/validate it when the
	// submitter actually supplied one. A blank/absent phone is not an error —
	// it's passed through empty and persisted as NULL (see repository.Create).
	var phone string
	if trimmed := strings.TrimSpace(in.Phone); trimmed != "" {
		normalized, ok := normalizePhone(trimmed)
		if !ok {
			return nil, false, apperrors.New(http.StatusBadRequest, "INVALID_PHONE", "phone number is not valid")
		}
		phone = normalized
	}

	// Turnstile is FAIL-OPEN by deliberate product decision for this capture
	// form (Pacifique, 2026-08): a PRESENT token is still verified, and a
	// rejected/invalid token still 403s — that's the one case Turnstile
	// actually blocks (a bot that loaded the widget and got a bad verdict).
	// But an ABSENT/empty token — no widget loaded, JS/ad-blocker stripped
	// it, no secret configured yet — no longer 403s. This is a low-stakes
	// public form (name + area alone is enough to submit) and losing real
	// signups to a missing token was worse than the residual spam risk. The
	// backstop against abuse is the per-IP + per-phone rate limits on this
	// route (see cmd/server/main.go's waitlist route wiring), NOT Turnstile.
	// This is a deliberate relaxation from the earlier fail-closed hardening.
	token := strings.TrimSpace(in.TurnstileToken)
	switch {
	case token != "" && s.turnstile != nil && s.turnstile.Configured():
		verified, verr := s.turnstile.Verify(ctx, token, remoteIP)
		if verr != nil {
			s.log.Error().Err(verr).Msg("waitlist: turnstile verify request failed")
			return nil, false, ErrTurnstileFailed
		}
		if !verified {
			return nil, false, ErrTurnstileFailed
		}
	case token == "":
		s.log.Warn().Msg("waitlist: no turnstile token supplied — allowing (fail-open; rate limits are the remaining guard)")
	default:
		// Token present but no verifier configured (TURNSTILE_SECRET unset) —
		// nothing to check it against, so treat it the same as "no token".
		s.log.Warn().Msg("waitlist: turnstile token present but no verifier configured — allowing (fail-open)")
	}

	name := strings.TrimSpace(in.Name)
	result, wasCreated, err := s.repo.Create(ctx, CreateInput{
		Role:             in.Role,
		Name:             name,
		Phone:            phone,
		Email:            in.Email,
		Area:             in.Area,
		VehicleType:      in.VehicleType,
		ReferredBy:       in.ReferredBy,
		ConsentLaunch:    in.ConsentLaunch,
		ConsentMarketing: in.ConsentMarketing,
		Source:           in.Source,
	})
	if err != nil {
		return nil, false, fmt.Errorf("waitlist: create signup: %w", err)
	}

	if !wasCreated {
		// Repeat submission for the same (role, phone): idempotent success,
		// no second SMS/email so re-submitting never spams the person.
		return result, false, nil
	}

	// Dispatch the confirmation SMS/email in a detached goroutine so a slow
	// Pindo call or a hung SMTP dial never holds the HTTP response open. The
	// context is deliberately context.Background(), not ctx (=r.Context()):
	// the request context is cancelled the instant the client disconnects,
	// which would kill a best-effort notification that the signup (already
	// committed to Postgres) doesn't depend on.
	go func() {
		notifyCtx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
		defer cancel()
		s.notify(notifyCtx, result, name)
	}()
	return result, true, nil
}

// notify sends the best-effort confirmation SMS (always) and email (if an
// address was given and SMTP is configured). Delivery failures are logged
// and swallowed — the signup already committed to Postgres, and a flaky SMS
// gateway must never turn into an error surfaced to the submitter. Runs off
// the request path (see the goroutine in Submit) on a timeout-bounded,
// detached context.
func (s *Service) notify(ctx context.Context, signup *Signup, name string) {
	if s.sms != nil && strings.TrimSpace(signup.Phone) != "" {
		if err := s.sms.SendMessage(ctx, signup.Phone, confirmationSMS); err != nil {
			s.log.Error().Err(err).Str("phone", logger.MaskMSISDN(signup.Phone)).Msg("waitlist: confirmation SMS failed")
		}
	}

	if signup.Email == nil || strings.TrimSpace(*signup.Email) == "" {
		return
	}
	if s.mailer == nil || !s.mailer.Configured() {
		return
	}
	if err := s.mailer.Send(ctx, *signup.Email, confirmationEmailSubject, confirmationEmailHTML(name)); err != nil {
		s.log.Error().Err(err).Msg("waitlist: confirmation email failed")
	}
}

// List returns signups newest-first for the admin console.
func (s *Service) List(ctx context.Context, f ListFilter) ([]*Signup, int, error) {
	if f.Limit <= 0 {
		f.Limit = 20
	}
	if f.Limit > 100 {
		f.Limit = 100
	}
	return s.repo.List(ctx, f)
}
