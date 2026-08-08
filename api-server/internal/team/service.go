package team

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	goredis "github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"golang.org/x/crypto/bcrypt"

	"github.com/workspace/ride-platform/config"
	"github.com/workspace/ride-platform/internal/email"
	"github.com/workspace/ride-platform/pkg/adminrole"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	rkeys "github.com/workspace/ride-platform/pkg/redis"
)

const (
	totpIssuer       = "Rides Admin"
	preAuthTokenType = "pre_auth"
	// setupTokenType marks a token that may ONLY complete TOTP enrolment.
	// Deliberately distinct from "access": the admin middleware accepts nothing
	// but "access", so this is unusable against every /admin endpoint.
	setupTokenType  = "totp_setup"
	backupCodeCount = 10
	// Fallback for the login 2FA window when config carries no value.
	// The configured default lives in config.go as JWT_ADMIN_PREAUTH_MINUTES.
	defaultPreAuthExpiry = 30 * time.Minute
)

// LoginResult is returned by Login. Exactly one of three shapes comes back:
//
//   - TwoFactorRequired  → enrolled admin; call Verify2FA with PreAuthToken.
//   - TwoFactorSetupRequired → not enrolled; call EnrollTOTPBegin / …Complete
//     with SetupToken. AccessToken is deliberately empty — see issueSetupToken.
//   - AccessToken        → nothing further needed (no path produces this today,
//     since 2FA is mandatory; kept so a future exemption has somewhere to go).
type LoginResult struct {
	AccessToken            string        `json:"access_token,omitempty"`
	PreAuthToken           string        `json:"pre_auth_token,omitempty"`
	SetupToken             string        `json:"setup_token,omitempty"`
	TwoFactorRequired      bool          `json:"two_factor_required"`
	TwoFactorSetupRequired bool          `json:"two_factor_setup_required"`
	Admin                  *AdminAccount `json:"admin,omitempty"`
}

// TOTPEnrollment is returned when a login-time enrolment begins.
type TOTPEnrollment struct {
	Secret     string `json:"secret"`
	OTPAuthURL string `json:"otpauth_url"`
}

type Service struct {
	repo TeamRepo
	cfg  *config.Config
	rdb  goredis.UniversalClient
	log  zerolog.Logger
}

func NewService(repo TeamRepo, cfg *config.Config, rdb goredis.UniversalClient, log zerolog.Logger) *Service {
	return &Service{repo: repo, cfg: cfg, rdb: rdb, log: log}
}

// ── Admin management ──────────────────────────────────────────────────────

func (s *Service) ListAdmins(ctx context.Context, status, roleID, search string) ([]*AdminAccount, error) {
	return s.repo.ListAdmins(ctx, status, roleID, search)
}

func (s *Service) Invite(ctx context.Context, name, email, roleID, password string) (*AdminAccount, error) {
	admin, err := s.repo.Invite(ctx, name, email, roleID)
	if err != nil {
		return nil, err
	}
	if password != "" {
		if err := s.SetPassword(ctx, admin.ID, password); err != nil {
			return nil, err
		}
		admin.Status = "ACTIVE"
	}
	return admin, nil
}

func (s *Service) UpdateRole(ctx context.Context, id, roleID string) error {
	return s.repo.UpdateRole(ctx, id, roleID)
}

func (s *Service) Suspend(ctx context.Context, id string) error {
	return s.repo.UpdateStatus(ctx, id, "SUSPENDED")
}

func (s *Service) Reinstate(ctx context.Context, id string) error {
	return s.repo.UpdateStatus(ctx, id, "ACTIVE")
}

func (s *Service) Remove(ctx context.Context, id string) error {
	return s.repo.Delete(ctx, id)
}

// ResendInvite refreshes the invited_at timestamp for a team member invite.
// ResendInvite re-stamps invited_at and actually sends the invite email. It used
// to only bump the timestamp while the console toasted "Invite resent to {email}",
// so the admin believed mail had gone out and the invitee kept waiting.
//
// No temporary password is issued here: re-sending an invite must not silently
// rotate a credential. The mail points the invitee at the login/reset flow.
func (s *Service) ResendInvite(ctx context.Context, id, loginURL string) error {
	if err := s.repo.TouchInvitedAt(ctx, id); err != nil {
		return err
	}
	a, _, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return err
	}
	if a == nil || a.Email == "" {
		return apperrors.New(http.StatusUnprocessableEntity, "NO_EMAIL", "this admin has no email address")
	}
	htmlContent := email.BuildWelcomeEmail(a.Name, a.Email, a.RoleName, "", loginURL)
	if err := email.SendEmail(ctx, a.Email, "Your Rides admin invite", htmlContent); err != nil {
		// Surface it: the caller claims the invite was sent.
		return apperrors.New(http.StatusBadGateway, "EMAIL_SEND_FAILED",
			"the invite could not be emailed — check the mail configuration")
	}
	return nil
}

