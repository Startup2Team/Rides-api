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
	// isProduction gates the Turnstile fail-open/fail-closed behavior below:
	// staging/dev may run without a Turnstile secret (skip verification),
	// production never may (this is a public, SMS-cost-triggering endpoint).
	isProduction bool
}

// NewService wires the waitlist service. isProduction should be
// cfg.Env == "production" — it controls whether a missing TURNSTILE_SECRET
// fails the request closed (production) or merely skips verification
// (staging/dev, where real Turnstile keys may not exist yet).
func NewService(repo Repo, sms SMSSender, turnstile TurnstileVerifier, mailer Mailer, log zerolog.Logger, isProduction bool) *Service {
	if isProduction && (turnstile == nil || !turnstile.Configured()) {
		log.Error().Msg("waitlist: TURNSTILE_SECRET is not set in production — the public waitlist endpoint will reject all submissions until it is configured")
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

	phone, ok := normalizePhone(in.Phone)
	if !ok {
		return nil, false, apperrors.New(http.StatusBadRequest, "INVALID_PHONE", "phone number is not valid")
	}

	// Turnstile verification happens before ANY DB write or SMS — this is the
	// bot/abuse gate for an unauthenticated, SMS-cost-triggering endpoint.
	if s.turnstile != nil && s.turnstile.Configured() {
		verified, verr := s.turnstile.Verify(ctx, in.TurnstileToken, remoteIP)
		if verr != nil {
			s.log.Error().Err(verr).Msg("waitlist: turnstile verify request failed")
			return nil, false, ErrTurnstileFailed
		}
		if !verified {
			return nil, false, ErrTurnstileFailed
		}
	} else if s.isProduction {
		// Fail closed: without a secret we cannot prove the submitter is
		// human, and this endpoint is unauthenticated + triggers a real SMS
		// send. Skipping verification here would turn a missing env var into
		// an open spam/SMS-cost relay in production.
		s.log.Error().Msg("waitlist: TURNSTILE_SECRET not set in production — rejecting submission")
		return nil, false, ErrTurnstileFailed
	} else {
		s.log.Warn().Msg("waitlist: TURNSTILE_SECRET not set — skipping Turnstile verification (non-production)")
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
	if s.sms != nil {
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
