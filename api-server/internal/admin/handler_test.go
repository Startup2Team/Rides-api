package admin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/internal/admin"
	mw "github.com/workspace/ride-platform/internal/middleware"
	"github.com/workspace/ride-platform/pkg/audit"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// ── Mock ──────────────────────────────────────────────────────────────────

type mockSvc struct {
	listDriversFn           func(ctx context.Context, status, vehicleType, search, sort string, limit, offset int) ([]map[string]interface{}, int, error)
	driverOverviewFn        func(ctx context.Context, vehicleType string) (map[string]interface{}, error)
	customerOverviewFn      func(ctx context.Context) (map[string]interface{}, error)
	negotiationsStatsFn     func(ctx context.Context) (map[string]interface{}, error)
	createDriverFromAdminFn func(ctx context.Context, in admin.AdminCreateDriverInput) (map[string]interface{}, error)
	forceDriverOfflineFn    func(ctx context.Context, profileID string) error
	liveRidesStatsFn        func(ctx context.Context) (map[string]interface{}, error)
	approveDriverFn         func(ctx context.Context, profileID, adminUserID string) error
	rejectDriverFn          func(ctx context.Context, profileID, adminUserID, reason string) error
	suspendDriverFn         func(ctx context.Context, profileID, adminUserID, reason string, durationHours int) error
	reinstateDriverFn       func(ctx context.Context, profileID string) error
	getDriverFn             func(ctx context.Context, profileID string) (map[string]interface{}, error)
	updateDriverFn          func(ctx context.Context, profileID string, fields map[string]interface{}) error
	deleteDriverFn          func(ctx context.Context, profileID string) error
	listCustomersFn         func(ctx context.Context, status, search, sort string, limit, offset int) ([]map[string]interface{}, int, error)
	getCustomerFn           func(ctx context.Context, userID string) (map[string]interface{}, error)
	suspendUserFn           func(ctx context.Context, userID string, durationHours int) error
	reinstateUserFn         func(ctx context.Context, userID string) error
	updateCustomerFn        func(ctx context.Context, userID, status, notes string) error
	banCustomerFn           func(ctx context.Context, userID, reason string) error
	listRidesFn             func(ctx context.Context, status, transportType, search string, limit, offset int) ([]map[string]interface{}, int, error)
	getRideFn               func(ctx context.Context, rideID string) (map[string]interface{}, error)
	listNegotiationsFn      func(ctx context.Context, status, search string, limit, offset int) ([]map[string]interface{}, int, error)
	getNegotiationFn        func(ctx context.Context, rideID string) (map[string]interface{}, error)
	revenueKPIsFn           func(ctx context.Context, period string) (map[string]interface{}, error)
	listTransactionsFn      func(ctx context.Context, txStatus, sort string, limit, offset int) ([]map[string]interface{}, int, error)
	revenueFn               func(ctx context.Context, period string) (map[string]interface{}, error)
	disbursePayoutsFn       func(ctx context.Context, transactionIDs []string) (int, float64, error)
	gpsAnomaliesFn          func(ctx context.Context, limit int) ([]map[string]interface{}, error)
	deviceCollisionsFn      func(ctx context.Context) ([]map[string]interface{}, error)
	listLiveRidesFn         func(ctx context.Context, status, district, search string, limit, offset int) ([]map[string]interface{}, int, error)
	getLiveRideFn           func(ctx context.Context, rideID string) (map[string]interface{}, error)
	interveneRideFn         func(ctx context.Context, rideID, action, reason string) error
	clearGPSFlagsFn         func(ctx context.Context, profileID string) error
	clearOTPLockoutFn       func(ctx context.Context, userID string) error
	clearDeviceCollisionFn  func(ctx context.Context, userID, deviceID string) error
	getAccountTimelineFn    func(ctx context.Context, userID string, limit int) (map[string]interface{}, error)
	launchReadinessFn       func(ctx context.Context) (map[string]interface{}, error)
	getDriverReferralsFn    func(ctx context.Context, profileID string) ([]map[string]interface{}, error)
}