// ResetMember2FA clears TOTP credentials for another admin account.
func (s *Service) ResetMember2FA(ctx context.Context, actorID, memberID string) error {
	if actorID == memberID {
		return apperrors.New(http.StatusForbidden, "SELF_ACTION", "use account settings to reset your own 2FA")
	}
	return s.repo.ClearTOTP(ctx, memberID)
}

func (s *Service) ListRoles(ctx context.Context) ([]*Role, error) {
	return s.repo.ListRoles(ctx)
}

func (s *Service) CreateRole(ctx context.Context, name, description string, permissions interface{}) (*Role, error) {
	return s.repo.CreateRole(ctx, name, description, permissions)
}

func (s *Service) UpdateRoleByID(ctx context.Context, roleID, name, description string, permissions interface{}) (*Role, error) {
	return s.repo.UpdateRoleByID(ctx, roleID, name, description, permissions)
}

func (s *Service) DeleteRoleByID(ctx context.Context, roleID string) error {
	return s.repo.DeleteRoleByID(ctx, roleID)
}

// UpdateRolePermissions replaces the permissions of a non-system role.
func (s *Service) UpdateRolePermissions(ctx context.Context, roleID string, permissions interface{}) (*Role, error) {
	return s.repo.UpdateRolePermissions(ctx, roleID, permissions)
}

func (s *Service) UpdateName(ctx context.Context, id, name string) error {
	return s.repo.UpdateName(ctx, id, name)
}

func (s *Service) UpdateProfile(ctx context.Context, id, name, phone, photoURL string) error {
	return s.repo.UpdateProfile(ctx, id, name, phone, photoURL)
}

func (s *Service) SetPassword(ctx context.Context, id, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return apperrors.ErrInternal
	}
	return s.repo.SetPassword(ctx, id, string(hash))
}

func (s *Service) ChangePassword(ctx context.Context, id, currentPassword, newPassword string) error {
	_, hash, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return apperrors.ErrNotFound
	}
	if hash == nil || *hash == "" {
		return apperrors.New(http.StatusUnauthorized, "PASSWORD_NOT_SET", "no password configured")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(*hash), []byte(currentPassword)); err != nil {
		return apperrors.New(http.StatusUnauthorized, "INVALID_CREDENTIALS", "current password is incorrect")
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return apperrors.ErrInternal
	}
	return s.repo.SetPassword(ctx, id, string(newHash))
}

// ── Authentication ────────────────────────────────────────────────────────

func (s *Service) Login(ctx context.Context, email, password string) (*LoginResult, error) {
	// Normalize the email — seed-admin/invite store it lowercased, and browser
	// inputs often auto-capitalize the first letter, which would 401 otherwise.
	email = strings.ToLower(strings.TrimSpace(email))
	admin, hash, err := s.repo.FindByEmail(ctx, email)
	if err != nil {
		return nil, apperrors.New(http.StatusUnauthorized, "INVALID_CREDENTIALS", "invalid email or password")
	}
	if admin.Status == "SUSPENDED" {
		return nil, apperrors.New(http.StatusForbidden, "ACCOUNT_SUSPENDED", "account is suspended")
	}
	if hash == nil || *hash == "" {
		return nil, apperrors.New(http.StatusUnauthorized, "PASSWORD_NOT_SET", "password not configured; use the set-password flow")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(*hash), []byte(password)); err != nil {
		return nil, apperrors.New(http.StatusUnauthorized, "INVALID_CREDENTIALS", "invalid email or password")
	}

	// Enforce 2FA wherever it is switched on, in every environment.
	//
	// This used to also require cfg.Env == "production", which locked staging
	// admins out entirely: the admin web decides whether to show the 2FA wall
	// from NODE_ENV, and a built Next.js image reports "production" even on
	// staging. So the UI demanded a code while the API, seeing ENV=staging,
	// never issued the pre-auth token that /2fa/verify needs — every attempt
	// came back INVALID_PRE_AUTH_TOKEN, surfaced to the user as "your sign-in
	// session expired", with no way through.
	//
	// Two components disagreeing about what "production" means is not a thing
	// to re-tune; an authentication control should behave identically
	// everywhere, and staging is precisely where you want to exercise it.
	if admin.TwoFactor {
		preAuth, err := s.issuePreAuthToken(admin.ID)
		if err != nil {
			return nil, apperrors.ErrInternal
		}
		return &LoginResult{
			PreAuthToken:      preAuth,
			TwoFactorRequired: true,
		}, nil
	}

	// Not enrolled yet: hand back a token that can ONLY enrol, never an access
	// token.
	//
	// This used to mint a real access token here and rely on the admin console to
	// withhold it and show the QR wall instead. That made 2FA a UI convention: the
	// token was in the login response either way, so anyone reading it out of the
	// network tab — or calling this endpoint directly — had a full admin session
	// without ever touching an authenticator. The wall was in front of the door,
	// not on it.
	setupToken, err := s.issueSetupToken(admin.ID)
	if err != nil {
		return nil, apperrors.ErrInternal
	}
	return &LoginResult{
		SetupToken:             setupToken,
		TwoFactorSetupRequired: true,
	}, nil
}

