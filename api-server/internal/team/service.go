package team

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	goredis "github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"

	"github.com/workspace/ride-platform/config"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	rkeys "github.com/workspace/ride-platform/pkg/redis"
)

const (
	totpIssuer       = "Taravelis Admin"
	preAuthTokenType = "pre_auth"
	preAuthExpiry    = 15 * time.Minute
	backupCodeCount  = 10
)

// LoginResult is returned by Login. When TwoFactorRequired is true, the
// caller must call Verify2FA or VerifyBackupCode using PreAuthToken.
type LoginResult struct {
	AccessToken       string        `json:"access_token,omitempty"`
	PreAuthToken      string        `json:"pre_auth_token,omitempty"`
	TwoFactorRequired bool          `json:"two_factor_required"`
	Admin             *AdminAccount `json:"admin,omitempty"`
}

type Service struct {
	repo TeamRepo
	cfg  *config.Config
	rdb  *goredis.Client
}

func NewService(repo TeamRepo, cfg *config.Config, rdb *goredis.Client) *Service {
	return &Service{repo: repo, cfg: cfg, rdb: rdb}
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

func (s *Service) CreateRole(ctx context.Context, name, description string, permissions interface{}) (*Role, error) {
	return s.repo.CreateRole(ctx, name, description, permissions)
}

func (s *Service) UpdateRoleByID(ctx context.Context, roleID, name, description string, permissions interface{}) (*Role, error) {
	return s.repo.UpdateRoleByID(ctx, roleID, name, description, permissions)
}

func (s *Service) DeleteRoleByID(ctx context.Context, roleID string) error {
	return s.repo.DeleteRoleByID(ctx, roleID)
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

	token, err := s.issueAccessToken(ctx, admin.ID)
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

	token, err := s.issueAccessToken(ctx, adminID)
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

	token, err := s.issueAccessToken(ctx, adminID)
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
	return s.rdb.Set(ctx, key, "revoked", s.cfg.JWT.AccessExpiry).Err()
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
		Issuer:      totpIssuer,
		AccountName: admin.Email,
	})
	if err != nil {
		return "", "", apperrors.ErrInternal
	}
	return key.Secret(), key.URL(), nil
}

func (s *Service) Enable2FA(ctx context.Context, adminID, secret, code string) ([]string, error) {
	if s.cfg.Env == "production" {
		if !validateTOTP(code, secret) {
			return nil, apperrors.New(http.StatusUnauthorized, "INVALID_2FA_CODE", "authenticator code does not match — check the secret and try again")
		}
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
// In non-production environments an empty currentCode bypasses verification,
// allowing re-enrollment when the authenticator app is lost.
func (s *Service) ResetTOTP(ctx context.Context, adminID, currentCode string) (secret, otpauthURL string, backupCodes []string, err error) {
	existing, repoErr := s.repo.GetTOTPSecret(ctx, adminID)

	hasSecret := repoErr == nil && existing != nil && *existing != ""

	if !hasSecret && s.cfg.Env == "production" {
		// Production requires an existing secret to reset against.
		err = apperrors.New(http.StatusConflict, "2FA_NOT_SETUP", "2FA is not configured")
		return
	}

	// Validate current code only if a secret exists AND (production OR user provided a code).
	if hasSecret && (s.cfg.Env == "production" || currentCode != "") {
		if !validateTOTP(currentCode, *existing) {
			err = apperrors.New(http.StatusUnauthorized, "INVALID_2FA_CODE", "authenticator code is invalid")
			return
		}
	}
	// In development with no secret, fall through and generate a fresh one.

	admin, _, findErr := s.repo.FindByID(ctx, adminID)
	if findErr != nil {
		err = apperrors.ErrNotFound
		return
	}

	key, genErr := totp.Generate(totp.GenerateOpts{
		Issuer:      totpIssuer,
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
func (s *Service) issueAccessToken(ctx context.Context, adminID string) (string, error) {
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
	signed, err := token.SignedString([]byte(s.cfg.JWT.AccessSecret))
	if err != nil {
		return "", err
	}

	key := rkeys.K.Session(adminID, jti)
	if err := s.rdb.Set(ctx, key, "valid", s.cfg.JWT.AccessExpiry).Err(); err != nil {
		return "", fmt.Errorf("store admin session: %w", err)
	}
	return signed, nil
}

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

// validateTOTP checks the code against the secret with a ±60-second tolerance
// to handle minor phone clock drift.
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