func (m *mockSvc) ListDrivers(ctx context.Context, status, vehicleType, search, sort string, limit, offset int) ([]map[string]interface{}, int, error) {
	return m.listDriversFn(ctx, status, vehicleType, search, sort, limit, offset)
}
func (m *mockSvc) DriverOverview(ctx context.Context, vehicleType string) (map[string]interface{}, error) {
	return m.driverOverviewFn(ctx, vehicleType)
}
func (m *mockSvc) CustomerOverview(ctx context.Context) (map[string]interface{}, error) {
	if m.customerOverviewFn != nil {
		return m.customerOverviewFn(ctx)
	}
	return map[string]interface{}{}, nil
}
func (m *mockSvc) NegotiationsStats(ctx context.Context) (map[string]interface{}, error) {
	if m.negotiationsStatsFn != nil {
		return m.negotiationsStatsFn(ctx)
	}
	return map[string]interface{}{}, nil
}
func (m *mockSvc) CreateDriverFromAdmin(ctx context.Context, in admin.AdminCreateDriverInput) (map[string]interface{}, error) {
	if m.createDriverFromAdminFn != nil {
		return m.createDriverFromAdminFn(ctx, in)
	}
	return map[string]interface{}{}, nil
}
func (m *mockSvc) ForceDriverOffline(ctx context.Context, profileID string) error {
	if m.forceDriverOfflineFn != nil {
		return m.forceDriverOfflineFn(ctx, profileID)
	}
	return nil
}
func (m *mockSvc) LiveRidesStats(ctx context.Context) (map[string]interface{}, error) {
	if m.liveRidesStatsFn != nil {
		return m.liveRidesStatsFn(ctx)
	}
	return map[string]interface{}{}, nil
}
func (m *mockSvc) ApproveDriver(ctx context.Context, profileID, adminUserID string) error {
	return m.approveDriverFn(ctx, profileID, adminUserID)
}
func (m *mockSvc) RejectDriver(ctx context.Context, profileID, adminUserID, reason string) error {
	return m.rejectDriverFn(ctx, profileID, adminUserID, reason)
}
func (m *mockSvc) RequestDriverMoreInfo(ctx context.Context, profileID, adminUserID, reason string) error {
	return nil
}
func (m *mockSvc) SuspendDriver(ctx context.Context, profileID, adminUserID, reason string, durationHours int) error {
	return m.suspendDriverFn(ctx, profileID, adminUserID, reason, durationHours)
}
func (m *mockSvc) ReinstateDriver(ctx context.Context, profileID string) error {
	return m.reinstateDriverFn(ctx, profileID)
}
func (m *mockSvc) GetDriver(ctx context.Context, profileID string) (map[string]interface{}, error) {
	return m.getDriverFn(ctx, profileID)
}
func (m *mockSvc) UpdateDriver(ctx context.Context, profileID string, fields map[string]interface{}) error {
	return m.updateDriverFn(ctx, profileID, fields)
}
func (m *mockSvc) DeleteDriver(ctx context.Context, profileID string) error {
	return m.deleteDriverFn(ctx, profileID)
}
func (m *mockSvc) ListCustomers(ctx context.Context, status, search, sort string, limit, offset int) ([]map[string]interface{}, int, error) {
	return m.listCustomersFn(ctx, status, search, sort, limit, offset)
}
func (m *mockSvc) GetCustomer(ctx context.Context, userID string) (map[string]interface{}, error) {
	return m.getCustomerFn(ctx, userID)
}
func (m *mockSvc) SuspendUser(ctx context.Context, userID string, durationHours int) error {
	return m.suspendUserFn(ctx, userID, durationHours)
}
func (m *mockSvc) ReinstateUser(ctx context.Context, userID string) error {
	return m.reinstateUserFn(ctx, userID)
}
func (m *mockSvc) UpdateCustomer(ctx context.Context, userID, status, notes string) error {
	return m.updateCustomerFn(ctx, userID, status, notes)
}
func (m *mockSvc) BanCustomer(ctx context.Context, userID, reason string) error {
	return m.banCustomerFn(ctx, userID, reason)
}
func (m *mockSvc) ListRides(ctx context.Context, status, transportType, search string, limit, offset int) ([]map[string]interface{}, int, error) {
	return m.listRidesFn(ctx, status, transportType, search, limit, offset)
}
func (m *mockSvc) GetRide(ctx context.Context, rideID string) (map[string]interface{}, error) {
	return m.getRideFn(ctx, rideID)
}
func (m *mockSvc) ListNegotiations(ctx context.Context, status, search string, limit, offset int) ([]map[string]interface{}, int, error) {
	return m.listNegotiationsFn(ctx, status, search, limit, offset)
}
func (m *mockSvc) GetNegotiation(ctx context.Context, rideID string) (map[string]interface{}, error) {
	return m.getNegotiationFn(ctx, rideID)
}
func (m *mockSvc) RevenueKPIs(ctx context.Context, period string) (map[string]interface{}, error) {
	return m.revenueKPIsFn(ctx, period)
}
func (m *mockSvc) ListTransactions(ctx context.Context, txStatus, sort string, limit, offset int) ([]map[string]interface{}, int, error) {
	return m.listTransactionsFn(ctx, txStatus, sort, limit, offset)
}
func (m *mockSvc) Revenue(ctx context.Context, period string) (map[string]interface{}, error) {
	return m.revenueFn(ctx, period)
}
func (m *mockSvc) DisbursePayouts(ctx context.Context, transactionIDs []string) (int, float64, error) {
	return m.disbursePayoutsFn(ctx, transactionIDs)
}
func (m *mockSvc) GPSAnomalies(ctx context.Context, limit int) ([]map[string]interface{}, error) {
	return m.gpsAnomaliesFn(ctx, limit)
}
func (m *mockSvc) DeviceCollisions(ctx context.Context) ([]map[string]interface{}, error) {
	return m.deviceCollisionsFn(ctx)
}
func (m *mockSvc) ListLiveRides(ctx context.Context, status, district, search string, limit, offset int) ([]map[string]interface{}, int, error) {
	return m.listLiveRidesFn(ctx, status, district, search, limit, offset)
}
func (m *mockSvc) GetLiveRide(ctx context.Context, rideID string) (map[string]interface{}, error) {
	return m.getLiveRideFn(ctx, rideID)
}
func (m *mockSvc) UpsertDriverDocument(ctx context.Context, profileID, documentType, fileURL string) error {
	return nil
}
func (m *mockSvc) LaunchReadiness(ctx context.Context) (map[string]interface{}, error) {
	if m.launchReadinessFn != nil {
		return m.launchReadinessFn(ctx)
	}
	return map[string]interface{}{}, nil
}
func (m *mockSvc) InterveneRide(ctx context.Context, rideID, action, reason string) error {
	return m.interveneRideFn(ctx, rideID, action, reason)
}
func (m *mockSvc) ClearGPSFlags(ctx context.Context, profileID string) error {
	if m.clearGPSFlagsFn != nil {
		return m.clearGPSFlagsFn(ctx, profileID)
	}
	return nil
}
func (m *mockSvc) ClearOTPLockout(ctx context.Context, userID string) error {
	if m.clearOTPLockoutFn != nil {
		return m.clearOTPLockoutFn(ctx, userID)
	}
	return nil
}
func (m *mockSvc) ClearDeviceCollisionFlag(ctx context.Context, userID, deviceID string) error {
	if m.clearDeviceCollisionFn != nil {
		return m.clearDeviceCollisionFn(ctx, userID, deviceID)
	}
	return nil
}
func (m *mockSvc) GetAccountTimeline(ctx context.Context, userID string, limit int) (map[string]interface{}, error) {
	if m.getAccountTimelineFn != nil {
		return m.getAccountTimelineFn(ctx, userID, limit)
	}
	return map[string]interface{}{}, nil
}

