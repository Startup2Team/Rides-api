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
