package team

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	mw "github.com/workspace/ride-platform/internal/middleware"
	"github.com/workspace/ride-platform/pkg/respond"
)

type Handler struct {
	svc TeamService
}

func NewHandler(svc TeamService) *Handler {
	return &Handler{svc: svc}
}

// ── Auth ──────────────────────────────────────────────────────────────────

// POST /api/v1/admin/auth/login
// Step 1 of login. Returns either a full access_token (no 2FA) or a
// short-lived pre_auth_token with two_factor_required: true.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Email == "" || body.Password == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "email and password are required")
		return
	}
	result, err := h.svc.Login(r.Context(), body.Email, body.Password)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, result)
}

// POST /api/v1/admin/auth/2fa/reissue
// Returns a pre_auth_token when 2FA is already enabled (recovery for setup UI mismatch).
func (h *Handler) Reissue2FAChallenge(w http.ResponseWriter, r *http.Request) {
	claims := mw.GetClaims(r)
	if claims == nil || claims.UserID == "" {
		respond.ErrorMsg(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	if claims.TokenType == preAuthTokenType {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "use your sign-in access token, not a pre-auth token")
		return
	}
	preAuth, err := h.svc.Reissue2FAChallenge(r.Context(), claims.UserID)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{
		"pre_auth_token":      preAuth,
		"two_factor_required": true,
	})
}

// POST /api/v1/admin/auth/2fa/verify
// Step 2a: complete login with a TOTP authenticator code.
// Body: { "pre_auth_token": "...", "code": "123456" }
func (h *Handler) Verify2FA(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PreAuthToken string `json:"pre_auth_token"`
		Code         string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil ||
		body.PreAuthToken == "" || body.Code == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "pre_auth_token and code are required")
		return
	}
	result, err := h.svc.Verify2FA(r.Context(), body.PreAuthToken, body.Code)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, result)
}

