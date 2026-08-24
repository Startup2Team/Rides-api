package waitlist_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/internal/waitlist"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// ── Test doubles ──────────────────────────────────────────────────────────

type fakeRepo struct {
	createCalls []waitlist.CreateInput
	// created controls Create's return: nil defaults to always "new".
	nextCreated  bool
	nextExisting *waitlist.Signup
	createErr    error
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{nextCreated: true}
}

func (f *fakeRepo) Create(_ context.Context, in waitlist.CreateInput) (*waitlist.Signup, bool, error) {
	f.createCalls = append(f.createCalls, in)
	if f.createErr != nil {
		return nil, false, f.createErr
	}
	if f.nextCreated {
		return &waitlist.Signup{
			ID: "new-id", Role: in.Role, Name: in.Name, Phone: in.Phone, Email: in.Email,
			VehicleType: in.VehicleType, ReferralCode: "ABCD1234", ConsentLaunch: in.ConsentLaunch,
			ConsentMarketing: in.ConsentMarketing,
		}, true, nil
	}
	if f.nextExisting != nil {
		return f.nextExisting, false, nil
	}
	return &waitlist.Signup{ID: "existing-id", Role: in.Role, Phone: in.Phone, ReferralCode: "EXIST999"}, false, nil
}

func (f *fakeRepo) List(_ context.Context, _ waitlist.ListFilter) ([]*waitlist.Signup, int, error) {
	return nil, 0, nil
}

type fakeSMS struct {
	calls int
	err   error
	// done, if non-nil, receives a signal after each SendMessage call — Submit
	// now dispatches notify() in a background goroutine, so tests that need
	// to observe a send must synchronize on it instead of racing the
	// goroutine by reading `calls` immediately after Submit returns.
	done chan struct{}
}

func (f *fakeSMS) SendMessage(_ context.Context, _, _ string) error {
	f.calls++
	if f.done != nil {
		f.done <- struct{}{}
	}
	return f.err
}

// waitForSignal fails the test if sig doesn't fire within a generous bound —
// used to deterministically wait for Submit's detached notify goroutine
// instead of sleeping.
func waitForSignal(t *testing.T, sig <-chan struct{}) {
	t.Helper()
	select {
	case <-sig:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for async notify() to run")
	}
}

type fakeTurnstile struct {
	configured bool
	verified   bool
	err        error
	// calls counts Verify invocations — used to assert that an absent token
	// short-circuits before ever calling out to Cloudflare (fail-open path).
	calls int
}

func (f *fakeTurnstile) Configured() bool { return f.configured }
func (f *fakeTurnstile) Verify(_ context.Context, _, _ string) (bool, error) {
	f.calls++
	return f.verified, f.err
}

type fakeMailer struct {
	configured bool
	calls      int
	err        error
	// done, if non-nil, receives a signal after each Send call — see fakeSMS.done.
	done chan struct{}
}

func (f *fakeMailer) Configured() bool { return f.configured }
func (f *fakeMailer) Send(_ context.Context, _, _, _ string) error {
	f.calls++
	if f.done != nil {
		f.done <- struct{}{}
	}
	return f.err
}

// newService builds a Service wired for non-production (Turnstile-skip)
// behavior — the default for every existing test. Tests exercising the
// production fail-closed path use newServiceEnv directly.
func newService(repo waitlist.Repo, sms *fakeSMS, ts *fakeTurnstile, mailer *fakeMailer) *waitlist.Service {
	return newServiceEnv(repo, sms, ts, mailer, false)
}

func newServiceEnv(repo waitlist.Repo, sms *fakeSMS, ts *fakeTurnstile, mailer *fakeMailer, isProduction bool) *waitlist.Service {
	if repo == nil {
		repo = newFakeRepo()
	}
	if sms == nil {
		sms = &fakeSMS{}
	}
	if ts == nil {
		ts = &fakeTurnstile{configured: false} // skip verification by default
	}
	if mailer == nil {
		mailer = &fakeMailer{configured: false}
	}
	return waitlist.NewService(repo, sms, ts, mailer, zerolog.Nop(), isProduction)
}

