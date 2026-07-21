package team_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mw "github.com/workspace/ride-platform/internal/middleware"
	"github.com/workspace/ride-platform/internal/team"
	"github.com/workspace/ride-platform/pkg/audit"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// ── Mock ──────────────────────────────────────────────────────────────────

type mockSvc struct {
	loginFn            func(ctx context.Context, email, password string) (*team.LoginResult, error)
	verify2FAFn        func(ctx context.Context, preAuthToken, code string) (*team.LoginResult, error)
	reissue2FAFn       func(ctx context.Context, adminID string) (string, error)
	verifyBackupFn     func(ctx context.Context, preAuthToken, backupCode string) (*team.LoginResult, error)
	logoutFn           func(ctx context.Context, adminID, jti string) error
	setup2FAFn         func(ctx context.Context, adminID string) (string, string, error)
	enable2FAFn        func(ctx context.Context, adminID, secret, code string) ([]string, error)
	disable2FAFn       func(ctx context.Context, adminID, password string) error
	resetTOTPFn        func(ctx context.Context, adminID, currentCode string) (string, string, []string, error)
	resetTOTPPreAuthFn func(ctx context.Context, preAuthToken, currentCode string) (string, string, []string, error)
	listAdminsFn       func(ctx context.Context, status, roleID, search string) ([]*team.AdminAccount, error)
	inviteFn           func(ctx context.Context, name, email, roleID, password string) (*team.AdminAccount, error)
	listRolesFn        func(ctx context.Context) ([]*team.Role, error)
	createRoleFn       func(ctx context.Context, name, description string, permissions interface{}) (*team.Role, error)
	updateRoleByIDFn   func(ctx context.Context, roleID, name, description string, permissions interface{}) (*team.Role, error)
	deleteRoleByIDFn   func(ctx context.Context, roleID string) error
	updateRoleFn       func(ctx context.Context, id, roleID string) error
	suspendFn          func(ctx context.Context, id string) error
	reinstateFn        func(ctx context.Context, id string) error
	removeFn           func(ctx context.Context, id string) error
	updateNameFn       func(ctx context.Context, id, name string) error
	changePasswordFn   func(ctx context.Context, id, current, newPw string) error
	setPasswordFn      func(ctx context.Context, id, password string) error
	listAuditLogFn     func(ctx context.Context, actor, action, targetType, from, to string, limit, offset int) ([]team.AuditEntry, int, error)
	forgotPasswordFn   func(ctx context.Context, email string) error
	verifyResetOTPFn   func(ctx context.Context, email, otp string) (string, error)
	resetPasswordFn    func(ctx context.Context, resetToken, newPassword string) error
}