func (s *Service) Verify2FA(ctx context.Context, preAuthToken, code string) (*LoginResult, error) {
	adminID, err := s.validatePreAuthToken(preAuthToken)
	if err != nil {
		return nil, err
	}

	secret, err := s.repo.GetTOTPSecret(ctx, adminID)
	if err != nil || secret == nil || *secret == "" {
		return nil, apperrors.New(http.StatusConflict, "2FA_NOT_SETUP", "2FA is not configured for this account")
	}

	if !validateTOTP(code, *secret) {
		return nil, apperrors.New(http.StatusUnauthorized, "INVALID_2FA_CODE", "authenticator code is invalid or expired")
	}

	admin, _, err := s.repo.FindByID(ctx, adminID)
	if err != nil {
		return nil, apperrors.ErrNotFound
	}

	token, err := s.issueAccessToken(ctx, adminID, admin.RoleName)
	if err != nil {
		return nil, apperrors.ErrInternal
	}
	go s.repo.TouchLastActive(context.Background(), adminID)
	return &LoginResult{
		AccessToken:       token,
		TwoFactorRequired: false,
		Admin:             admin,
	}, nil
}

func (s *Service) VerifyBackupCode(ctx context.Context, preAuthToken, rawCode string) (*LoginResult, error) {
	adminID, err := s.validatePreAuthToken(preAuthToken)
	if err != nil {
		return nil, err
	}

	codes, err := s.repo.GetBackupCodes(ctx, adminID)
	if err != nil {
		return nil, apperrors.ErrInternal
	}

	matched := -1
	for i, c := range codes {
		if c.Used {
			continue
		}
		if bcrypt.CompareHashAndPassword([]byte(c.Hash), []byte(rawCode)) == nil {
			matched = i
			break
		}
	}
	if matched == -1 {
		return nil, apperrors.New(http.StatusUnauthorized, "INVALID_BACKUP_CODE", "backup code is invalid or already used")
	}

	codes[matched].Used = true
	if err := s.repo.SaveBackupCodes(ctx, adminID, codes); err != nil {
		return nil, apperrors.ErrInternal
	}

	admin, _, err := s.repo.FindByID(ctx, adminID)
	if err != nil {
		return nil, apperrors.ErrNotFound
	}

	token, err := s.issueAccessToken(ctx, adminID, admin.RoleName)
	if err != nil {
		return nil, apperrors.ErrInternal
	}
	go s.repo.TouchLastActive(context.Background(), adminID)
	return &LoginResult{
		AccessToken:       token,
		TwoFactorRequired: false,
		Admin:             admin,
	}, nil
}

// Logout revokes the current session token in Redis.
func (s *Service) Logout(ctx context.Context, adminID, jti string) error {
	if jti == "" {
		return nil
	}
	key := rkeys.K.Session(adminID, jti)
	return s.rdb.Set(ctx, key, "revoked", s.cfg.JWT.AdminIdleExpiry).Err()
}

// ── 2FA setup ─────────────────────────────────────────────────────────────

// Reissue2FAChallenge returns a fresh pre_auth token when the account already has
// 2FA enabled (e.g. client was sent to the setup UI by mistake).
func (s *Service) Reissue2FAChallenge(ctx context.Context, adminID string) (string, error) {
	admin, _, err := s.repo.FindByID(ctx, adminID)
	if err != nil {
		return "", apperrors.ErrNotFound
	}
	if !admin.TwoFactor {
		return "", apperrors.New(http.StatusConflict, "2FA_NOT_ENABLED", "2FA is not enabled on this account")
	}
	return s.issuePreAuthToken(adminID)
}

