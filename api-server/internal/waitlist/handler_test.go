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
	svc := waitlist.NewService(repo, &fakeSMS{}, ts, &fakeMailer{}, zerolog.Nop())
	return waitlist.NewHandler(svc)
}

func doSubmit(h *waitlist.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/waitlist", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.Submit(rr, req)
	return rr
}

func TestHandlerSubmit_ValidCustomer_Returns201WithReferralCode(t *testing.T) {
	h := newTestHandler(nil, nil)
	rr := doSubmit(h, `{"role":"CUSTOMER","name":"Aline","phone":"0788123456","consent_launch":true}`)

	require.Equal(t, http.StatusCreated, rr.Code)
	assert.Contains(t, rr.Body.String(), "ABCD1234")
}

func TestHandlerSubmit_MissingConsentLaunch_Returns400(t *testing.T) {
	h := newTestHandler(nil, nil)
	rr := doSubmit(h, `{"role":"CUSTOMER","name":"Aline","phone":"0788123456","consent_launch":false}`)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
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
}

func TestHandlerList_ReturnsSignupsEnvelope(t *testing.T) {
	h := newTestHandler(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/waitlist", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}