// POST /api/v1/admin/auth/2fa/backup
// Step 2b: complete login with a single-use backup code.
// Body: { "pre_auth_token": "...", "backup_code": "ab1cd-ef2gh" }
func (h *Handler) VerifyBackupCode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PreAuthToken string `json:"pre_auth_token"`
		BackupCode   string `json:"backup_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil ||
		body.PreAuthToken == "" || body.BackupCode == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "pre_auth_token and backup_code are required")
		return
	}
	result, err := h.svc.VerifyBackupCode(r.Context(), body.PreAuthToken, body.BackupCode)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, result)
}

// ── 2FA setup (protected — requires valid admin JWT) ──────────────────────

// GET /api/v1/admin/account/2fa/setup
// Returns a fresh TOTP secret + otpauth:// URI so the frontend can render a QR code.
// The secret is NOT stored yet. Call /2fa/enable after the user scans and verifies.
func (h *Handler) Setup2FA(w http.ResponseWriter, r *http.Request) {
	claims := mw.GetClaims(r)
	secret, otpauthURL, err := h.svc.Generate2FASetup(r.Context(), claims.UserID)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]string{
		"secret":      secret,
		"otpauth_url": otpauthURL,
	})
}

// POST /api/v1/admin/account/2fa/enable
// Verifies the TOTP code against the pending secret, persists it, and returns backup codes.
// Body: { "secret": "BASE32SECRET", "code": "123456" }
// The backup codes are returned ONCE in plaintext — store them securely.
func (h *Handler) Enable2FA(w http.ResponseWriter, r *http.Request) {
	claims := mw.GetClaims(r)
	var body struct {
		Secret string `json:"secret"`
		Code   string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil ||
		body.Secret == "" || body.Code == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "secret and code are required")
		return
	}
	backupCodes, err := h.svc.Enable2FA(r.Context(), claims.UserID, body.Secret, body.Code)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{
		"two_factor_enabled": true,
		"backup_codes":       backupCodes,
	})
}

// POST /api/v1/admin/account/2fa/disable
// Disables 2FA. Requires current password for confirmation.
// Body: { "password": "current-password" }
func (h *Handler) Disable2FA(w http.ResponseWriter, r *http.Request) {
	claims := mw.GetClaims(r)
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Password == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "password is required")
		return
	}
	if err := h.svc.Disable2FA(r.Context(), claims.UserID, body.Password); err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]bool{"two_factor_enabled": false})
}

// ── Account (own profile) ─────────────────────────────────────────────────

// GET /api/v1/admin/account
func (h *Handler) GetAccount(w http.ResponseWriter, r *http.Request) {
	claims := mw.GetClaims(r)
	admins, err := h.svc.ListAdmins(r.Context(), "", "", "")
	if err != nil {
		respond.Error(w, err)
		return
	}
	for _, a := range admins {
		if a.ID == claims.UserID {
			respond.OK(w, a)
			return
		}
	}
	respond.ErrorMsg(w, http.StatusNotFound, "NOT_FOUND", "account not found")
}

// PUT /api/v1/admin/account
func (h *Handler) UpdateAccount(w http.ResponseWriter, r *http.Request) {
	claims := mw.GetClaims(r)
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "name is required")
		return
	}
	if err := h.svc.UpdateName(r.Context(), claims.UserID, body.Name); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// POST /api/v1/admin/account/password
func (h *Handler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	claims := mw.GetClaims(r)
	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.NewPassword) < 8 {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "new_password must be at least 8 characters")
		return
	}
	if err := h.svc.ChangePassword(r.Context(), claims.UserID, body.CurrentPassword, body.NewPassword); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// ── Team management ───────────────────────────────────────────────────────

// GET /api/v1/admin/team
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	admins, err := h.svc.ListAdmins(r.Context(), q.Get("status"), q.Get("role_id"), q.Get("search"))
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"admins": admins})
}

// POST /api/v1/admin/team/invite
func (h *Handler) Invite(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		RoleID   string `json:"role_id"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Email == "" || body.RoleID == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "email and role_id are required")
		return
	}
	if body.Password != "" && len(body.Password) < 8 {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "password must be at least 8 characters")
		return
	}
	admin, err := h.svc.Invite(r.Context(), body.Name, body.Email, body.RoleID, body.Password)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.Created(w, admin)
}

// GET /api/v1/admin/team/roles
func (h *Handler) ListRoles(w http.ResponseWriter, r *http.Request) {
	roles, err := h.svc.ListRoles(r.Context())
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"roles": roles})
}