func (s *Service) Generate2FASetup(ctx context.Context, adminID string) (secret, otpauthURL string, err error) {
	admin, _, err := s.repo.FindByID(ctx, adminID)
	if err != nil {
		return "", "", apperrors.ErrNotFound
	}
	if admin.TwoFactor {
		return "", "", apperrors.New(http.StatusConflict, "2FA_ALREADY_ENABLED", "2FA is already enabled; disable it first")
	}

	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      s.totpIssuerLabel(),
		AccountName: admin.Email,
	})
	if err != nil {
		return "", "", apperrors.ErrInternal
	}
	return key.Secret(), key.URL(), nil
}

func (s *Service) Enable2FA(ctx context.Context, adminID, secret, code string) ([]string, error) {
	// Always prove the scan worked before storing the secret.
	//
	// This check used to run only when cfg.Env == "production", so on staging any
	// six digits enrolled successfully. That is worse than a weak control: the
	// secret gets saved for a QR the admin never actually scanned, and from then
	// on no code they can produce will ever validate — a silent self-lockout that
	// only an admin reset can undo. Verifying one live code is the entire point of
	// the enable step.
	if !validateTOTP(code, secret) {
		return nil, apperrors.New(http.StatusUnauthorized, "INVALID_2FA_CODE", "authenticator code does not match — check the secret and try again")
	}

	plain, hashed, err := generateBackupCodes()
	if err != nil {
		return nil, apperrors.ErrInternal
	}

	if err := s.repo.SaveTOTP(ctx, adminID, secret); err != nil {
		return nil, apperrors.ErrInternal
	}
	if err := s.repo.SaveBackupCodes(ctx, adminID, hashed); err != nil {
		return nil, apperrors.ErrInternal
	}
	return plain, nil
}

func (s *Service) Disable2FA(ctx context.Context, adminID, password string) error {
	_, hash, err := s.repo.FindByID(ctx, adminID)
	if err != nil {
		return apperrors.ErrNotFound
	}
	if hash == nil || *hash == "" {
		return apperrors.New(http.StatusUnauthorized, "PASSWORD_NOT_SET", "no password configured")
	}
	if bcrypt.CompareHashAndPassword([]byte(*hash), []byte(password)) != nil {
		return apperrors.New(http.StatusUnauthorized, "INVALID_CREDENTIALS", "password is incorrect")
	}
	return s.repo.ClearTOTP(ctx, adminID)
}

// ResetTOTP verifies the current TOTP code, clears the existing secret, and
// returns a fresh secret + QR URI for re-enrollment.
//
// Rotating a second factor requires proving you still hold the current one, in
// every environment. Both checks below were gated on cfg.Env == "production",
// which on staging let an authenticated session rotate its own TOTP secret with
// no code at all — so stealing a session was enough to take over the second
// factor and lock the real owner out.
//
// An admin who genuinely lost their authenticator does NOT come through here:
// another Super Admin clears it via ResetMember2FA, which is the auditable path
// and requires a second person. That is the escape hatch, not a weaker check.
func (s *Service) ResetTOTP(ctx context.Context, adminID, currentCode string) (secret, otpauthURL string, backupCodes []string, err error) {
	existing, repoErr := s.repo.GetTOTPSecret(ctx, adminID)

	if repoErr != nil || existing == nil || *existing == "" {
		err = apperrors.New(http.StatusConflict, "2FA_NOT_SETUP", "2FA is not configured")
		return
	}

	if !validateTOTP(currentCode, *existing) {
		err = apperrors.New(http.StatusUnauthorized, "INVALID_2FA_CODE", "authenticator code is invalid")
		return
	}

	admin, _, findErr := s.repo.FindByID(ctx, adminID)
	if findErr != nil {
		err = apperrors.ErrNotFound
		return
	}

	key, genErr := totp.Generate(totp.GenerateOpts{
		Issuer:      s.totpIssuerLabel(),
		AccountName: admin.Email,
	})
	if genErr != nil {
		err = apperrors.ErrInternal
		return
	}

	plain, hashed, genErr := generateBackupCodes()
	if genErr != nil {
		err = apperrors.ErrInternal
		return
	}

	if saveErr := s.repo.SaveTOTP(ctx, adminID, key.Secret()); saveErr != nil {
		err = apperrors.ErrInternal
		return
	}
	if saveErr := s.repo.SaveBackupCodes(ctx, adminID, hashed); saveErr != nil {
		err = apperrors.ErrInternal
		return
	}

	return key.Secret(), key.URL(), plain, nil
}

