package waitlist

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/rs/zerolog"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/logger"
)

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
	return fmt.Sprintf(`<p>Hi %s,</p><p>You're on the Rides waitlist. We'll email and text you as soon as we launch in your area.</p><p>Thanks for your interest in Rides.</p>`, name)
}

type Service struct {
	repo      Repo
	sms       SMSSender
	turnstile TurnstileVerifier
	mailer    Mailer
	log       zerolog.Logger
}

func NewService(repo Repo, sms SMSSender, turnstile TurnstileVerifier, mailer Mailer, log zerolog.Logger) *Service {
	return &Service{repo: repo, sms: sms, turnstile: turnstile, mailer: mailer, log: log}
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
	} else {
		s.log.Warn().Msg("waitlist: TURNSTILE_SECRET not set — skipping Turnstile verification")
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

	s.notify(ctx, result, name)
	return result, true, nil
}

// notify sends the best-effort confirmation SMS (always) and email (if an
// address was given and SMTP is configured). Delivery failures are logged
// and swallowed — the signup already committed to Postgres, and a flaky SMS
// gateway must never turn into an error surfaced to the submitter.
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