func (m *mockSvc) GetDriverReferrals(ctx context.Context, profileID string) ([]map[string]interface{}, error) {
	if m.getDriverReferralsFn != nil {
		return m.getDriverReferralsFn(ctx, profileID)
	}
	return []map[string]interface{}{}, nil
}

// ── Test helpers ──────────────────────────────────────────────────────────

func jsonBody(t *testing.T, v interface{}) *bytes.Buffer {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return bytes.NewBuffer(b)
}

func decodeData(t *testing.T, rr *httptest.ResponseRecorder, target interface{}) {
	t.Helper()
	var env struct {
		Data  json.RawMessage `json:"data"`
		Error interface{}     `json:"error"`
	}
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&env))
	if target != nil && env.Data != nil {
		require.NoError(t, json.Unmarshal(env.Data, target))
	}
}

// injectClaims injects JWT claims directly into the request context,
// bypassing the full JWT+Redis middleware for handler-level tests.
func injectClaims(userID, role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), mw.ContextKeyClaims, &mw.Claims{
				UserID:    userID,
				RoleState: role,
				TokenType: "access",
			})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// newRouter returns a chi router pre-wired with the given handler under the
// admin role guard and admin claims injected.
func newRouter(h *admin.Handler, adminUserID string) *chi.Mux {
	r := chi.NewRouter()
	r.Use(injectClaims(adminUserID, mw.RoleAdmin))
	r.Use(mw.RequireRole(mw.RoleAdmin))

	// Drivers
	r.Get("/admin/drivers", h.ListDrivers)
	r.Get("/admin/drivers/overview", h.DriverOverview)
	r.Get("/admin/drivers/{id}", h.GetDriver)
	r.Post("/admin/drivers/{id}/approve", h.ApproveDriver)
	r.Post("/admin/drivers/{id}/reject", h.RejectDriver)
	r.Post("/admin/drivers/{id}/suspend", h.SuspendDriver)
	r.Post("/admin/drivers/{id}/reinstate", h.ReinstateDriver)
	r.Patch("/admin/drivers/{id}", h.UpdateDriver)
	r.Delete("/admin/drivers/{id}", h.DeleteDriver)
	r.Patch("/admin/drivers/{id}/verify", h.VerifyDriver)
	r.Patch("/admin/drivers/{id}/status", h.UpdateDriverStatus)

	// Customers
	r.Get("/admin/customers", h.ListCustomers)
	r.Get("/admin/customers/{id}", h.GetCustomer)
	r.Post("/admin/users/{id}/suspend", h.SuspendUser)
	r.Post("/admin/customers/{id}/reinstate", h.ReinstateUser)
	r.Patch("/admin/customers/{id}", h.UpdateCustomer)
	r.Patch("/admin/customers/{id}/ban", h.BanCustomer)

	// Users (compat alias)
	r.Get("/admin/users", h.ListUsers)

	// Rides
	r.Get("/admin/rides", h.ListRides)
	r.Get("/admin/rides/{id}", h.GetRide)
	r.Get("/admin/rides/live", h.ListLiveRides)
	r.Get("/admin/rides/live/{id}", h.GetLiveRide)
	r.Post("/admin/rides/live/{id}/intervene", h.InterveneRide)

	// Negotiations
	r.Get("/admin/negotiations", h.ListNegotiations)
	r.Get("/admin/negotiations/{id}", h.GetNegotiation)

	// Revenue
	r.Get("/admin/revenue/kpis", h.RevenueKPIs)
	r.Get("/admin/revenue/transactions", h.ListTransactions)
	r.Get("/admin/revenue", h.Revenue)
	r.Post("/admin/revenue/payouts/disburse", h.DisbursePayouts)

	// Flags
	r.Get("/admin/flags/gps-anomalies", h.GPSAnomalies)
	r.Get("/admin/flags/device-collisions", h.DeviceCollisions)

	return r
}