// ResetTOTPFromPreAuth re-enrolls TOTP during the login 2FA step (pre-auth token only).
func (s *Service) ResetTOTPFromPreAuth(ctx context.Context, preAuthToken, currentCode string) (secret, otpauthURL string, backupCodes []string, err error) {
	adminID, err := s.validatePreAuthToken(preAuthToken)
	if err != nil {
		return "", "", nil, err
	}
	return s.ResetTOTP(ctx, adminID, currentCode)
}

// ── JWT helpers ───────────────────────────────────────────────────────────

// issueAccessToken creates a full admin access token and stores the session in Redis.
// roleName is the human-readable role from admin_roles.name; it is converted to
// the stable enum code (e.g. "Super Admin" → "SUPER_ADMIN") embedded in the JWT.
func (s *Service) issueAccessToken(ctx context.Context, adminID, roleName string) (string, error) {
	return s.issueAccessTokenAt(ctx, adminID, roleName, time.Now())
}

// issueAccessTokenAt mints an admin access token. loginAt is the time the session
// originally started — preserved across renewals so AdminSessionMax can cap the
// chain regardless of how much activity keeps refreshing it.
func (s *Service) issueAccessTokenAt(ctx context.Context, adminID, roleName string, loginAt time.Time) (string, error) {
	jti := uuid.NewString()
	claims := jwt.MapClaims{
		"user_id":    adminID,
		"role_state": "ADMIN",
		"admin_role": adminrole.FromRoleName(roleName),
		"token_type": "access",
		"jti":        jti,
		"login_at":   loginAt.Unix(),
		"exp":        time.Now().Add(s.cfg.JWT.AdminIdleExpiry).Unix(),
		"iat":        time.Now().Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(s.cfg.JWT.AdminAccessSecret))
	if err != nil {
		return "", err
	}

	key := rkeys.K.Session(adminID, jti)
	if err := s.rdb.Set(ctx, key, "valid", s.cfg.JWT.AdminIdleExpiry).Err(); err != nil {
		return "", fmt.Errorf("store admin session: %w", err)
	}
	return signed, nil
}

// RenewSession slides an admin session forward. The console calls this while the
// admin is active, which turns the token lifetime into an idle timeout: keep
// working and you stay signed in; go idle for JWT_ADMIN_IDLE_MINUTES and the next
// request lands on the login screen.
//
// The old jti is revoked so a renewed session leaves exactly one live token, and
// the chain is capped at JWT_ADMIN_SESSION_MAX_HOURS from the original login.
func (s *Service) RenewSession(ctx context.Context, adminID, jti string, loginAt int64) (string, error) {
	if loginAt > 0 {
		started := time.Unix(loginAt, 0)
		if time.Since(started) > s.cfg.JWT.AdminSessionMax {
			return "", apperrors.New(http.StatusUnauthorized, "SESSION_EXPIRED",
				"this session has reached its maximum age — sign in again")
		}
	} else {
		loginAt = time.Now().Unix()
	}

	a, _, err := s.repo.FindByID(ctx, adminID)
	if err != nil {
		return "", apperrors.ErrUnauthorized
	}
	// A suspended/removed admin must not be able to renew.
	if a == nil || a.Status == "SUSPENDED" || a.Status == "DISABLED" {
		return "", apperrors.New(http.StatusUnauthorized, "ACCOUNT_INACTIVE", "this account is no longer active")
	}

	signed, err := s.issueAccessTokenAt(ctx, adminID, a.RoleName, time.Unix(loginAt, 0))
	if err != nil {
		return "", err
	}
	if jti != "" {
		// Best-effort: the new token is already live, so a failed revoke must not
		// fail the renewal.
		_ = s.Logout(ctx, adminID, jti)
	}
	s.repo.TouchLastActive(ctx, adminID)
	return signed, nil
}

func (s *Service) issuePreAuthToken(adminID string) (string, error) {
	// A zero value here would mint tokens that expire the instant they are issued,
	// turning every 2FA login into "your sign-in session expired" with no way
	// through. LoadConfig always sets this, but a Config built directly — in a
	// test, or a future caller — would not, so the failure mode is closed here
	// rather than trusted to every construction site.
	expiry := s.cfg.JWT.AdminPreAuthExpiry
	if expiry <= 0 {
		expiry = defaultPreAuthExpiry
	}
	claims := jwt.MapClaims{
		"user_id":    adminID,
		"token_type": preAuthTokenType,
		"exp":        time.Now().Add(expiry).Unix(),
		"iat":        time.Now().Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(s.cfg.JWT.AdminAccessSecret))
}