// POST /api/v1/admin/team/members/:id/role
func (h *Handler) UpdateRole(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		RoleID string `json:"role_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.RoleID == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "role_id is required")
		return
	}
	if err := h.svc.UpdateRole(r.Context(), id, body.RoleID); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// POST /api/v1/admin/team/members/:id/suspend
func (h *Handler) Suspend(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	claims := mw.GetClaims(r)
	if claims != nil && claims.UserID == id {
		respond.ErrorMsg(w, http.StatusForbidden, "SELF_ACTION", "cannot suspend your own account")
		return
	}
	if err := h.svc.Suspend(r.Context(), id); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// POST /api/v1/admin/team/members/:id/reinstate
func (h *Handler) Reinstate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.svc.Reinstate(r.Context(), id); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// POST /api/v1/admin/team/members/:id/remove
func (h *Handler) Remove(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	claims := mw.GetClaims(r)
	if claims != nil && claims.UserID == id {
		respond.ErrorMsg(w, http.StatusForbidden, "SELF_ACTION", "cannot remove your own account")
		return
	}
	if err := h.svc.Remove(r.Context(), id); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// POST /api/v1/admin/team/members/:id/set-password
func (h *Handler) SetPassword(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Password) < 8 {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "password must be at least 8 characters")
		return
	}
	if err := h.svc.SetPassword(r.Context(), id, body.Password); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// POST /api/v1/admin/auth/logout
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	claims := mw.GetClaims(r)
	if claims != nil {
		_ = h.svc.Logout(r.Context(), claims.UserID, claims.ID)
	}
	respond.OK(w, map[string]string{"message": "logged out"})
}

// POST /api/v1/admin/auth/totp/reset-login
// Re-enroll TOTP during login using a pre_auth_token (no full admin session required).
func (h *Handler) ResetTOTPLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PreAuthToken string `json:"pre_auth_token"`
		Code         string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.PreAuthToken == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "pre_auth_token is required")
		return
	}
	secret, qr, backupCodes, err := h.svc.ResetTOTPFromPreAuth(r.Context(), body.PreAuthToken, body.Code)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{
		"secret":       secret,
		"qr_code_url":  qr,
		"backup_codes": backupCodes,
	})
}

// POST /api/v1/admin/auth/totp/reset
func (h *Handler) ResetTOTP(w http.ResponseWriter, r *http.Request) {
	claims := mw.GetClaims(r)
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Code == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "code is required")
		return
	}
	secret, qr, backupCodes, err := h.svc.ResetTOTP(r.Context(), claims.UserID, body.Code)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{
		"secret":       secret,
		"qr_code_url":  qr,
		"backup_codes": backupCodes,
	})
}

// GET /api/v1/admin/account/sessions  — returns the current session only (full session tracking not yet implemented)
func (h *Handler) GetSessions(w http.ResponseWriter, r *http.Request) {
	claims := mw.GetClaims(r)
	session := map[string]interface{}{
		"id":      claims.ID,
		"current": true,
	}
	respond.OK(w, map[string]interface{}{"sessions": []interface{}{session}})
}

// DELETE /api/v1/admin/account/sessions/:sessionId
func (h *Handler) RevokeSession(w http.ResponseWriter, r *http.Request) {
	claims := mw.GetClaims(r)
	sessionID := chi.URLParam(r, "sessionId")
	if err := h.svc.Logout(r.Context(), claims.UserID, sessionID); err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]string{"message": "session revoked"})
}

// POST /api/v1/admin/team/roles
func (h *Handler) CreateRole(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string      `json:"name"`
		Description string      `json:"description"`
		Permissions interface{} `json:"permissions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "name is required")
		return
	}
	if body.Permissions == nil {
		body.Permissions = []interface{}{}
	}
	role, err := h.svc.CreateRole(r.Context(), body.Name, body.Description, body.Permissions)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.Created(w, role)
}

// PATCH /api/v1/admin/team/roles/:roleId
func (h *Handler) UpdateRoleByID(w http.ResponseWriter, r *http.Request) {
	roleID := chi.URLParam(r, "roleId")
	var body struct {
		Name        string      `json:"name"`
		Description string      `json:"description"`
		Permissions interface{} `json:"permissions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON")
		return
	}
	role, err := h.svc.UpdateRoleByID(r.Context(), roleID, body.Name, body.Description, body.Permissions)
	if err != nil {
		if err.Error() == "cannot_delete_system_role" {
			respond.ErrorMsg(w, http.StatusBadRequest, "CANNOT_MODIFY_SYSTEM_ROLE", "system roles cannot be modified")
			return
		}
		respond.Error(w, err)
		return
	}
	respond.OK(w, role)
}

// DELETE /api/v1/admin/team/roles/:roleId
func (h *Handler) DeleteRoleByID(w http.ResponseWriter, r *http.Request) {
	roleID := chi.URLParam(r, "roleId")
	if err := h.svc.DeleteRoleByID(r.Context(), roleID); err != nil {
		if err.Error() == "cannot_delete_system_role" {
			respond.ErrorMsg(w, http.StatusBadRequest, "CANNOT_DELETE_SYSTEM_ROLE", "system roles cannot be deleted")
			return
		}
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]string{"message": "deleted"})
}
