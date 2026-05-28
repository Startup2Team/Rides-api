package team

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"

	"github.com/workspace/ride-platform/config"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

const (
	totpIssuer       = "Taravelis Admin"
	preAuthTokenType = "pre_auth"
	preAuthExpiry    = 5 * time.Minute
	backupCodeCount  = 10
)

// LoginResult is returned by Login. When TwoFactorRequired is true, the
// caller must call Verify2FA or VerifyBackupCode using PreAuthToken.
type LoginResult struct {
	AccessToken      string       `json:"access_token,omitempty"`
	PreAuthToken     string       `json:"pre_auth_token,omitempty"`
	TwoFactorRequired bool        `json:"two_factor_required"`
	Admin            *AdminAccount `json:"admin,omitempty"`
}

type Service struct {
	repo *Repository
	cfg  *config.Config
}

func NewService(repo *Repository, cfg *config.Config) *Service {
	return &Service{repo: repo, cfg: cfg}
}

// ── Admin management ──────────────────────────────────────────────────────

func (s *Service) ListAdmins(ctx context.Context, status, roleID, search string) ([]*AdminAccount, error) {
	return s.repo.ListAdmins(ctx, status, roleID, search)
}

func (s *Service) Invite(ctx context.Context, name, email, roleID string) (*AdminAccount, error) {
	return s.repo.Invite(ctx, name, email, roleID)
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

func (s *Service) ListRoles(ctx context.Context) ([]*Role, error) {
	return s.repo.ListRoles(ctx)
}

func (s *Service) UpdateName(ctx context.Context, id, name string) error {
	return s.repo.UpdateName(ctx, id, name)
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

// Login validates email+password. If 2FA is enabled it returns a short-lived
// pre_auth_token that must be exchanged via Verify2FA or VerifyBackupCode.
// If 2FA is not enabled it returns a full access token immediately.
func (s *Service) Login(ctx context.Context, email, password string) (*LoginResult, error) {
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

	// 2FA is enabled — issue a short-lived pre-auth token instead of the real access token.
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

	// 2FA not enabled — issue access token directly.
	token, err := s.issueAccessToken(admin.ID)
	if err != nil {
		return nil, apperrors.ErrInternal
	}
	go s.repo.TouchLastActive(context.Background(), admin.ID)
	return &LoginResult{
		AccessToken:       token,
		TwoFactorRequired: false,
		Admin:             admin,
	}, nil
}

// Verify2FA completes login by verifying a TOTP code against the pre-auth token.
func (s *Service) Verify2FA(ctx context.Context, preAuthToken, code string) (*LoginResult, error) {
	adminID, err := s.validatePreAuthToken(preAuthToken)
	if err != nil {
		return nil, err
	}

	secret, err := s.repo.GetTOTPSecret(ctx, adminID)
	if err != nil || secret == nil || *secret == "" {
		return nil, apperrors.New(http.StatusConflict, "2FA_NOT_SETUP", "2FA is not configured for this account")
	}

	if !totp.Validate(code, *secret) {
		return nil, apperrors.New(http.StatusUnauthorized, "INVALID_2FA_CODE", "authenticator code is invalid or expired")
	}

	admin, _, err := s.repo.FindByID(ctx, adminID)
	if err != nil {
		return nil, apperrors.ErrNotFound
	}

	token, err := s.issueAccessToken(adminID)
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

// VerifyBackupCode completes login using one of the ten single-use backup codes.
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

	// Mark the used code.
	codes[matched].Used = true
	if err := s.repo.SaveBackupCodes(ctx, adminID, codes); err != nil {
		return nil, apperrors.ErrInternal
	}

	admin, _, err := s.repo.FindByID(ctx, adminID)
	if err != nil {
		return nil, apperrors.ErrNotFound
	}

	token, err := s.issueAccessToken(adminID)
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

// ── 2FA setup ─────────────────────────────────────────────────────────────

// Generate2FASetup creates a fresh TOTP key and returns the secret + otpauth URI.
// The secret is NOT stored yet — Enable2FA must be called after the user verifies.
func (s *Service) Generate2FASetup(ctx context.Context, adminID string) (secret, otpauthURL string, err error) {
	admin, _, err := s.repo.FindByID(ctx, adminID)
	if err != nil {
		return "", "", apperrors.ErrNotFound
	}
	if admin.TwoFactor {
		return "", "", apperrors.New(http.StatusConflict, "2FA_ALREADY_ENABLED", "2FA is already enabled; disable it first")
	}

	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      totpIssuer,
		AccountName: admin.Email,
	})
	if err != nil {
		return "", "", apperrors.ErrInternal
	}
	return key.Secret(), key.URL(), nil
}

// Enable2FA verifies the provided TOTP code against the given secret (generated
// by Generate2FASetup), then persists the secret and returns 10 plaintext backup codes.
// The plain codes are shown once and never stored in plaintext.
func (s *Service) Enable2FA(ctx context.Context, adminID, secret, code string) ([]string, error) {
	if !totp.Validate(code, secret) {
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

// Disable2FA verifies the current password, then removes TOTP configuration.
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

// ── JWT helpers ───────────────────────────────────────────────────────────

// issueAccessToken creates a full admin access token stored in Redis.
func (s *Service) issueAccessToken(adminID string) (string, error) {
	jti := uuid.NewString()
	claims := jwt.MapClaims{
		"user_id":    adminID,
		"role_state": "ADMIN",
		"token_type": "access",
		"jti":        jti,
		"exp":        time.Now().Add(s.cfg.JWT.AccessExpiry).Unix(),
		"iat":        time.Now().Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(s.cfg.JWT.AccessSecret))
}

// issuePreAuthToken creates a 5-minute token that is only valid for the 2FA step.
// It is NOT stored in Redis so the normal Authenticate middleware will reject it.
func (s *Service) issuePreAuthToken(adminID string) (string, error) {
	claims := jwt.MapClaims{
		"user_id":    adminID,
		"token_type": preAuthTokenType,
		"exp":        time.Now().Add(preAuthExpiry).Unix(),
		"iat":        time.Now().Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(s.cfg.JWT.AccessSecret))
}

// validatePreAuthToken parses a pre_auth JWT and returns the admin ID.
func (s *Service) validatePreAuthToken(tokenStr string) (string, error) {
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, apperrors.ErrTokenInvalid
		}
		return []byte(s.cfg.JWT.AccessSecret), nil
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

// ── Backup codes ──────────────────────────────────────────────────────────

// generateBackupCodes creates 10 random codes in XXXXX-XXXXX format.
// Returns the plaintext codes (shown once to the user) and the hashed records for storage.
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