// issueSetupToken mints a token whose ONLY power is to complete TOTP enrolment.
//
// token_type is not "access", and the admin middleware admits nothing else
// (internal/middleware/auth.go), so this cannot reach a single /admin endpoint.
// It carries no jti and gets no Redis session, because it is not a session — it
// is a one-purpose ticket that expires with the login attempt that created it.
func (s *Service) issueSetupToken(adminID string) (string, error) {
	expiry := s.cfg.JWT.AdminPreAuthExpiry
	if expiry <= 0 {
		expiry = defaultPreAuthExpiry
	}
	claims := jwt.MapClaims{
		"user_id":    adminID,
		"token_type": setupTokenType,
		"exp":        time.Now().Add(expiry).Unix(),
		"iat":        time.Now().Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(s.cfg.JWT.AdminAccessSecret))
}

func (s *Service) validateSetupToken(tokenStr string) (string, error) {
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, apperrors.ErrTokenInvalid
		}
		return []byte(s.cfg.JWT.AdminAccessSecret), nil
	})
	if err != nil || !token.Valid {
		return "", apperrors.New(http.StatusUnauthorized, "INVALID_SETUP_TOKEN", "setup token is invalid or expired")
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", apperrors.ErrTokenInvalid
	}
	// Checked explicitly so a pre-auth or access token cannot be replayed here.
	if claims["token_type"] != setupTokenType {
		return "", apperrors.New(http.StatusUnauthorized, "INVALID_SETUP_TOKEN", "token type mismatch")
	}

	adminID, _ := claims["user_id"].(string)
	if adminID == "" {
		return "", apperrors.ErrTokenInvalid
	}
	return adminID, nil
}

// EnrollTOTPBegin returns a fresh secret + QR URI for an admin who is signing in
// without 2FA configured. Generates only — nothing is stored until Complete
// verifies a live code, so an abandoned enrolment leaves no trace.
func (s *Service) EnrollTOTPBegin(ctx context.Context, setupToken string) (*TOTPEnrollment, error) {
	adminID, err := s.validateSetupToken(setupToken)
	if err != nil {
		return nil, err
	}
	admin, _, findErr := s.repo.FindByID(ctx, adminID)
	if findErr != nil {
		return nil, apperrors.ErrNotFound
	}
	if admin.TwoFactor {
		// Already enrolled — this path would silently replace a working secret.
		return nil, apperrors.New(http.StatusConflict, "2FA_ALREADY_SET", "2FA is already configured; sign in with your authenticator")
	}

	key, genErr := totp.Generate(totp.GenerateOpts{
		Issuer:      s.totpIssuerLabel(),
		AccountName: admin.Email,
	})
	if genErr != nil {
		return nil, apperrors.ErrInternal
	}
	return &TOTPEnrollment{Secret: key.Secret(), OTPAuthURL: key.URL()}, nil
}

// EnrollTOTPComplete verifies the code against the secret the admin just scanned,
// stores it, and only then issues a real session. This is the single point where
// a setup token becomes an access token.
//
// The secret is passed back in rather than re-generated, because generating again
// would produce a DIFFERENT secret from the QR on screen and no code would ever
// match — the same trap the console hit and documented in login-form.tsx.
func (s *Service) EnrollTOTPComplete(ctx context.Context, setupToken, secret, code string) (*LoginResult, []string, error) {
	adminID, err := s.validateSetupToken(setupToken)
	if err != nil {
		return nil, nil, err
	}
	admin, _, findErr := s.repo.FindByID(ctx, adminID)
	if findErr != nil {
		return nil, nil, apperrors.ErrNotFound
	}
	if admin.TwoFactor {
		return nil, nil, apperrors.New(http.StatusConflict, "2FA_ALREADY_SET", "2FA is already configured; sign in with your authenticator")
	}
	if secret == "" {
		return nil, nil, apperrors.New(http.StatusBadRequest, "BAD_REQUEST", "secret is required")
	}

	// Enable2FA validates the code, stores the secret and returns backup codes.
	backupCodes, enableErr := s.Enable2FA(ctx, adminID, secret, code)
	if enableErr != nil {
		return nil, nil, enableErr
	}

	token, tokErr := s.issueAccessToken(ctx, adminID, admin.RoleName)
	if tokErr != nil {
		return nil, nil, apperrors.ErrInternal
	}
	go s.repo.TouchLastActive(context.Background(), adminID)

	admin.TwoFactor = true
	return &LoginResult{AccessToken: token, Admin: admin}, backupCodes, nil
}