func (m *mockSvc) Login(ctx context.Context, email, password string) (*team.LoginResult, error) {
	return m.loginFn(ctx, email, password)
}
func (m *mockSvc) Verify2FA(ctx context.Context, pre, code string) (*team.LoginResult, error) {
	return m.verify2FAFn(ctx, pre, code)
}
func (m *mockSvc) Reissue2FAChallenge(ctx context.Context, adminID string) (string, error) {
	if m.reissue2FAFn != nil {
		return m.reissue2FAFn(ctx, adminID)
	}
	return "", nil
}
func (m *mockSvc) VerifyBackupCode(ctx context.Context, pre, code string) (*team.LoginResult, error) {
	return m.verifyBackupFn(ctx, pre, code)
}
func (m *mockSvc) Logout(ctx context.Context, adminID, jti string) error {
	if m.logoutFn != nil {
		return m.logoutFn(ctx, adminID, jti)
	}
	return nil
}
func (m *mockSvc) Generate2FASetup(ctx context.Context, adminID string) (string, string, error) {
	return m.setup2FAFn(ctx, adminID)
}
func (m *mockSvc) Enable2FA(ctx context.Context, adminID, secret, code string) ([]string, error) {
	return m.enable2FAFn(ctx, adminID, secret, code)
}
func (m *mockSvc) Disable2FA(ctx context.Context, adminID, password string) error {
	return m.disable2FAFn(ctx, adminID, password)
}
func (m *mockSvc) ResetTOTP(ctx context.Context, adminID, code string) (string, string, []string, error) {
	if m.resetTOTPFn != nil {
		return m.resetTOTPFn(ctx, adminID, code)
	}
	return "", "", nil, apperrors.New(http.StatusInternalServerError, "INTERNAL", "reset not configured")
}
func (m *mockSvc) ResetTOTPFromPreAuth(ctx context.Context, preAuthToken, code string) (string, string, []string, error) {
	if m.resetTOTPPreAuthFn != nil {
		return m.resetTOTPPreAuthFn(ctx, preAuthToken, code)
	}
	return "", "", nil, nil
}
func (m *mockSvc) ListAdmins(ctx context.Context, status, roleID, search string) ([]*team.AdminAccount, error) {
	return m.listAdminsFn(ctx, status, roleID, search)
}
func (m *mockSvc) Invite(ctx context.Context, name, email, roleID, password string) (*team.AdminAccount, error) {
	return m.inviteFn(ctx, name, email, roleID, password)
}
func (m *mockSvc) ListRoles(ctx context.Context) ([]*team.Role, error) {
	return m.listRolesFn(ctx)
}
func (m *mockSvc) CreateRole(ctx context.Context, name, description string, permissions interface{}) (*team.Role, error) {
	return m.createRoleFn(ctx, name, description, permissions)
}
func (m *mockSvc) UpdateRoleByID(ctx context.Context, roleID, name, description string, permissions interface{}) (*team.Role, error) {
	return m.updateRoleByIDFn(ctx, roleID, name, description, permissions)
}
func (m *mockSvc) DeleteRoleByID(ctx context.Context, roleID string) error {
	return m.deleteRoleByIDFn(ctx, roleID)
}
func (m *mockSvc) UpdateRolePermissions(ctx context.Context, roleID string, permissions interface{}) error {
	return nil
}
func (m *mockSvc) UpdateRole(ctx context.Context, id, roleID string) error {
	return m.updateRoleFn(ctx, id, roleID)
}
func (m *mockSvc) Suspend(ctx context.Context, id string) error {
	return m.suspendFn(ctx, id)
}
func (m *mockSvc) Reinstate(ctx context.Context, id string) error {
	return m.reinstateFn(ctx, id)
}
func (m *mockSvc) Remove(ctx context.Context, id string) error {
	return m.removeFn(ctx, id)
}
func (m *mockSvc) ResendInvite(ctx context.Context, id string) error                  { return nil }
func (m *mockSvc) ResetMember2FA(ctx context.Context, actorID, memberID string) error { return nil }
func (m *mockSvc) GetMemberActivity(ctx context.Context, adminID string, limit int) ([]team.AuditEntry, error) {
	return nil, nil
}
func (m *mockSvc) UpdateName(ctx context.Context, id, name string) error {
	return m.updateNameFn(ctx, id, name)
}
func (m *mockSvc) UpdateProfile(ctx context.Context, id, name, phone, photoURL string) error {
	return nil
}
func (m *mockSvc) ChangePassword(ctx context.Context, id, current, newPw string) error {
	return m.changePasswordFn(ctx, id, current, newPw)
}
func (m *mockSvc) SetPassword(ctx context.Context, id, password string) error {
	return m.setPasswordFn(ctx, id, password)
}
func (m *mockSvc) SendWelcomeEmail(ctx context.Context, id, tempPassword, loginURL string) error {
	return nil
}
func (m *mockSvc) ListAuditLog(ctx context.Context, actor, action, targetType, from, to string, limit, offset int) ([]team.AuditEntry, int, error) {
	if m.listAuditLogFn != nil {
		return m.listAuditLogFn(ctx, actor, action, targetType, from, to, limit, offset)
	}
	return nil, 0, nil
}
func (m *mockSvc) ForgotPassword(ctx context.Context, email string) error {
	if m.forgotPasswordFn != nil {
		return m.forgotPasswordFn(ctx, email)
	}
	return nil
}
func (m *mockSvc) VerifyResetOTP(ctx context.Context, email, otp string) (string, error) {
	if m.verifyResetOTPFn != nil {
		return m.verifyResetOTPFn(ctx, email, otp)
	}
	return "test-reset-token", nil
}
func (m *mockSvc) ResetPassword(ctx context.Context, resetToken, newPassword string) error {
	if m.resetPasswordFn != nil {
		return m.resetPasswordFn(ctx, resetToken, newPassword)
	}
	return nil
}

type dummyDB struct{}

func (d dummyDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func newHandler(svc team.TeamService) *team.Handler {
	return team.NewHandler(svc, audit.New(dummyDB{}))
}

// ── Helpers ───────────────────────────────────────────────────────────────

func jsonBody(t *testing.T, v interface{}) *bytes.Buffer {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return bytes.NewBuffer(b)
}

func decodeData(t *testing.T, rr *httptest.ResponseRecorder, target interface{}) {
	t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&env))
	if target != nil && env.Data != nil {
		require.NoError(t, json.Unmarshal(env.Data, target))
	}
}

