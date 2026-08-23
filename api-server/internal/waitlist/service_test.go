package waitlist_test

import (
	"context"
	"errors"
	"testing"

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
}

func (f *fakeSMS) SendMessage(_ context.Context, _, _ string) error {
	f.calls++
	return f.err
}

type fakeTurnstile struct {
	configured bool
	verified   bool
	err        error
}

func (f *fakeTurnstile) Configured() bool { return f.configured }
func (f *fakeTurnstile) Verify(_ context.Context, _, _ string) (bool, error) {
	return f.verified, f.err
}

type fakeMailer struct {
	configured bool
	calls      int
	err        error
}

func (f *fakeMailer) Configured() bool { return f.configured }
func (f *fakeMailer) Send(_ context.Context, _, _, _ string) error {
	f.calls++
	return f.err
}

func newService(repo waitlist.Repo, sms *fakeSMS, ts *fakeTurnstile, mailer *fakeMailer) *waitlist.Service {
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
	return waitlist.NewService(repo, sms, ts, mailer, zerolog.Nop())
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
	sms := &fakeSMS{}
	svc := newService(repo, sms, nil, nil)

	signup, created, err := svc.Submit(context.Background(), validCustomerInput(), "203.0.113.1")

	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, "ABCD1234", signup.ReferralCode)
	require.Len(t, repo.createCalls, 1)
	assert.Equal(t, "+250788123456", repo.createCalls[0].Phone, "phone must be normalized to E.164 before persisting")
	assert.Equal(t, 1, sms.calls, "a new signup must trigger exactly one confirmation SMS")
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

func TestSubmit_TurnstileConfiguredAndRejected_Returns403(t *testing.T) {
	repo := newFakeRepo()
	sms := &fakeSMS{}
	ts := &fakeTurnstile{configured: true, verified: false}
	svc := newService(repo, sms, ts, nil)

	_, _, err := svc.Submit(context.Background(), validCustomerInput(), "203.0.113.1")

	require.Error(t, err)
	var ae *apperrors.AppError
	require.True(t, errors.As(err, &ae))
	assert.Equal(t, 403, ae.StatusCode)
	assert.Empty(t, repo.createCalls, "a rejected turnstile check must happen before any DB write")
	assert.Equal(t, 0, sms.calls, "a rejected turnstile check must never trigger an SMS")
}

func TestSubmit_TurnstileConfiguredAndVerified_Succeeds(t *testing.T) {
	repo := newFakeRepo()
	ts := &fakeTurnstile{configured: true, verified: true}
	svc := newService(repo, nil, ts, nil)

	_, created, err := svc.Submit(context.Background(), validCustomerInput(), "203.0.113.1")

	require.NoError(t, err)
	assert.True(t, created)
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
	mailer := &fakeMailer{configured: true}
	svc := newService(repo, nil, nil, mailer)
	email := "aline@example.com"
	in := validCustomerInput()
	in.Email = &email

	_, created, err := svc.Submit(context.Background(), in, "203.0.113.1")

	require.NoError(t, err)
	assert.True(t, created)
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
