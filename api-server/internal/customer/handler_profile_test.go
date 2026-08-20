package customer_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/workspace/ride-platform/internal/customer"
	"github.com/workspace/ride-platform/internal/middleware"
)

// captureRepo records the ProfileUpdate it receives and serves a stored profile
// back from FindByID, so a test can prove the full PUT→persist→GET round-trip
// for full_name without a database.
type captureRepo struct {
	stored     customer.Profile
	lastUpdate *customer.ProfileUpdate
}

func (r *captureRepo) FindByID(_ context.Context, _ string) (*customer.Profile, error) {
	p := r.stored
	return &p, nil
}

// UpdateProfile mirrors the production COALESCE semantics: a nil field leaves the
// stored value unchanged; a non-nil field overwrites it.
func (r *captureRepo) UpdateProfile(_ context.Context, _ string, u customer.ProfileUpdate) error {
	r.lastUpdate = &u
	if u.FullName != nil {
		r.stored.FullName = u.FullName
	}
	if u.Email != nil {
		r.stored.Email = u.Email
	}
	if u.ProfileImageURL != nil {
		r.stored.ProfileImageURL = u.ProfileImageURL
	}
	if u.Gender != nil {
		r.stored.Gender = u.Gender
	}
	return nil
}

func (r *captureRepo) RideStats(_ context.Context, _ string) (int, float64, error) {
	return 0, 0, nil
}

func newHandler(repo customer.Repo) *customer.Handler {
	return customer.NewHandler(customer.NewService(repo, zerolog.Nop()))
}

func withClaims(r *http.Request, userID string) *http.Request {
	ctx := context.WithValue(r.Context(), middleware.ContextKeyClaims, &middleware.Claims{UserID: userID})
	return r.WithContext(ctx)
}

// PUT /customer/profile must persist an edited full_name (BE-VERIFY-2 / MOB-4).
// This locks in that the handler decodes the flat snake_case key and hands it to
// the repo, and that a subsequent GET returns the edited value.
func TestUpdateProfile_PersistsAndReturnsFullName(t *testing.T) {
	old := "Old Name"
	repo := &captureRepo{stored: customer.Profile{ID: "u1", PhoneNumber: "+250700000000", FullName: &old}}
	h := newHandler(repo)

	// PUT the edited name.
	putReq := withClaims(
		httptest.NewRequest(http.MethodPut, "/api/v1/customer/profile",
			strings.NewReader(`{"full_name":"New Name"}`)),
		"u1",
	)
	putRec := httptest.NewRecorder()
	h.UpdateProfile(putRec, putReq)

	if putRec.Code != http.StatusNoContent {
		t.Fatalf("PUT status = %d, want 204", putRec.Code)
	}
	if repo.lastUpdate == nil || repo.lastUpdate.FullName == nil {
		t.Fatalf("repo did not receive full_name; ProfileUpdate.FullName is nil (name dropped)")
	}
	if *repo.lastUpdate.FullName != "New Name" {
		t.Fatalf("repo got full_name = %q, want %q", *repo.lastUpdate.FullName, "New Name")
	}

	// GET must now return the edited name.
	getReq := withClaims(httptest.NewRequest(http.MethodGet, "/api/v1/customer/profile", nil), "u1")
	getRec := httptest.NewRecorder()
	h.GetProfile(getRec, getReq)

	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", getRec.Code)
	}
	var env struct {
		Data customer.Profile `json:"data"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode GET body: %v", err)
	}
	if env.Data.FullName == nil || *env.Data.FullName != "New Name" {
		t.Fatalf("GET returned full_name = %v, want %q", env.Data.FullName, "New Name")
	}
}

// Omitting full_name from the payload must leave the stored name untouched
// (COALESCE semantics) — proving the edit path is additive, not destructive.
func TestUpdateProfile_OmittedFullNameIsUnchanged(t *testing.T) {
	old := "Keep Me"
	repo := &captureRepo{stored: customer.Profile{ID: "u1", FullName: &old}}
	h := newHandler(repo)

	req := withClaims(
		httptest.NewRequest(http.MethodPut, "/api/v1/customer/profile",
			strings.NewReader(`{"email":"a@b.com"}`)),
		"u1",
	)
	rec := httptest.NewRecorder()
	h.UpdateProfile(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT status = %d, want 204", rec.Code)
	}
	if repo.lastUpdate == nil {
		t.Fatalf("repo did not receive an update")
	}
	if repo.lastUpdate.FullName != nil {
		t.Fatalf("FullName should be nil when omitted, got %q", *repo.lastUpdate.FullName)
	}
	if repo.stored.FullName == nil || *repo.stored.FullName != "Keep Me" {
		t.Fatalf("stored full_name changed to %v, want unchanged %q", repo.stored.FullName, "Keep Me")
	}
}

// Gender (FEAT-onboarding-fields) is OPTIONAL: a rider can set it via PUT
// /customer/profile and a subsequent GET must return it — the same
// PUT→persist→GET round-trip TestUpdateProfile_PersistsAndReturnsFullName
// proves for full_name.
func TestUpdateProfile_PersistsAndReturnsGender(t *testing.T) {
	repo := &captureRepo{stored: customer.Profile{ID: "u1", PhoneNumber: "+250700000000"}}
	h := newHandler(repo)

	putReq := withClaims(
		httptest.NewRequest(http.MethodPut, "/api/v1/customer/profile",
			strings.NewReader(`{"gender":"female"}`)),
		"u1",
	)
	putRec := httptest.NewRecorder()
	h.UpdateProfile(putRec, putReq)

	if putRec.Code != http.StatusNoContent {
		t.Fatalf("PUT status = %d, want 204: %s", putRec.Code, putRec.Body.String())
	}
	if repo.lastUpdate == nil || repo.lastUpdate.Gender == nil {
		t.Fatalf("repo did not receive gender; ProfileUpdate.Gender is nil (dropped)")
	}
	if *repo.lastUpdate.Gender != "female" {
		t.Fatalf("repo got gender = %q, want %q", *repo.lastUpdate.Gender, "female")
	}

	getReq := withClaims(httptest.NewRequest(http.MethodGet, "/api/v1/customer/profile", nil), "u1")
	getRec := httptest.NewRecorder()
	h.GetProfile(getRec, getReq)

	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", getRec.Code)
	}
	var env struct {
		Data customer.Profile `json:"data"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode GET body: %v", err)
	}
	if env.Data.Gender == nil || *env.Data.Gender != "female" {
		t.Fatalf("GET returned gender = %v, want %q", env.Data.Gender, "female")
	}
}