func injectClaims(userID, role, jti string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			import_claims := &mw.Claims{UserID: userID, RoleState: role, TokenType: "access"}
			import_claims.ID = jti
			ctx := r.Context()
			ctx = context.WithValue(ctx, mw.ContextKeyClaims, import_claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func newRouter(h *team.Handler, adminID string) *chi.Mux {
	r := chi.NewRouter()
	r.Use(injectClaims(adminID, mw.RoleAdmin, "test-jti"))
	r.Use(mw.RequireRole(mw.RoleAdmin))

	// Auth
	r.Post("/admin/auth/login", h.Login)
	r.Post("/admin/auth/2fa/verify", h.Verify2FA)
	r.Post("/admin/auth/2fa/backup", h.VerifyBackupCode)
	r.Post("/admin/auth/logout", h.Logout)
	r.Post("/admin/auth/totp/reset", h.ResetTOTP)

	// 2FA setup
	r.Get("/admin/account/2fa/setup", h.Setup2FA)
	r.Post("/admin/account/2fa/enable", h.Enable2FA)
	r.Post("/admin/account/2fa/disable", h.Disable2FA)

	// Account
	r.Get("/admin/account", h.GetAccount)
	r.Put("/admin/account", h.UpdateAccount)
	r.Post("/admin/account/password", h.ChangePassword)

	// Team
	r.Get("/admin/team", h.List)
	r.Post("/admin/team/invite", h.Invite)
	r.Get("/admin/team/roles", h.ListRoles)
	r.Post("/admin/team/roles", h.CreateRole)
	r.Patch("/admin/team/roles/{roleId}", h.UpdateRoleByID)
	r.Delete("/admin/team/roles/{roleId}", h.DeleteRoleByID)
	r.Post("/admin/team/members/{id}/role", h.UpdateRole)
	r.Post("/admin/team/members/{id}/suspend", h.Suspend)
	r.Post("/admin/team/members/{id}/reinstate", h.Reinstate)
	r.Post("/admin/team/members/{id}/remove", h.Remove)
	r.Post("/admin/team/members/{id}/set-password", h.SetPassword)

	// Account extras
	r.Get("/admin/account/sessions", h.GetSessions)
	r.Delete("/admin/account/sessions/{sessionId}", h.RevokeSession)
	return r
}

// noAuthRouter has RequireRole but no claims injected → every request gets 401
func noAuthRouter(h *team.Handler) *chi.Mux {
	r := chi.NewRouter()
	r.Use(mw.RequireRole(mw.RoleAdmin))
	r.Post("/admin/auth/2fa/verify", h.Verify2FA)
	r.Get("/admin/account/2fa/setup", h.Setup2FA)
	r.Get("/admin/team", h.List)
	return r
}

const adminID = "admin-uuid-001"

// ── GROUP F: Admin Auth ───────────────────────────────────────────────────

func TestLogin_HappyPath_No2FA(t *testing.T) {
	mock := &mockSvc{
		loginFn: func(_ context.Context, email, _ string) (*team.LoginResult, error) {
			return &team.LoginResult{AccessToken: "tok-abc", TwoFactorRequired: false}, nil
		},
	}
	r := chi.NewRouter()
	r.Post("/admin/auth/login", newHandler(mock).Login)

	req := httptest.NewRequest(http.MethodPost, "/admin/auth/login",
		jsonBody(t, map[string]string{"email": "admin@test.com", "password": "secret123"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	var body team.LoginResult
	decodeData(t, rr, &body)
	assert.Equal(t, "tok-abc", body.AccessToken)
}

func TestLogin_HappyPath_Requires2FA(t *testing.T) {
	mock := &mockSvc{
		loginFn: func(_ context.Context, _, _ string) (*team.LoginResult, error) {
			return &team.LoginResult{PreAuthToken: "pre-tok", TwoFactorRequired: true}, nil
		},
	}
	r := chi.NewRouter()
	r.Post("/admin/auth/login", newHandler(mock).Login)

	req := httptest.NewRequest(http.MethodPost, "/admin/auth/login",
		jsonBody(t, map[string]string{"email": "admin@test.com", "password": "secret123"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	var body team.LoginResult
	decodeData(t, rr, &body)
	assert.True(t, body.TwoFactorRequired)
	assert.Equal(t, "pre-tok", body.PreAuthToken)
}

func TestLogin_WrongPassword(t *testing.T) {
	mock := &mockSvc{
		loginFn: func(_ context.Context, _, _ string) (*team.LoginResult, error) {
			return nil, apperrors.New(http.StatusUnauthorized, "INVALID_CREDENTIALS", "invalid email or password")
		},
	}
	r := chi.NewRouter()
	r.Post("/admin/auth/login", newHandler(mock).Login)

	req := httptest.NewRequest(http.MethodPost, "/admin/auth/login",
		jsonBody(t, map[string]string{"email": "admin@test.com", "password": "wrong"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestLogin_UnknownEmail(t *testing.T) {
	mock := &mockSvc{
		loginFn: func(_ context.Context, _, _ string) (*team.LoginResult, error) {
			return nil, apperrors.New(http.StatusUnauthorized, "INVALID_CREDENTIALS", "invalid email or password")
		},
	}
	r := chi.NewRouter()
	r.Post("/admin/auth/login", newHandler(mock).Login)

	req := httptest.NewRequest(http.MethodPost, "/admin/auth/login",
		jsonBody(t, map[string]string{"email": "nobody@test.com", "password": "pass"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestLogin_MissingFields(t *testing.T) {
	r := chi.NewRouter()
	r.Post("/admin/auth/login", newHandler(&mockSvc{}).Login)

	req := httptest.NewRequest(http.MethodPost, "/admin/auth/login",
		jsonBody(t, map[string]string{"email": "admin@test.com"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// ── Verify2FA ─────────────────────────────────────────────────────────────

func TestVerify2FA_ValidCode(t *testing.T) {
	mock := &mockSvc{
		verify2FAFn: func(_ context.Context, _, _ string) (*team.LoginResult, error) {
			return &team.LoginResult{AccessToken: "admin-jwt"}, nil
		},
	}
	r := newRouter(newHandler(mock), adminID)

	req := httptest.NewRequest(http.MethodPost, "/admin/auth/2fa/verify",
		jsonBody(t, map[string]string{"pre_auth_token": "pre-tok", "code": "123456"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	var body team.LoginResult
	decodeData(t, rr, &body)
	assert.Equal(t, "admin-jwt", body.AccessToken)
}

func TestVerify2FA_WrongCode(t *testing.T) {
	mock := &mockSvc{
		verify2FAFn: func(_ context.Context, _, _ string) (*team.LoginResult, error) {
			return nil, apperrors.New(http.StatusUnauthorized, "INVALID_2FA_CODE", "invalid TOTP code")
		},
	}
	r := newRouter(newHandler(mock), adminID)

	req := httptest.NewRequest(http.MethodPost, "/admin/auth/2fa/verify",
		jsonBody(t, map[string]string{"pre_auth_token": "pre-tok", "code": "000000"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestVerify2FA_MissingFields(t *testing.T) {
	r := newRouter(newHandler(&mockSvc{}), adminID)

	req := httptest.NewRequest(http.MethodPost, "/admin/auth/2fa/verify",
		jsonBody(t, map[string]string{"pre_auth_token": "pre-tok"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestVerify2FA_NoAuth(t *testing.T) {
	r := noAuthRouter(newHandler(&mockSvc{}))
	req := httptest.NewRequest(http.MethodPost, "/admin/auth/2fa/verify",
		jsonBody(t, map[string]string{"pre_auth_token": "x", "code": "123456"}))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

// ── VerifyBackupCode ──────────────────────────────────────────────────────

func TestVerifyBackupCode_Valid(t *testing.T) {
	mock := &mockSvc{
		verifyBackupFn: func(_ context.Context, _, _ string) (*team.LoginResult, error) {
			return &team.LoginResult{AccessToken: "admin-jwt"}, nil
		},
	}
	r := newRouter(newHandler(mock), adminID)

	req := httptest.NewRequest(http.MethodPost, "/admin/auth/2fa/backup",
		jsonBody(t, map[string]string{"pre_auth_token": "pre-tok", "backup_code": "ab1cd-ef2gh"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestVerifyBackupCode_AlreadyUsed(t *testing.T) {
	mock := &mockSvc{
		verifyBackupFn: func(_ context.Context, _, _ string) (*team.LoginResult, error) {
			return nil, apperrors.New(http.StatusUnauthorized, "BACKUP_CODE_USED", "backup code already used")
		},
	}
	r := newRouter(newHandler(mock), adminID)

	req := httptest.NewRequest(http.MethodPost, "/admin/auth/2fa/backup",
		jsonBody(t, map[string]string{"pre_auth_token": "pre-tok", "backup_code": "used-code"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestVerifyBackupCode_MissingFields(t *testing.T) {
	r := newRouter(newHandler(&mockSvc{}), adminID)
	req := httptest.NewRequest(http.MethodPost, "/admin/auth/2fa/backup",
		jsonBody(t, map[string]string{"pre_auth_token": "pre-tok"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// ── Logout ────────────────────────────────────────────────────────────────

func TestLogout_InvalidatesSession(t *testing.T) {
	var gotAdminID, gotJTI string
	mock := &mockSvc{
		logoutFn: func(_ context.Context, adminID, jti string) error {
			gotAdminID = adminID
			gotJTI = jti
			return nil
		},
	}
	r := newRouter(newHandler(mock), adminID)

	req := httptest.NewRequest(http.MethodPost, "/admin/auth/logout", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, adminID, gotAdminID)
	assert.Equal(t, "test-jti", gotJTI)
}

// ── 2FA Setup ─────────────────────────────────────────────────────────────

func TestSetup2FA_ReturnsSecretAndURL(t *testing.T) {
	mock := &mockSvc{
		setup2FAFn: func(_ context.Context, _ string) (string, string, error) {
			return "BASE32SECRET", "otpauth://totp/Rides:admin@test.com?secret=BASE32SECRET", nil
		},
	}
	r := newRouter(newHandler(mock), adminID)

	req := httptest.NewRequest(http.MethodGet, "/admin/account/2fa/setup", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	var body map[string]string
	decodeData(t, rr, &body)
	assert.Equal(t, "BASE32SECRET", body["secret"])
	assert.NotEmpty(t, body["otpauth_url"])
}

func TestSetup2FA_NoAuth(t *testing.T) {
	r := noAuthRouter(newHandler(&mockSvc{}))
	req := httptest.NewRequest(http.MethodGet, "/admin/account/2fa/setup", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

// ── Enable2FA ─────────────────────────────────────────────────────────────

func TestEnable2FA_ReturnsBackupCodes(t *testing.T) {
	codes := []string{"code-1", "code-2", "code-3"}
	mock := &mockSvc{
		enable2FAFn: func(_ context.Context, _, _, _ string) ([]string, error) {
			return codes, nil
		},
	}
	r := newRouter(newHandler(mock), adminID)

	req := httptest.NewRequest(http.MethodPost, "/admin/account/2fa/enable",
		jsonBody(t, map[string]string{"secret": "BASE32SECRET", "code": "123456"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	var body map[string]interface{}
	decodeData(t, rr, &body)
	assert.Equal(t, true, body["two_factor_enabled"])
	assert.NotEmpty(t, body["backup_codes"])
}

func TestEnable2FA_InvalidCode(t *testing.T) {
	mock := &mockSvc{
		enable2FAFn: func(_ context.Context, _, _, _ string) ([]string, error) {
			return nil, apperrors.New(http.StatusUnauthorized, "INVALID_2FA_CODE", "invalid TOTP code")
		},
	}
	r := newRouter(newHandler(mock), adminID)

	req := httptest.NewRequest(http.MethodPost, "/admin/account/2fa/enable",
		jsonBody(t, map[string]string{"secret": "BASE32SECRET", "code": "000000"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestEnable2FA_MissingFields(t *testing.T) {
	r := newRouter(newHandler(&mockSvc{}), adminID)
	req := httptest.NewRequest(http.MethodPost, "/admin/account/2fa/enable",
		jsonBody(t, map[string]string{"secret": "BASE32SECRET"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// ── Disable2FA ────────────────────────────────────────────────────────────

func TestDisable2FA_Success(t *testing.T) {
	mock := &mockSvc{
		disable2FAFn: func(_ context.Context, _, _ string) error { return nil },
	}
	r := newRouter(newHandler(mock), adminID)

	req := httptest.NewRequest(http.MethodPost, "/admin/account/2fa/disable",
		jsonBody(t, map[string]string{"password": "mypassword"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	var body map[string]bool
	decodeData(t, rr, &body)
	assert.Equal(t, false, body["two_factor_enabled"])
}

func TestDisable2FA_MissingPassword(t *testing.T) {
	r := newRouter(newHandler(&mockSvc{}), adminID)
	req := httptest.NewRequest(http.MethodPost, "/admin/account/2fa/disable",
		jsonBody(t, map[string]string{}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// ── ResetTOTP ─────────────────────────────────────────────────────────────

func TestResetTOTP_Success(t *testing.T) {
	mock := &mockSvc{
		resetTOTPFn: func(_ context.Context, _, _ string) (string, string, []string, error) {
			return "NEW_SECRET", "otpauth://...", []string{"code-1", "code-2"}, nil
		},
	}
	r := newRouter(newHandler(mock), adminID)

	req := httptest.NewRequest(http.MethodPost, "/admin/auth/totp/reset",
		jsonBody(t, map[string]string{"code": "123456"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	var body map[string]interface{}
	decodeData(t, rr, &body)
	assert.Equal(t, "NEW_SECRET", body["secret"])
	assert.NotEmpty(t, body["backup_codes"])
}

func TestResetTOTP_WrongCode(t *testing.T) {
	mock := &mockSvc{
		resetTOTPFn: func(_ context.Context, _, _ string) (string, string, []string, error) {
			return "", "", nil, apperrors.New(http.StatusUnauthorized, "INVALID_2FA_CODE", "invalid code")
		},
	}
	r := newRouter(newHandler(mock), adminID)

	req := httptest.NewRequest(http.MethodPost, "/admin/auth/totp/reset",
		jsonBody(t, map[string]string{"code": "000000"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestResetTOTP_MissingCode(t *testing.T) {
	r := newRouter(newHandler(&mockSvc{}), adminID)
	req := httptest.NewRequest(http.MethodPost, "/admin/auth/totp/reset",
		jsonBody(t, map[string]string{}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// ── Team management ───────────────────────────────────────────────────────

func TestListAdmins_HappyPath(t *testing.T) {
	mock := &mockSvc{
		listAdminsFn: func(_ context.Context, _, _, _ string) ([]*team.AdminAccount, error) {
			return []*team.AdminAccount{{ID: "a1", Email: "admin@test.com"}}, nil
		},
	}
	r := newRouter(newHandler(mock), adminID)

	req := httptest.NewRequest(http.MethodGet, "/admin/team", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	var body map[string]interface{}
	decodeData(t, rr, &body)
	assert.NotEmpty(t, body["admins"])
}

func TestListAdmins_NoAuth(t *testing.T) {
	r := noAuthRouter(newHandler(&mockSvc{}))
	req := httptest.NewRequest(http.MethodGet, "/admin/team", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestInvite_HappyPath(t *testing.T) {
	mock := &mockSvc{
		inviteFn: func(_ context.Context, name, email, roleID, _ string) (*team.AdminAccount, error) {
			return &team.AdminAccount{ID: "new-admin", Email: email}, nil
		},
	}
	r := newRouter(newHandler(mock), adminID)

	req := httptest.NewRequest(http.MethodPost, "/admin/team/invite",
		jsonBody(t, map[string]string{"name": "New Admin", "email": "new@test.com", "role_id": "role-uuid"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusCreated, rr.Code)
}

func TestInvite_DuplicateEmail(t *testing.T) {
	mock := &mockSvc{
		inviteFn: func(_ context.Context, _, _, _, _ string) (*team.AdminAccount, error) {
			return nil, apperrors.ErrConflict
		},
	}
	r := newRouter(newHandler(mock), adminID)

	req := httptest.NewRequest(http.MethodPost, "/admin/team/invite",
		jsonBody(t, map[string]string{"name": "Dup", "email": "exists@test.com", "role_id": "role-uuid"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusConflict, rr.Code)
}

func TestInvite_MissingFields(t *testing.T) {
	r := newRouter(newHandler(&mockSvc{}), adminID)
	req := httptest.NewRequest(http.MethodPost, "/admin/team/invite",
		jsonBody(t, map[string]string{"name": "No Role"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestListRoles_HappyPath(t *testing.T) {
	desc := "Full access"
	mock := &mockSvc{
		listRolesFn: func(_ context.Context) ([]*team.Role, error) {
			return []*team.Role{{ID: "r1", Name: "Super Admin", Description: &desc}}, nil
		},
	}
	r := newRouter(newHandler(mock), adminID)

	req := httptest.NewRequest(http.MethodGet, "/admin/team/roles", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

// ── Suspend / Reinstate / Remove ──────────────────────────────────────────

func TestSuspend_HappyPath(t *testing.T) {
	var gotID string
	mock := &mockSvc{
		suspendFn: func(_ context.Context, id string) error {
			gotID = id
			return nil
		},
	}
	r := newRouter(newHandler(mock), adminID)

	req := httptest.NewRequest(http.MethodPost, "/admin/team/members/member-uuid/suspend", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNoContent, rr.Code)
	assert.Equal(t, "member-uuid", gotID)
}

func TestSuspend_SelfSuspend(t *testing.T) {
	// adminID == member id → SELF_ACTION forbidden
	r := newRouter(newHandler(&mockSvc{}), adminID)
	req := httptest.NewRequest(http.MethodPost, "/admin/team/members/"+adminID+"/suspend", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestReinstate_HappyPath(t *testing.T) {
	mock := &mockSvc{
		reinstateFn: func(_ context.Context, _ string) error { return nil },
	}
	r := newRouter(newHandler(mock), adminID)
	req := httptest.NewRequest(http.MethodPost, "/admin/team/members/member-uuid/reinstate", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNoContent, rr.Code)
}

func TestRemove_HappyPath(t *testing.T) {
	mock := &mockSvc{
		removeFn: func(_ context.Context, _ string) error { return nil },
	}
	r := newRouter(newHandler(mock), adminID)
	req := httptest.NewRequest(http.MethodPost, "/admin/team/members/member-uuid/remove", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNoContent, rr.Code)
}

func TestRemove_SelfRemove(t *testing.T) {
	r := newRouter(newHandler(&mockSvc{}), adminID)
	req := httptest.NewRequest(http.MethodPost, "/admin/team/members/"+adminID+"/remove", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

// ── UpdateRole / SetPassword ──────────────────────────────────────────────

func TestUpdateRole_HappyPath(t *testing.T) {
	mock := &mockSvc{
		updateRoleFn: func(_ context.Context, _, _ string) error { return nil },
	}
	r := newRouter(newHandler(mock), adminID)
	req := httptest.NewRequest(http.MethodPost, "/admin/team/members/member-uuid/role",
		jsonBody(t, map[string]string{"role_id": "new-role-uuid"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNoContent, rr.Code)
}

func TestUpdateRole_MissingRoleID(t *testing.T) {
	r := newRouter(newHandler(&mockSvc{}), adminID)
	req := httptest.NewRequest(http.MethodPost, "/admin/team/members/member-uuid/role",
		jsonBody(t, map[string]string{}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestSetPassword_HappyPath(t *testing.T) {
	mock := &mockSvc{
		setPasswordFn: func(_ context.Context, _, _ string) error { return nil },
	}
	r := newRouter(newHandler(mock), adminID)
	req := httptest.NewRequest(http.MethodPost, "/admin/team/members/member-uuid/set-password",
		jsonBody(t, map[string]string{"password": "newpassword123"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNoContent, rr.Code)
}

func TestSetPassword_TooShort(t *testing.T) {
	r := newRouter(newHandler(&mockSvc{}), adminID)
	req := httptest.NewRequest(http.MethodPost, "/admin/team/members/member-uuid/set-password",
		jsonBody(t, map[string]string{"password": "short"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// ── CreateRole / UpdateRoleByID / DeleteRoleByID ──────────────────────────

func TestCreateRole_HappyPath(t *testing.T) {
	mock := &mockSvc{
		createRoleFn: func(_ context.Context, name, _ string, _ interface{}) (*team.Role, error) {
			return &team.Role{ID: "new-role", Name: name}, nil
		},
	}
	r := newRouter(newHandler(mock), adminID)
	req := httptest.NewRequest(http.MethodPost, "/admin/team/roles",
		jsonBody(t, map[string]interface{}{"name": "Finance", "permissions": []string{"/admin/revenue"}}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusCreated, rr.Code)
}

func TestCreateRole_MissingName(t *testing.T) {
	r := newRouter(newHandler(&mockSvc{}), adminID)
	req := httptest.NewRequest(http.MethodPost, "/admin/team/roles",
		jsonBody(t, map[string]interface{}{"permissions": []string{}}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestDeleteRoleByID_SystemRole(t *testing.T) {
	mock := &mockSvc{
		deleteRoleByIDFn: func(_ context.Context, _ string) error {
			return errors.New("cannot_delete_system_role")
		},
	}
	r := newRouter(newHandler(mock), adminID)
	req := httptest.NewRequest(http.MethodDelete, "/admin/team/roles/system-role-id", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestDeleteRoleByID_HappyPath(t *testing.T) {
	mock := &mockSvc{
		deleteRoleByIDFn: func(_ context.Context, _ string) error { return nil },
	}
	r := newRouter(newHandler(mock), adminID)
	req := httptest.NewRequest(http.MethodDelete, "/admin/team/roles/custom-role-id", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

// ── Account endpoints ─────────────────────────────────────────────────────

func TestUpdateAccount_HappyPath(t *testing.T) {
	mock := &mockSvc{
		updateNameFn: func(_ context.Context, _, _ string) error { return nil },
		listAdminsFn: func(_ context.Context, _, _, _ string) ([]*team.AdminAccount, error) {
			return []*team.AdminAccount{{ID: adminID, Name: "Old Name"}}, nil
		},
	}
	r := newRouter(newHandler(mock), adminID)
	req := httptest.NewRequest(http.MethodPut, "/admin/account",
		jsonBody(t, map[string]string{"name": "New Name"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNoContent, rr.Code)
}

func TestUpdateAccount_MissingName(t *testing.T) {
	r := newRouter(newHandler(&mockSvc{}), adminID)
	req := httptest.NewRequest(http.MethodPut, "/admin/account",
		jsonBody(t, map[string]string{}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestChangePassword_HappyPath(t *testing.T) {
	mock := &mockSvc{
		changePasswordFn: func(_ context.Context, _, _, _ string) error { return nil },
	}
	r := newRouter(newHandler(mock), adminID)
	req := httptest.NewRequest(http.MethodPost, "/admin/account/password",
		jsonBody(t, map[string]string{"current_password": "old123", "new_password": "newpass123"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNoContent, rr.Code)
}

func TestChangePassword_TooShort(t *testing.T) {
	r := newRouter(newHandler(&mockSvc{}), adminID)
	req := httptest.NewRequest(http.MethodPost, "/admin/account/password",
		jsonBody(t, map[string]string{"current_password": "old", "new_password": "short"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// ── GetAccount ────────────────────────────────────────────────────────────

func TestGetAccount_Found(t *testing.T) {
	mock := &mockSvc{
		listAdminsFn: func(_ context.Context, _, _, _ string) ([]*team.AdminAccount, error) {
			return []*team.AdminAccount{{ID: adminID, Email: "admin@test.com"}}, nil
		},
	}
	r := newRouter(newHandler(mock), adminID)
	req := httptest.NewRequest(http.MethodGet, "/admin/account", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestGetAccount_NotFound(t *testing.T) {
	mock := &mockSvc{
		listAdminsFn: func(_ context.Context, _, _, _ string) ([]*team.AdminAccount, error) {
			return []*team.AdminAccount{{ID: "other-admin"}}, nil
		},
	}
	r := newRouter(newHandler(mock), adminID)
	req := httptest.NewRequest(http.MethodGet, "/admin/account", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

// ── Sessions ──────────────────────────────────────────────────────────────

func TestGetSessions_ReturnsCurrent(t *testing.T) {
	r := newRouter(newHandler(&mockSvc{}), adminID)
	req := httptest.NewRequest(http.MethodGet, "/admin/account/sessions", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	var body map[string]interface{}
	decodeData(t, rr, &body)
	assert.NotEmpty(t, body["sessions"])
}

func TestRevokeSession_HappyPath(t *testing.T) {
	mock := &mockSvc{
		logoutFn: func(_ context.Context, _, _ string) error { return nil },
	}
	r := newRouter(newHandler(mock), adminID)
	req := httptest.NewRequest(http.MethodDelete, "/admin/account/sessions/session-abc", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

// ── UpdateRoleByID ────────────────────────────────────────────────────────

func TestUpdateRoleByID_HappyPath(t *testing.T) {
	desc := "updated"
	mock := &mockSvc{
		updateRoleByIDFn: func(_ context.Context, roleID, _, _ string, _ interface{}) (*team.Role, error) {
			return &team.Role{ID: roleID, Name: "Updated Role", Description: &desc}, nil
		},
	}
	r := newRouter(newHandler(mock), adminID)
	req := httptest.NewRequest(http.MethodPatch, "/admin/team/roles/role-uuid",
		jsonBody(t, map[string]interface{}{"name": "Updated Role", "permissions": []string{}}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestUpdateRoleByID_SystemRole(t *testing.T) {
	mock := &mockSvc{
		updateRoleByIDFn: func(_ context.Context, _, _, _ string, _ interface{}) (*team.Role, error) {
			return nil, errors.New("cannot_delete_system_role")
		},
	}
	r := newRouter(newHandler(mock), adminID)
	req := httptest.NewRequest(http.MethodPatch, "/admin/team/roles/system-role",
		jsonBody(t, map[string]interface{}{"name": "Super Admin"}))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// ── Reinstate error case ──────────────────────────────────────────────────

func TestReinstate_NotFound(t *testing.T) {
	mock := &mockSvc{
		reinstateFn: func(_ context.Context, _ string) error { return apperrors.ErrNotFound },
	}
	r := newRouter(newHandler(mock), adminID)
	req := httptest.NewRequest(http.MethodPost, "/admin/team/members/unknown/reinstate", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

// ── ListRoles error case ──────────────────────────────────────────────────

func TestListRoles_ServiceError(t *testing.T) {
	mock := &mockSvc{
		listRolesFn: func(_ context.Context) ([]*team.Role, error) {
			return nil, apperrors.ErrInternal
		},
	}
	r := newRouter(newHandler(mock), adminID)
	req := httptest.NewRequest(http.MethodGet, "/admin/team/roles", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}