func validCustomerInput() waitlist.SubmitInput {
	return waitlist.SubmitInput{
		Role:          waitlist.RoleCustomer,
		Name:          "Aline U.",
		Phone:         "0788123456",
		ConsentLaunch: true,
	}
}

// ── Tests ─────────────────────────────────────────────────────────────────

func TestSubmit_ConsentLaunchFalse_Returns400(t *testing.T) {
	svc := newService(nil, nil, nil, nil)
	in := validCustomerInput()
	in.ConsentLaunch = false

	_, _, err := svc.Submit(context.Background(), in, "203.0.113.1")

	require.Error(t, err)
	var ae *apperrors.AppError
	require.True(t, errors.As(err, &ae))
	assert.Equal(t, "CONSENT_REQUIRED", ae.Code)
	assert.Equal(t, 400, ae.StatusCode)
}

func TestSubmit_DriverWithoutVehicleType_Returns400(t *testing.T) {
	svc := newService(nil, nil, nil, nil)
	in := validCustomerInput()
	in.Role = waitlist.RoleDriver
	in.VehicleType = nil

	_, _, err := svc.Submit(context.Background(), in, "203.0.113.1")

	require.Error(t, err)
	var ae *apperrors.AppError
	require.True(t, errors.As(err, &ae))
	assert.Equal(t, "VEHICLE_TYPE_REQUIRED", ae.Code)
	assert.Equal(t, 400, ae.StatusCode)
}

func TestSubmit_InvalidPhone_Returns400(t *testing.T) {
	svc := newService(nil, nil, nil, nil)
	in := validCustomerInput()
	in.Phone = "not-a-phone"

	_, _, err := svc.Submit(context.Background(), in, "203.0.113.1")

	require.Error(t, err)
	var ae *apperrors.AppError
	require.True(t, errors.As(err, &ae))
	assert.Equal(t, "INVALID_PHONE", ae.Code)
}

func TestSubmit_ValidCustomer_CreatesAndSendsSMS(t *testing.T) {
	repo := newFakeRepo()
	sms := &fakeSMS{done: make(chan struct{}, 1)}
	svc := newService(repo, sms, nil, nil)

	signup, created, err := svc.Submit(context.Background(), validCustomerInput(), "203.0.113.1")

	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, "ABCD1234", signup.ReferralCode)
	require.Len(t, repo.createCalls, 1)
	assert.Equal(t, "+250788123456", repo.createCalls[0].Phone, "phone must be normalized to E.164 before persisting")

	// notify() (SMS + email) now runs in a detached background goroutine so
	// it never blocks Submit's return — wait for it before asserting.
	waitForSignal(t, sms.done)
	assert.Equal(t, 1, sms.calls, "a new signup must trigger exactly one confirmation SMS")
}

// FIX 2: phone is optional (mirrors email) — name + area alone is a valid
// submission, and the absent phone must not trigger an SMS attempt.
func TestSubmit_PhoneAbsent_SucceedsWithoutSendingSMS(t *testing.T) {
	repo := newFakeRepo()
	sms := &fakeSMS{}
	// Give the async notify() goroutine an email to send too, and wait on
	// that — notify() checks SMS first then email in the same goroutine, so
	// by the time the (synchronized-on) email send has happened, the SMS
	// send decision has already been made. Deterministic, no sleep needed.
	mailer := &fakeMailer{configured: true, done: make(chan struct{}, 1)}
	svc := newService(repo, sms, nil, mailer)
	in := validCustomerInput()
	in.Phone = ""
	email := "aline@example.com"
	in.Email = &email

	signup, created, err := svc.Submit(context.Background(), in, "203.0.113.1")

	require.NoError(t, err)
	assert.True(t, created)
	require.NotNil(t, signup)
	require.Len(t, repo.createCalls, 1)
	assert.Empty(t, repo.createCalls[0].Phone, "an absent phone must be persisted as empty/NULL, not rejected")

	waitForSignal(t, mailer.done)
	assert.Equal(t, 0, sms.calls, "an absent phone must never trigger an SMS send")
	assert.Equal(t, 1, mailer.calls, "email confirmation must still be sent when an email was supplied")
}