// Gender must stay OPTIONAL/skippable — registration and every other profile
// edit must keep working with no gender ever supplied (never required).
func TestUpdateProfile_OmittedGenderIsUnchanged(t *testing.T) {
	repo := &captureRepo{stored: customer.Profile{ID: "u1"}}
	h := newHandler(repo)

	req := withClaims(
		httptest.NewRequest(http.MethodPut, "/api/v1/customer/profile",
			strings.NewReader(`{"email":"a@b.com"}`)),
		"u1",
	)
	rec := httptest.NewRecorder()
	h.UpdateProfile(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT status = %d, want 204", rec.Code)
	}
	if repo.lastUpdate.Gender != nil {
		t.Fatalf("Gender should be nil when omitted, got %q", *repo.lastUpdate.Gender)
	}
	if repo.stored.Gender != nil {
		t.Fatalf("stored gender should remain nil, got %q", *repo.stored.Gender)
	}
}

// An invalid gender value must be rejected with a 400 before it ever reaches
// the repository — same "reject bad input before any DB write" doctrine as
// the driver/admin national-ID gates.
func TestUpdateProfile_InvalidGenderRejected(t *testing.T) {
	repo := &captureRepo{stored: customer.Profile{ID: "u1"}}
	h := newHandler(repo)

	req := withClaims(
		httptest.NewRequest(http.MethodPut, "/api/v1/customer/profile",
			strings.NewReader(`{"gender":"robot"}`)),
		"u1",
	)
	rec := httptest.NewRecorder()
	h.UpdateProfile(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT status = %d, want 400", rec.Code)
	}
	if repo.lastUpdate != nil {
		t.Fatalf("repo must not be reached for an invalid gender value")
	}
}

// An empty-string gender is treated as "not supplied" (leave unchanged), not
// an error — there is no "clear my gender" UI path, and "" would violate the
// gender column's CHECK constraint (migration 083) if written through.
func TestUpdateProfile_EmptyStringGenderTreatedAsOmitted(t *testing.T) {
	repo := &captureRepo{stored: customer.Profile{ID: "u1"}}
	h := newHandler(repo)

	req := withClaims(
		httptest.NewRequest(http.MethodPut, "/api/v1/customer/profile",
			strings.NewReader(`{"gender":""}`)),
		"u1",
	)
	rec := httptest.NewRecorder()
	h.UpdateProfile(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT status = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if repo.lastUpdate == nil {
		t.Fatalf("repo did not receive an update")
	}
	if repo.lastUpdate.Gender != nil {
		t.Fatalf("empty-string gender must be normalized to nil before reaching the repo, got %q", *repo.lastUpdate.Gender)
	}
}