func (s *Service) validatePreAuthToken(tokenStr string) (string, error) {
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, apperrors.ErrTokenInvalid
		}
		return []byte(s.cfg.JWT.AdminAccessSecret), nil
	})
	if err != nil || !token.Valid {
		return "", apperrors.New(http.StatusUnauthorized, "INVALID_PRE_AUTH_TOKEN", "pre-auth token is invalid or expired")
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", apperrors.ErrTokenInvalid
	}
	if claims["token_type"] != preAuthTokenType {
		return "", apperrors.New(http.StatusUnauthorized, "INVALID_PRE_AUTH_TOKEN", "token type mismatch")
	}

	adminID, _ := claims["user_id"].(string)
	if adminID == "" {
		return "", apperrors.ErrTokenInvalid
	}
	return adminID, nil
}

// validateTOTP checks the code against the secret with a ±60-second tolerance
// to handle minor phone clock drift.
// totpIssuerLabel is what the authenticator app shows above the account name.
//
// Staging and production are separate databases with separate TOTP secrets, so
// one person legitimately holds two enrolments for the same email. With a fixed
// issuer both appeared as an identical "Rides Admin: you@example.com" and there
// was no way to tell which was which — entering the production code against
// staging just returns "authenticator code is invalid or expired", with nothing
// pointing at the real cause. Naming the environment makes the two distinct.
func (s *Service) totpIssuerLabel() string {
	if s.cfg.Env == "production" {
		return totpIssuer
	}
	return totpIssuer + " (" + s.cfg.Env + ")"
}