func TestSubmit_DuplicateRolePhone_IsIdempotentAndDoesNotResendSMS(t *testing.T) {
	repo := newFakeRepo()
	repo.nextCreated = false // simulate (role, phone) already existed
	sms := &fakeSMS{}
	svc := newService(repo, sms, nil, nil)

	signup, created, err := svc.Submit(context.Background(), validCustomerInput(), "203.0.113.1")

	require.NoError(t, err)
	assert.False(t, created, "repeat submission must not be reported as a fresh signup")
	assert.Equal(t, "existing-id", signup.ID)
	assert.Equal(t, 0, sms.calls, "a duplicate submission must never re-send the confirmation SMS")
}

// A PRESENT token that fails verification must still 403 — this is the one
// case fail-open Turnstile actually blocks (a bot that loaded the widget and
// got a bad verdict back).
func TestSubmit_TurnstilePresentTokenRejected_Returns403(t *testing.T) {
	repo := newFakeRepo()
	sms := &fakeSMS{}
	ts := &fakeTurnstile{configured: true, verified: false}
	in := validCustomerInput()
	in.TurnstileToken = "some-token"
	svc := newService(repo, sms, ts, nil)

	_, _, err := svc.Submit(context.Background(), in, "203.0.113.1")

	require.Error(t, err)
	var ae *apperrors.AppError
	require.True(t, errors.As(err, &ae))
	assert.Equal(t, 403, ae.StatusCode)
	assert.Equal(t, 1, ts.calls, "a present token must actually be verified")
	assert.Empty(t, repo.createCalls, "a rejected turnstile check must happen before any DB write")
	assert.Equal(t, 0, sms.calls, "a rejected turnstile check must never trigger an SMS")
}

func TestSubmit_TurnstilePresentTokenVerified_Succeeds(t *testing.T) {
	repo := newFakeRepo()
	ts := &fakeTurnstile{configured: true, verified: true}
	in := validCustomerInput()
	in.TurnstileToken = "some-token"
	svc := newService(repo, nil, ts, nil)

	_, created, err := svc.Submit(context.Background(), in, "203.0.113.1")

	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, 1, ts.calls)
}

// FIX 3 — Turnstile is fail-open: an ABSENT/empty token must never 403,
// regardless of whether a verifier is configured, and Verify must not even be
// called (nothing to check). The remaining backstop is rate limiting, not
// Turnstile.
func TestSubmit_TurnstileTokenAbsent_ConfiguredVerifier_AllowsWithoutCallingVerify(t *testing.T) {
	repo := newFakeRepo()
	ts := &fakeTurnstile{configured: true, verified: false} // would reject if ever called
	in := validCustomerInput()
	in.TurnstileToken = ""
	svc := newService(repo, nil, ts, nil)

	_, created, err := svc.Submit(context.Background(), in, "203.0.113.1")

	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, 0, ts.calls, "an absent token must never reach Cloudflare's verify endpoint")
}

func TestSubmit_TurnstileNotConfigured_SkipsVerification(t *testing.T) {
	repo := newFakeRepo()
	ts := &fakeTurnstile{configured: false}
	svc := newService(repo, nil, ts, nil)

	_, created, err := svc.Submit(context.Background(), validCustomerInput(), "203.0.113.1")

	require.NoError(t, err)
	assert.True(t, created)
	require.Len(t, repo.createCalls, 1)
}

func TestSubmit_EmailPresentAndMailerConfigured_SendsEmail(t *testing.T) {
	repo := newFakeRepo()
	mailer := &fakeMailer{configured: true, done: make(chan struct{}, 1)}
	svc := newService(repo, nil, nil, mailer)
	email := "aline@example.com"
	in := validCustomerInput()
	in.Email = &email

	_, created, err := svc.Submit(context.Background(), in, "203.0.113.1")

	require.NoError(t, err)
	assert.True(t, created)
	waitForSignal(t, mailer.done)
	assert.Equal(t, 1, mailer.calls)
}