// noAuthRouter returns a router with RequireRole but NO claims injected,
// so every request gets 401.
func noAuthRouter(h *admin.Handler) *chi.Mux {
	r := chi.NewRouter()
	r.Use(mw.RequireRole(mw.RoleAdmin))
	r.Get("/admin/drivers", h.ListDrivers)
	r.Post("/admin/drivers/{id}/approve", h.ApproveDriver)
	r.Post("/admin/drivers/{id}/reject", h.RejectDriver)
	r.Post("/admin/drivers/{id}/suspend", h.SuspendDriver)
	r.Post("/admin/drivers/{id}/reinstate", h.ReinstateDriver)
	r.Get("/admin/customers", h.ListCustomers)
	r.Get("/admin/rides", h.ListRides)
	r.Get("/admin/revenue/kpis", h.RevenueKPIs)
	return r
}

type dummyDB struct{}

func (d dummyDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func newAdminHandler(svc admin.AdminService, auth admin.AuthService, env string) *admin.Handler {
	return admin.NewHandler(svc, auth, audit.New(dummyDB{}), env)
}

const adminID = "admin-uuid-001"

// ── GROUP G: Admin Drivers ────────────────────────────────────────────────

func TestListDrivers_HappyPath(t *testing.T) {
	mock := &mockSvc{
		listDriversFn: func(_ context.Context, status, vehicleType, search, sort string, limit, offset int) ([]map[string]interface{}, int, error) {
			return []map[string]interface{}{{"id": "d1", "full_name": "Alice"}}, 1, nil
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)

	req := httptest.NewRequest(http.MethodGet, "/admin/drivers", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	var body struct {
		Drivers []map[string]interface{} `json:"drivers"`
		Total   float64                  `json:"total"`
	}
	decodeData(t, rr, &body)
	assert.Len(t, body.Drivers, 1)
	assert.Equal(t, float64(1), body.Total)
}

func TestListDrivers_NoAuth(t *testing.T) {
	r := noAuthRouter(newAdminHandler(&mockSvc{}, nil, "test"))
	req := httptest.NewRequest(http.MethodGet, "/admin/drivers", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestListDrivers_StatusFilter(t *testing.T) {
	var gotStatus string
	mock := &mockSvc{
		listDriversFn: func(_ context.Context, status, _, _, _ string, _, _ int) ([]map[string]interface{}, int, error) {
			gotStatus = status
			return nil, 0, nil
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodGet, "/admin/drivers?status=PENDING_REVIEW", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "PENDING_REVIEW", gotStatus)
}

func TestDriverOverview_HappyPath(t *testing.T) {
	mock := &mockSvc{
		driverOverviewFn: func(_ context.Context, _ string) (map[string]interface{}, error) {
			return map[string]interface{}{"total": 50, "active": 30}, nil
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodGet, "/admin/drivers/overview", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	var body map[string]interface{}
	decodeData(t, rr, &body)
	assert.Equal(t, float64(50), body["total"])
}

func TestGetDriver_HappyPath(t *testing.T) {
	mock := &mockSvc{
		getDriverFn: func(_ context.Context, profileID string) (map[string]interface{}, error) {
			return map[string]interface{}{"id": profileID, "full_name": "Bob"}, nil
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodGet, "/admin/drivers/profile-abc", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestGetDriver_NotFound(t *testing.T) {
	mock := &mockSvc{
		getDriverFn: func(_ context.Context, _ string) (map[string]interface{}, error) {
			return nil, apperrors.ErrNotFound
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodGet, "/admin/drivers/unknown-id", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestApproveDriver_HappyPath(t *testing.T) {
	var gotProfileID, gotAdminID string
	mock := &mockSvc{
		approveDriverFn: func(_ context.Context, profileID, adminUserID string) error {
			gotProfileID = profileID
			gotAdminID = adminUserID
			return nil
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodPost, "/admin/drivers/profile-xyz/approve", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNoContent, rr.Code)
	assert.Equal(t, "profile-xyz", gotProfileID)
	assert.Equal(t, adminID, gotAdminID)
}

func TestApproveDriver_NoAuth(t *testing.T) {
	r := noAuthRouter(newAdminHandler(&mockSvc{}, nil, "test"))
	req := httptest.NewRequest(http.MethodPost, "/admin/drivers/profile-xyz/approve", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestApproveDriver_SelfApprovalForbidden(t *testing.T) {
	mock := &mockSvc{
		approveDriverFn: func(_ context.Context, _, _ string) error {
			return apperrors.ErrSelfApproval
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodPost, "/admin/drivers/own-profile/approve", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestApproveDriver_AlreadyApproved(t *testing.T) {
	mock := &mockSvc{
		approveDriverFn: func(_ context.Context, _, _ string) error {
			return apperrors.ErrConflict
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodPost, "/admin/drivers/profile-xyz/approve", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusConflict, rr.Code)
}

func TestRejectDriver_HappyPath(t *testing.T) {
	var gotReason string
	mock := &mockSvc{
		rejectDriverFn: func(_ context.Context, _, _, reason string) error {
			gotReason = reason
			return nil
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodPost, "/admin/drivers/profile-xyz/reject",
		jsonBody(t, map[string]string{"reason": "incomplete documents"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNoContent, rr.Code)
	assert.Equal(t, "incomplete documents", gotReason)
}

func TestRejectDriver_NoAuth(t *testing.T) {
	r := noAuthRouter(newAdminHandler(&mockSvc{}, nil, "test"))
	req := httptest.NewRequest(http.MethodPost, "/admin/drivers/profile-xyz/reject",
		jsonBody(t, map[string]string{"reason": "bad docs"}))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestSuspendDriver_HappyPath(t *testing.T) {
	var gotHours int
	mock := &mockSvc{
		suspendDriverFn: func(_ context.Context, _, _, _ string, durationHours int) error {
			gotHours = durationHours
			return nil
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodPost, "/admin/drivers/profile-xyz/suspend",
		jsonBody(t, map[string]interface{}{"reason": "fraud", "duration_hours": 48}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNoContent, rr.Code)
	assert.Equal(t, 48, gotHours)
}

func TestSuspendDriver_MissingDuration(t *testing.T) {
	mock := &mockSvc{}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodPost, "/admin/drivers/profile-xyz/suspend",
		jsonBody(t, map[string]interface{}{"reason": "fraud"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestSuspendDriver_NoAuth(t *testing.T) {
	r := noAuthRouter(newAdminHandler(&mockSvc{}, nil, "test"))
	req := httptest.NewRequest(http.MethodPost, "/admin/drivers/profile-xyz/suspend",
		jsonBody(t, map[string]interface{}{"duration_hours": 24}))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestReinstateDriver_HappyPath(t *testing.T) {
	var gotID string
	mock := &mockSvc{
		reinstateDriverFn: func(_ context.Context, profileID string) error {
			gotID = profileID
			return nil
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodPost, "/admin/drivers/profile-xyz/reinstate", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNoContent, rr.Code)
	assert.Equal(t, "profile-xyz", gotID)
}

func TestReinstateDriver_NotFound(t *testing.T) {
	mock := &mockSvc{
		reinstateDriverFn: func(_ context.Context, _ string) error {
			return apperrors.ErrNotFound
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodPost, "/admin/drivers/unknown/reinstate", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestVerifyDriver_Approve(t *testing.T) {
	called := false
	mock := &mockSvc{
		approveDriverFn: func(_ context.Context, _, _ string) error {
			called = true
			return nil
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodPatch, "/admin/drivers/profile-xyz/verify",
		jsonBody(t, map[string]string{"action": "approve"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.True(t, called)
}

func TestVerifyDriver_RejectRequiresReason(t *testing.T) {
	mock := &mockSvc{}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodPatch, "/admin/drivers/profile-xyz/verify",
		jsonBody(t, map[string]string{"action": "reject"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestVerifyDriver_InvalidAction(t *testing.T) {
	mock := &mockSvc{}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodPatch, "/admin/drivers/profile-xyz/verify",
		jsonBody(t, map[string]string{"action": "delete"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestUpdateDriverStatus_Suspend(t *testing.T) {
	called := false
	mock := &mockSvc{
		suspendDriverFn: func(_ context.Context, _, _, _ string, _ int) error {
			called = true
			return nil
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodPatch, "/admin/drivers/profile-xyz/status",
		jsonBody(t, map[string]interface{}{"status": "Suspended", "duration_hours": 24}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.True(t, called)
}

func TestUpdateDriverStatus_Reinstate(t *testing.T) {
	called := false
	mock := &mockSvc{
		reinstateDriverFn: func(_ context.Context, _ string) error {
			called = true
			return nil
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodPatch, "/admin/drivers/profile-xyz/status",
		jsonBody(t, map[string]string{"status": "Active"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.True(t, called)
}

func TestUpdateDriverStatus_InvalidStatus(t *testing.T) {
	mock := &mockSvc{}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodPatch, "/admin/drivers/profile-xyz/status",
		jsonBody(t, map[string]string{"status": "Deleted"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestUpdateDriver_HappyPath(t *testing.T) {
	mock := &mockSvc{
		updateDriverFn: func(_ context.Context, _ string, _ map[string]interface{}) error {
			return nil
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodPatch, "/admin/drivers/profile-xyz",
		jsonBody(t, map[string]interface{}{"vehicle_plate": "RAD 001 A"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNoContent, rr.Code)
}

func TestUpdateDriver_EmptyBody(t *testing.T) {
	mock := &mockSvc{}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodPatch, "/admin/drivers/profile-xyz",
		jsonBody(t, map[string]interface{}{}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestDeleteDriver_HappyPath(t *testing.T) {
	mock := &mockSvc{
		deleteDriverFn: func(_ context.Context, _ string) error { return nil },
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodDelete, "/admin/drivers/profile-xyz", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

// ── GROUP H: Admin Customers ──────────────────────────────────────────────

func TestListCustomers_HappyPath(t *testing.T) {
	mock := &mockSvc{
		listCustomersFn: func(_ context.Context, _, _, _ string, limit, _ int) ([]map[string]interface{}, int, error) {
			return []map[string]interface{}{{"id": "c1"}, {"id": "c2"}}, 2, nil
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodGet, "/admin/customers", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	var body struct {
		Customers []map[string]interface{} `json:"customers"`
	}
	decodeData(t, rr, &body)
	assert.Len(t, body.Customers, 2)
}

func TestListCustomers_NoAuth(t *testing.T) {
	r := noAuthRouter(newAdminHandler(&mockSvc{}, nil, "test"))
	req := httptest.NewRequest(http.MethodGet, "/admin/customers", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestGetCustomer_HappyPath(t *testing.T) {
	mock := &mockSvc{
		getCustomerFn: func(_ context.Context, userID string) (map[string]interface{}, error) {
			return map[string]interface{}{"id": userID, "full_name": "Test User"}, nil
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodGet, "/admin/customers/user-abc", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestGetCustomer_NotFound(t *testing.T) {
	mock := &mockSvc{
		getCustomerFn: func(_ context.Context, _ string) (map[string]interface{}, error) {
			return nil, apperrors.ErrNotFound
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodGet, "/admin/customers/unknown", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestSuspendUser_HappyPath(t *testing.T) {
	var gotHours int
	mock := &mockSvc{
		suspendUserFn: func(_ context.Context, _ string, durationHours int) error {
			gotHours = durationHours
			return nil
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodPost, "/admin/users/user-abc/suspend",
		jsonBody(t, map[string]int{"duration_hours": 72}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNoContent, rr.Code)
	assert.Equal(t, 72, gotHours)
}

func TestSuspendUser_MissingDuration(t *testing.T) {
	mock := &mockSvc{}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodPost, "/admin/users/user-abc/suspend",
		jsonBody(t, map[string]string{"reason": "spam"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestReinstateUser_HappyPath(t *testing.T) {
	called := false
	mock := &mockSvc{
		reinstateUserFn: func(_ context.Context, _ string) error {
			called = true
			return nil
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodPost, "/admin/customers/user-abc/reinstate", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNoContent, rr.Code)
	assert.True(t, called)
}

func TestBanCustomer_HappyPath(t *testing.T) {
	mock := &mockSvc{
		banCustomerFn: func(_ context.Context, _, _ string) error { return nil },
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodPatch, "/admin/customers/user-abc/ban",
		jsonBody(t, map[string]string{"reason": "repeated fraud"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestBanCustomer_MissingReason(t *testing.T) {
	mock := &mockSvc{}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodPatch, "/admin/customers/user-abc/ban",
		jsonBody(t, map[string]string{}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// ── GROUP I: Admin Rides & Flags ──────────────────────────────────────────

func TestListRides_HappyPath(t *testing.T) {
	mock := &mockSvc{
		listRidesFn: func(_ context.Context, _, _, _ string, _, _ int) ([]map[string]interface{}, int, error) {
			return []map[string]interface{}{{"id": "r1"}, {"id": "r2"}, {"id": "r3"}}, 3, nil
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodGet, "/admin/rides", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	var body struct {
		Rides []map[string]interface{} `json:"rides"`
	}
	decodeData(t, rr, &body)
	assert.Len(t, body.Rides, 3)
}

func TestListRides_NoAuth(t *testing.T) {
	r := noAuthRouter(newAdminHandler(&mockSvc{}, nil, "test"))
	req := httptest.NewRequest(http.MethodGet, "/admin/rides", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestGetRide_HappyPath(t *testing.T) {
	mock := &mockSvc{
		getRideFn: func(_ context.Context, rideID string) (map[string]interface{}, error) {
			return map[string]interface{}{"id": rideID, "status": "COMPLETED"}, nil
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodGet, "/admin/rides/ride-abc", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestGetRide_NotFound(t *testing.T) {
	mock := &mockSvc{
		getRideFn: func(_ context.Context, _ string) (map[string]interface{}, error) {
			return nil, apperrors.ErrNotFound
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodGet, "/admin/rides/unknown", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestListLiveRides_HappyPath(t *testing.T) {
	mock := &mockSvc{
		listLiveRidesFn: func(_ context.Context, _, _, _ string, _, _ int) ([]map[string]interface{}, int, error) {
			return []map[string]interface{}{{"id": "lr1", "status": "IN_PROGRESS"}}, 1, nil
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodGet, "/admin/rides/live", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestInterveneRide_HappyPath(t *testing.T) {
	var gotAction string
	mock := &mockSvc{
		interveneRideFn: func(_ context.Context, _, action, _ string) error {
			gotAction = action
			return nil
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodPost, "/admin/rides/live/ride-abc/intervene",
		jsonBody(t, map[string]string{"action": "cancel", "reason": "admin intervention"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "cancel", gotAction)
}

func TestInterveneRide_MissingAction(t *testing.T) {
	mock := &mockSvc{}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodPost, "/admin/rides/live/ride-abc/intervene",
		jsonBody(t, map[string]string{"reason": "intervention"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestInterveneRide_CompletedRide(t *testing.T) {
	mock := &mockSvc{
		interveneRideFn: func(_ context.Context, _, _, _ string) error {
			return apperrors.ErrInvalidTransition
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodPost, "/admin/rides/live/ride-abc/intervene",
		jsonBody(t, map[string]string{"action": "cancel", "reason": "test"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusConflict, rr.Code)
}

func TestGPSAnomalies_HappyPath(t *testing.T) {
	mock := &mockSvc{
		gpsAnomaliesFn: func(_ context.Context, limit int) ([]map[string]interface{}, error) {
			return []map[string]interface{}{{"driver_id": "d1", "computed_speed_kmh": 250}}, nil
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodGet, "/admin/flags/gps-anomalies", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestDeviceCollisions_HappyPath(t *testing.T) {
	mock := &mockSvc{
		deviceCollisionsFn: func(_ context.Context) ([]map[string]interface{}, error) {
			return []map[string]interface{}{{"device_id": "dev1", "user_count": 3}}, nil
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodGet, "/admin/flags/device-collisions", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

// ── Negotiations ──────────────────────────────────────────────────────────

func TestListNegotiations_HappyPath(t *testing.T) {
	mock := &mockSvc{
		listNegotiationsFn: func(_ context.Context, _, _ string, _, _ int) ([]map[string]interface{}, int, error) {
			return []map[string]interface{}{{"ride_id": "r1", "status": "Agreed"}}, 1, nil
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodGet, "/admin/negotiations", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestGetNegotiation_HappyPath(t *testing.T) {
	mock := &mockSvc{
		getNegotiationFn: func(_ context.Context, rideID string) (map[string]interface{}, error) {
			return map[string]interface{}{"ride_id": rideID, "rounds": 2}, nil
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodGet, "/admin/negotiations/ride-abc", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

// ── Revenue ───────────────────────────────────────────────────────────────

func TestRevenueKPIs_HappyPath(t *testing.T) {
	mock := &mockSvc{
		revenueKPIsFn: func(_ context.Context, period string) (map[string]interface{}, error) {
			return map[string]interface{}{"total_revenue_rwf": 500000, "period": period}, nil
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodGet, "/admin/revenue/kpis?period=week", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	var body map[string]interface{}
	decodeData(t, rr, &body)
	assert.Equal(t, float64(500000), body["total_revenue_rwf"])
}

func TestRevenueKPIs_NoAuth(t *testing.T) {
	r := noAuthRouter(newAdminHandler(&mockSvc{}, nil, "test"))
	req := httptest.NewRequest(http.MethodGet, "/admin/revenue/kpis", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestRevenueKPIs_DefaultPeriod(t *testing.T) {
	var gotPeriod string
	mock := &mockSvc{
		revenueKPIsFn: func(_ context.Context, period string) (map[string]interface{}, error) {
			gotPeriod = period
			return map[string]interface{}{}, nil
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodGet, "/admin/revenue/kpis", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "today", gotPeriod)
}

func TestListTransactions_HappyPath(t *testing.T) {
	mock := &mockSvc{
		listTransactionsFn: func(_ context.Context, _, _ string, _, _ int) ([]map[string]interface{}, int, error) {
			return []map[string]interface{}{{"id": "tx1", "amount_rwf": 3000}}, 1, nil
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodGet, "/admin/revenue/transactions", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestRevenue_DefaultPeriod(t *testing.T) {
	var gotPeriod string
	mock := &mockSvc{
		revenueFn: func(_ context.Context, period string) (map[string]interface{}, error) {
			gotPeriod = period
			return map[string]interface{}{}, nil
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodGet, "/admin/revenue", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "month", gotPeriod)
}

func TestDisbursePayouts_HappyPath(t *testing.T) {
	mock := &mockSvc{
		disbursePayoutsFn: func(_ context.Context, ids []string) (int, float64, error) {
			return len(ids), 12000, nil
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodPost, "/admin/revenue/payouts/disburse",
		jsonBody(t, map[string]interface{}{"transactionIds": []string{"tx1", "tx2"}}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	var body map[string]interface{}
	decodeData(t, rr, &body)
	assert.Equal(t, float64(2), body["disbursed"])
}

func TestDisbursePayouts_EmptyList(t *testing.T) {
	mock := &mockSvc{}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodPost, "/admin/revenue/payouts/disburse",
		jsonBody(t, map[string]interface{}{"transactionIds": []string{}}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// ── Additional coverage: 0% and low-coverage handlers ────────────────────

func TestListUsers_DelegatesToListCustomers(t *testing.T) {
	called := false
	mock := &mockSvc{
		listCustomersFn: func(_ context.Context, _, _, _ string, _, _ int) ([]map[string]interface{}, int, error) {
			called = true
			return []map[string]interface{}{{"id": "u1"}}, 1, nil
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.True(t, called)
}

func TestUpdateCustomer_HappyPath(t *testing.T) {
	var gotStatus string
	mock := &mockSvc{
		updateCustomerFn: func(_ context.Context, _, status, _ string) error {
			gotStatus = status
			return nil
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodPatch, "/admin/customers/user-abc",
		jsonBody(t, map[string]string{"status": "Active", "notes": "reviewed"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNoContent, rr.Code)
	assert.Equal(t, "Active", gotStatus)
}

func TestUpdateCustomer_ServiceError(t *testing.T) {
	mock := &mockSvc{
		updateCustomerFn: func(_ context.Context, _, _, _ string) error {
			return apperrors.ErrNotFound
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodPatch, "/admin/customers/unknown",
		jsonBody(t, map[string]string{"status": "Active"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestGetLiveRide_HappyPath(t *testing.T) {
	mock := &mockSvc{
		getLiveRideFn: func(_ context.Context, rideID string) (map[string]interface{}, error) {
			return map[string]interface{}{"id": rideID, "status": "IN_PROGRESS"}, nil
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodGet, "/admin/rides/live/ride-abc", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	var body map[string]interface{}
	decodeData(t, rr, &body)
	assert.Equal(t, "IN_PROGRESS", body["status"])
}

func TestGetLiveRide_NotFound(t *testing.T) {
	mock := &mockSvc{
		getLiveRideFn: func(_ context.Context, _ string) (map[string]interface{}, error) {
			return nil, apperrors.ErrNotFound
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodGet, "/admin/rides/live/unknown", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestDriverOverview_ServiceError(t *testing.T) {
	mock := &mockSvc{
		driverOverviewFn: func(_ context.Context, _ string) (map[string]interface{}, error) {
			return nil, apperrors.ErrInternal
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodGet, "/admin/drivers/overview", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestGPSAnomalies_ServiceError(t *testing.T) {
	mock := &mockSvc{
		gpsAnomaliesFn: func(_ context.Context, _ int) ([]map[string]interface{}, error) {
			return nil, apperrors.ErrInternal
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodGet, "/admin/flags/gps-anomalies", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestDeviceCollisions_ServiceError(t *testing.T) {
	mock := &mockSvc{
		deviceCollisionsFn: func(_ context.Context) ([]map[string]interface{}, error) {
			return nil, apperrors.ErrInternal
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodGet, "/admin/flags/device-collisions", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestDeleteDriver_ServiceError(t *testing.T) {
	mock := &mockSvc{
		deleteDriverFn: func(_ context.Context, _ string) error {
			return apperrors.ErrNotFound
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodDelete, "/admin/drivers/unknown", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestReinstateUser_ServiceError(t *testing.T) {
	mock := &mockSvc{
		reinstateUserFn: func(_ context.Context, _ string) error {
			return apperrors.ErrNotFound
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodPost, "/admin/customers/unknown/reinstate", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestGetNegotiation_NotFound(t *testing.T) {
	mock := &mockSvc{
		getNegotiationFn: func(_ context.Context, _ string) (map[string]interface{}, error) {
			return nil, apperrors.ErrNotFound
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodGet, "/admin/negotiations/unknown", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestVerifyDriver_RejectWithReason(t *testing.T) {
	called := false
	mock := &mockSvc{
		rejectDriverFn: func(_ context.Context, _, _, reason string) error {
			called = true
			assert.Equal(t, "missing license", reason)
			return nil
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodPatch, "/admin/drivers/profile-xyz/verify",
		jsonBody(t, map[string]string{"action": "reject", "reason": "missing license"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.True(t, called)
}

func TestVerifyDriver_MissingAction(t *testing.T) {
	mock := &mockSvc{}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodPatch, "/admin/drivers/profile-xyz/verify",
		jsonBody(t, map[string]string{}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestPaginate_CustomLimitOffset(t *testing.T) {
	var gotLimit, gotOffset int
	mock := &mockSvc{
		listDriversFn: func(_ context.Context, _, _, _, _ string, limit, offset int) ([]map[string]interface{}, int, error) {
			gotLimit = limit
			gotOffset = offset
			return nil, 0, nil
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodGet, "/admin/drivers?limit=10&offset=20", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, 10, gotLimit)
	assert.Equal(t, 20, gotOffset)
}

func TestRevenueKPIs_ServiceError(t *testing.T) {
	mock := &mockSvc{
		revenueKPIsFn: func(_ context.Context, _ string) (map[string]interface{}, error) {
			return nil, apperrors.ErrInternal
		},
	}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodGet, "/admin/revenue/kpis", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestUpdateDriverStatus_MissingStatus(t *testing.T) {
	mock := &mockSvc{}
	r := newRouter(newAdminHandler(mock, nil, "test"), adminID)
	req := httptest.NewRequest(http.MethodPatch, "/admin/drivers/profile-xyz/status",
		jsonBody(t, map[string]string{}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}