func validateTOTP(code, secret string) bool {
	valid, _ := totp.ValidateCustom(code, secret, time.Now().UTC(), totp.ValidateOpts{
		Period:    30,
		Skew:      2,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
	return valid
}

// ── Backup codes ──────────────────────────────────────────────────────────

func generateBackupCodes() (plain []string, hashed []BackupCode, err error) {
	for i := 0; i < backupCodeCount; i++ {
		raw := make([]byte, 5)
		if _, err = rand.Read(raw); err != nil {
			return nil, nil, err
		}
		raw2 := make([]byte, 5)
		if _, err = rand.Read(raw2); err != nil {
			return nil, nil, err
		}
		code := hex.EncodeToString(raw)[:5] + "-" + hex.EncodeToString(raw2)[:5]
		plain = append(plain, code)

		h, err := bcrypt.GenerateFromPassword([]byte(code), bcrypt.MinCost)
		if err != nil {
			return nil, nil, err
		}
		hashed = append(hashed, BackupCode{Hash: string(h), Used: false})
	}
	return plain, hashed, nil
}

// ── Audit log ─────────────────────────────────────────────────────────────

// AuditEntry is one row from admin_audit_log.
type AuditEntry struct {
	ID         int64          `json:"id"`
	AdminID    string         `json:"admin_id,omitempty"`
	AdminName  string         `json:"admin_name,omitempty"`
	AdminRole  string         `json:"admin_role,omitempty"`
	Action     string         `json:"action"`
	TargetType string         `json:"target_type,omitempty"`
	TargetID   string         `json:"target_id,omitempty"`
	Detail     string         `json:"detail,omitempty"`
	IP         string         `json:"ip,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	OccurredAt time.Time      `json:"occurred_at"`
}

// LogAction inserts one audit entry. Errors are non-fatal — callers ignore them.
func (s *Service) LogAction(ctx context.Context, adminID, action, targetType, targetID, detail, ip string) {
	_ = s.repo.LogAction(ctx, adminID, action, targetType, targetID, detail, ip)
}

// GetMemberActivity returns the most recent audit entries for a given admin.
func (s *Service) GetMemberActivity(ctx context.Context, adminID string, limit int) ([]AuditEntry, error) {
	return s.repo.GetMemberActivity(ctx, adminID, limit)
}

// ListAuditLog returns the platform-wide admin audit trail, newest first.
// Restricted to Super Admin at the route level.
func (s *Service) ListAuditLog(ctx context.Context, actor, action, targetType, from, to string, limit, offset int) ([]AuditEntry, int, error) {
	return s.repo.ListAuditLog(ctx, actor, action, targetType, from, to, limit, offset)
}

func (s *Service) SendWelcomeEmail(ctx context.Context, id, tempPassword, loginURL string) error {
	a, _, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return err
	}

	htmlContent := email.BuildWelcomeEmail(a.Name, a.Email, a.RoleName, tempPassword, loginURL)
	return email.SendEmail(ctx, a.Email, "Welcome to Rides", htmlContent)
}

func (s *Service) ForgotPassword(ctx context.Context, emailAddress string) error {
	admin, _, err := s.repo.FindByEmail(ctx, emailAddress)
	if err != nil || admin == nil {
		// Silently succeed to prevent account enumeration
		return nil
	}

	// Generate a 6-digit numeric OTP code
	otpCode := fmt.Sprintf("%06d", secureRandomNumber(100000, 999999))

	// Save OTP in Redis (expires in 10 minutes)
	redisKey := fmt.Sprintf("admin:reset_otp:%s", emailAddress)
	err = s.rdb.Set(ctx, redisKey, otpCode, 10*time.Minute).Err()
	if err != nil {
		return err
	}

	// Send the OTP code via email
	htmlContent := fmt.Sprintf(`
		<h3>Rides Admin Password Reset</h3>
		<p>Hello %s,</p>
		<p>You requested to reset your password. Use the following 6-digit one-time password (OTP) code to verify your identity:</p>
		<h2 style="letter-spacing: 2px;">%s</h2>
		<p>This code expires in 10 minutes. If you did not request this, please ignore this email.</p>
	`, admin.Name, otpCode)

	// Deliver via email only. Never log the OTP. If delivery fails we log
	// server-side (so ops can act) but still return success — surfacing the
	// error to the caller would leak which emails are registered.
	if err := email.SendEmail(ctx, emailAddress, "Your Password Reset OTP", htmlContent); err != nil {
		s.log.Error().Err(err).Str("email", emailAddress).Msg("admin reset: failed to send OTP email")
	}
	return nil
}

func (s *Service) VerifyResetOTP(ctx context.Context, emailAddress, otp string) (string, error) {
	redisKey := fmt.Sprintf("admin:reset_otp:%s", emailAddress)
	storedOtp, err := s.rdb.Get(ctx, redisKey).Result()
	if err == goredis.Nil {
		return "", apperrors.ErrInvalidOTP
	} else if err != nil {
		return "", err
	}

	if storedOtp != otp {
		return "", apperrors.ErrInvalidOTP
	}

	// OTP is verified, delete it
	_ = s.rdb.Del(ctx, redisKey).Err()

	// Generate a secure reset token
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", err
	}
	resetToken := hex.EncodeToString(tokenBytes)

	// Save token in Redis mapped to email (expires in 10 minutes)
	tokenKey := fmt.Sprintf("admin:reset_token:%s", resetToken)
	err = s.rdb.Set(ctx, tokenKey, emailAddress, 10*time.Minute).Err()
	if err != nil {
		return "", err
	}

	return resetToken, nil
}

func (s *Service) ResetPassword(ctx context.Context, resetToken, newPassword string) error {
	tokenKey := fmt.Sprintf("admin:reset_token:%s", resetToken)
	emailAddress, err := s.rdb.Get(ctx, tokenKey).Result()
	if err == goredis.Nil {
		return apperrors.New(http.StatusUnauthorized, "INVALID_RESET_TOKEN", "invalid or expired reset token")
	} else if err != nil {
		return err
	}

	// Delete token so it can only be used once
	_ = s.rdb.Del(ctx, tokenKey).Err()

	// Find the user to get their ID
	admin, _, err := s.repo.FindByEmail(ctx, emailAddress)
	if err != nil || admin == nil {
		return apperrors.ErrNotFound
	}

	// Hash new password using bcrypt
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	// Update password in DB
	err = s.repo.SetPassword(ctx, admin.ID, string(hash))
	if err != nil {
		return err
	}

	// A password reset must not leave old sessions valid — revoke them all so a
	// leaked/stolen session can't outlive the reset. Best-effort.
	s.revokeAllAdminSessions(ctx, admin.ID)

	// Log audit action
	s.LogAction(ctx, admin.ID, "account.password_reset", "admin_accounts", admin.ID, "Password reset successfully via email recovery flow", "")

	return nil
}

// revokeAllAdminSessions deletes every active Redis session for an admin
// (`session:<adminID>:*`). Best-effort: failures are logged, not fatal.
func (s *Service) revokeAllAdminSessions(ctx context.Context, adminID string) {
	pattern := fmt.Sprintf("session:%s:*", adminID)
	var cursor uint64
	for {
		keys, cur, err := s.rdb.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			s.log.Warn().Err(err).Str("admin_id", adminID).Msg("admin reset: scan sessions failed")
			return
		}
		if len(keys) > 0 {
			if err := s.rdb.Del(ctx, keys...).Err(); err != nil {
				s.log.Warn().Err(err).Str("admin_id", adminID).Msg("admin reset: delete sessions failed")
			}
		}
		cursor = cur
		if cursor == 0 {
			return
		}
	}
}

func secureRandomNumber(min, max int) int {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	val := int(b[0]) | int(b[1])<<8 | int(b[2])<<16 | int(b[3])<<24
	if val < 0 {
		val = -val
	}
	return min + val%(max-min+1)
}