func TestSubmit_EmailPresentButMailerNotConfigured_SkipsSilently(t *testing.T) {
	repo := newFakeRepo()
	mailer := &fakeMailer{configured: false}
	svc := newService(repo, nil, nil, mailer)
	email := "aline@example.com"
	in := validCustomerInput()
	in.Email = &email

	_, created, err := svc.Submit(context.Background(), in, "203.0.113.1")

	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, 0, mailer.calls, "an unconfigured mailer must never be called")
}

func TestSubmit_TurnstileTransportError_Returns403NoDBWriteNoSMS(t *testing.T) {
	repo := newFakeRepo()
	sms := &fakeSMS{}
	ts := &fakeTurnstile{configured: true, err: errors.New("dial challenges.cloudflare.com: network unreachable")}
	in := validCustomerInput()
	in.TurnstileToken = "some-token" // a transport error only happens once we attempt to verify a present token
	svc := newService(repo, sms, ts, nil)

	_, _, err := svc.Submit(context.Background(), in, "203.0.113.1")

	require.Error(t, err)
	var ae *apperrors.AppError
	require.True(t, errors.As(err, &ae))
	assert.Equal(t, 403, ae.StatusCode)
	assert.Empty(t, repo.createCalls, "a Turnstile transport failure must happen before any DB write")
	assert.Equal(t, 0, sms.calls, "a Turnstile transport failure must never trigger an SMS")
}

func TestSubmit_SMSSendFails_StillReturnsSuccess(t *testing.T) {
	repo := newFakeRepo()
	sms := &fakeSMS{err: errors.New("pindo: timeout"), done: make(chan struct{}, 1)}
	svc := newService(repo, sms, nil, nil)

	signup, created, err := svc.Submit(context.Background(), validCustomerInput(), "203.0.113.1")

	// The signup already committed to Postgres by the time notify() runs —
	// a flaky SMS gateway is a best-effort side effect, never a request error.
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, "ABCD1234", signup.ReferralCode)

	waitForSignal(t, sms.done)
	assert.Equal(t, 1, sms.calls, "the SMS attempt must actually happen, not just be skipped")
}

// FIX 3: production with no TURNSTILE_SECRET configured used to fail closed
// (403 on every submission). That's now fail-open by deliberate product
// decision — see the Turnstile block in Service.Submit.
func TestSubmit_ProductionWithEmptyTurnstileSecretAndNoToken_AllowsFailOpen(t *testing.T) {
	repo := newFakeRepo()
	sms := &fakeSMS{done: make(chan struct{}, 1)}
	ts := &fakeTurnstile{configured: false} // TURNSTILE_SECRET unset
	svc := newServiceEnv(repo, sms, ts, nil, true /* isProduction */)

	signup, created, err := svc.Submit(context.Background(), validCustomerInput(), "203.0.113.1")

	require.NoError(t, err)
	assert.True(t, created)
	require.NotNil(t, signup)
	require.Len(t, repo.createCalls, 1, "a fail-open submission must still be persisted")
	waitForSignal(t, sms.done)
	assert.Equal(t, 1, sms.calls)
}

func TestSubmit_NonProductionWithEmptyTurnstileSecret_StillSkipsAndSucceeds(t *testing.T) {
	repo := newFakeRepo()
	ts := &fakeTurnstile{configured: false}
	svc := newServiceEnv(repo, nil, ts, nil, false /* isProduction */)

	_, created, err := svc.Submit(context.Background(), validCustomerInput(), "203.0.113.1")

	require.NoError(t, err)
	assert.True(t, created)
	require.Len(t, repo.createCalls, 1)
}

func TestSubmit_DriverWithVehicleType_Succeeds(t *testing.T) {
	repo := newFakeRepo()
	vt := "MOTO_BIKE"
	in := validCustomerInput()
	in.Role = waitlist.RoleDriver
	in.VehicleType = &vt
	svc := newService(repo, nil, nil, nil)

	_, created, err := svc.Submit(context.Background(), in, "203.0.113.1")

	require.NoError(t, err)
	assert.True(t, created)
	require.Len(t, repo.createCalls, 1)
	assert.Equal(t, "MOTO_BIKE", *repo.createCalls[0].VehicleType)
}
