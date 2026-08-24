package waitlist_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/internal/waitlist"
)

func newTestHandler(repo waitlist.Repo, ts *fakeTurnstile) *waitlist.Handler {
	if repo == nil {
		repo = newFakeRepo()
	}
	if ts == nil {
		ts = &fakeTurnstile{configured: false}
	}
	svc := waitlist.NewService(repo, &fakeSMS{}, ts, &fakeMailer{}, zerolog.Nop(), false)
	return waitlist.NewHandler(svc)
}

func doSubmit(h *waitlist.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/waitlist", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.Submit(rr, req)
	return rr
}

func TestHandlerSubmit_ValidCustomer_Returns200WithReferralCode(t *testing.T) {
	h := newTestHandler(nil, nil)
	rr := doSubmit(h, `{"role":"CUSTOMER","name":"Aline","phone":"0788123456","consent_launch":true}`)

	// Uniform 200 for new vs. duplicate signups (see the dedupe test below) —
	// a 201-vs-200 split would let a caller distinguish new from existing.
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "ABCD1234")
}

// FIX 2: phone is optional — name + area alone must be enough to submit.
func TestHandlerSubmit_NoPhoneNoEmail_Returns200(t *testing.T) {
	h := newTestHandler(nil, nil)
	rr := doSubmit(h, `{"role":"CUSTOMER","name":"Aline","area":"Kimironko","consent_launch":true}`)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "ABCD1234")
}

func TestHandlerSubmit_MissingConsentLaunch_Returns400(t *testing.T) {
	h := newTestHandler(nil, nil)
	rr := doSubmit(h, `{"role":"CUSTOMER","name":"Aline","phone":"0788123456","consent_launch":false}`)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	// The raw go-playground/validator error ("Key: 'submitRequest.ConsentLaunch'
	// Error:Field validation for ...") must never reach a public client.
	assert.NotContains(t, rr.Body.String(), "Field validation")
	assert.Contains(t, rr.Body.String(), "request validation failed")
}

func TestHandlerSubmit_InvalidRole_Returns400(t *testing.T) {
	h := newTestHandler(nil, nil)
	rr := doSubmit(h, `{"role":"ALIEN","name":"Aline","phone":"0788123456","consent_launch":true}`)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandlerSubmit_DriverInvalidVehicleType_Returns400(t *testing.T) {
	h := newTestHandler(nil, nil)
	rr := doSubmit(h, `{"role":"DRIVER","name":"Eric","phone":"0788123456","vehicle_type":"SPACESHIP","consent_launch":true}`)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandlerSubmit_MalformedJSON_Returns400(t *testing.T) {
	h := newTestHandler(nil, nil)
	rr := doSubmit(h, `{not json`)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandlerSubmit_TurnstileRejected_Returns403(t *testing.T) {
	h := newTestHandler(nil, &fakeTurnstile{configured: true, verified: false})
	rr := doSubmit(h, `{"role":"CUSTOMER","name":"Aline","phone":"0788123456","consent_launch":true,"turnstile_token":"bad"}`)

	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestHandlerSubmit_Duplicate_Returns200NotError(t *testing.T) {
	repo := newFakeRepo()
	repo.nextCreated = false
	h := newTestHandler(repo, nil)

	rr := doSubmit(h, `{"role":"CUSTOMER","name":"Aline","phone":"0788123456","consent_launch":true}`)

	assert.Equal(t, http.StatusOK, rr.Code, "a repeat submission must be a success, not an error")
	// The dedupe path must never echo the EXISTING row's referral_code — that
	// would leak (a) that this phone is already on the waitlist and (b)
	// another party's referral code to whoever submits it a second time.
	assert.NotContains(t, rr.Body.String(), "EXIST999", "duplicate path must not leak the existing referral_code")
	assert.NotContains(t, rr.Body.String(), "referral_code")
}

func TestHandlerList_ReturnsSignupsEnvelope(t *testing.T) {
	h := newTestHandler(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/waitlist", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}
