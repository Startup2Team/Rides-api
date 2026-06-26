package auth

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"golang.org/x/crypto/bcrypt"

	"github.com/workspace/ride-platform/config"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	rkeys "github.com/workspace/ride-platform/pkg/redis"

	"github.com/workspace/ride-platform/internal/middleware"
	"github.com/workspace/ride-platform/internal/telephony"
)

const (
	PurposeRegistration = "REGISTRATION"
	PurposeLogin        = "LOGIN"
	otpLength           = 6
	otpExpiryMinutes    = 10
	// maxOTPAttempts locks a code after this many wrong guesses within its window,
	// so a 6-digit OTP can't be brute-forced even if the HTTP limiter is bypassed.
	maxOTPAttempts = 5
)

// Service handles all authentication business logic.
type Service struct {
	repo      *Repository
	redis     *goredis.Client
	telephony *telephony.Service
	cfg       *config.Config
	log       zerolog.Logger
}

func NewService(repo *Repository, rdb *goredis.Client, tel *telephony.Service, cfg *config.Config, log zerolog.Logger) *Service {
	return &Service{repo: repo, redis: rdb, telephony: tel, cfg: cfg, log: log}
}

// InitiateOTP generates a 6-digit OTP, stores a bcrypt hash, and sends via SMS.
// fullName and email are stashed in Redis so VerifyOTP can use them on first registration.
// In non-production the plaintext OTP is returned so the handler can echo it back to the
// client — eliminates the need to read Docker logs during development.
func (s *Service) InitiateOTP(ctx context.Context, phone, purpose, deviceID, platform, fullName string, email *string) (devOTP string, err error) {
	otp, err := generateOTP()
	if err != nil {
		return "", fmt.Errorf("generate otp: %w", err)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(otp), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash otp: %w", err)
	}

	expiresAt := time.Now().Add(otpExpiryMinutes * time.Minute)
	if err := s.repo.CreateOTP(ctx, phone, string(hash), purpose, expiresAt); err != nil {
		return "", fmt.Errorf("store otp: %w", err)
	}

	// Stash registration metadata in Redis (TTL matches OTP expiry).
	if purpose == PurposeRegistration && fullName != "" {
		metaKey := "otp:meta:" + phone
		s.redis.HSet(ctx, metaKey, "full_name", fullName)
		if email != nil {
			s.redis.HSet(ctx, metaKey, "email", *email)
		}
		s.redis.Expire(ctx, metaKey, otpExpiryMinutes*time.Minute)
	}

	// In non-production always print the OTP immediately so dev/test flows
	// are never blocked waiting for real SMS delivery.
	if s.cfg.Env != "production" {
		s.log.Warn().
			Str("phone", phone).
			Str("otp", otp).
			Msg("⚠️  DEV MODE — OTP (not sent via SMS)")
		devOTP = otp
	}

	if err := s.telephony.SendOTP(ctx, phone, otp); err != nil {
		s.log.Error().Err(err).Str("phone", phone).Msg("otp: sms send failed")
		if s.cfg.Env == "production" {
			return "", fmt.Errorf("sms send: %w", err)
		}
		// Non-production: log the failure but continue — OTP already printed above.
	}

	// Dev WhatsApp fallback — useful when AT SMS sandbox isn't configured but
	// you have a registered AT_WHATSAPP_SENDER number.
	if s.cfg.Env != "production" && s.cfg.AT.WhatsAppEnabled {
		if err := s.telephony.SendOTPWhatsApp(ctx, phone, otp); err != nil {
			// Non-fatal — the OTP is already in the dev logs.
			s.log.Warn().Err(err).Str("phone", phone).Msg("otp: whatsapp send failed (non-fatal in dev)")
		} else {
			s.log.Info().Str("phone", phone).Msg("otp: sent via WhatsApp (dev mode)")
		}
	}

	return devOTP, nil
}

// VerifyOTP validates the submitted OTP code and returns JWT tokens.
func (s *Service) VerifyOTP(ctx context.Context, phone, code, purpose, deviceID, platform, appVersion, ipAddr string) (*TokenPair, *User, error) {
	// Lock the code after too many wrong guesses in its window — caps brute force.
	attemptsKey := "otp:attempts:" + phone + ":" + purpose
	if n, _ := s.redis.Get(ctx, attemptsKey).Int(); n >= maxOTPAttempts {
		return nil, nil, apperrors.New(429, "OTP_LOCKED", "Too many incorrect attempts. Request a new code.")
	}

	record, err := s.repo.FindLatestOTP(ctx, phone, purpose)
	if err != nil {
		return nil, nil, err
	}

	if err := bcrypt.CompareHashAndPassword([]byte(record.OTPHash), []byte(code)); err != nil {
		// Count the failed attempt; set TTL = OTP lifetime on first failure.
		if c, e := s.redis.Incr(ctx, attemptsKey).Result(); e == nil && c == 1 {
			s.redis.Expire(ctx, attemptsKey, otpExpiryMinutes*time.Minute)
		}
		return nil, nil, apperrors.ErrInvalidOTP
	}

	// Correct code — clear the attempt counter and consume the OTP.
	s.redis.Del(ctx, attemptsKey)
	if err := s.repo.MarkOTPUsed(ctx, record.ID); err != nil {
		return nil, nil, fmt.Errorf("mark otp used: %w", err)
	}

	// Upsert user — create on first registration, return existing on login.
	user, err := s.repo.FindUserByPhone(ctx, phone)
	if err != nil {
		if err == apperrors.ErrNotFound {
			// Pull registration metadata stashed during InitiateOTP.
			var fullName *string
			var email *string
			metaKey := "otp:meta:" + phone
			if v, e := s.redis.HGet(ctx, metaKey, "full_name").Result(); e == nil && v != "" {
				fullName = &v
			}
			if v, e := s.redis.HGet(ctx, metaKey, "email").Result(); e == nil && v != "" {
				email = &v
			}
			s.redis.Del(ctx, metaKey)

			user, err = s.repo.CreateUser(ctx, phone, deviceID, platform, fullName, email)
			if err != nil {
				return nil, nil, fmt.Errorf("create user: %w", err)
			}
		} else {
			return nil, nil, err
		}
	} else {
		// Existing user — update device_id
		_ = s.repo.UpdateUserDeviceID(ctx, user.ID, deviceID)
	}

	// Reject suspended accounts before issuing tokens (auto-lifting any temp-ban
	// whose window has already elapsed).
	s.liftExpiredSuspension(ctx, user)
	if user.IsSuspended {
		return nil, nil, apperrors.New(403, "ACCOUNT_SUSPENDED", "Your account has been suspended. Contact support.")
	}

	// Log device session
	_ = s.repo.LogDeviceSession(ctx, user.ID, deviceID, platform, appVersion, ipAddr)

	// Device collision detection — same device_id on multiple accounts.
	collision, _ := s.repo.DetectDeviceCollision(ctx, deviceID, user.ID)
	if collision {
		s.log.Warn().Str("device_id", deviceID).Str("user_id", user.ID).Msg("device collision detected")
		// Auto-suspend on device collision is a production anti-fraud guard. In
		// dev/test we routinely sign into multiple accounts (customer + driver)
		// on one device/simulator, so only flag — never suspend — outside prod.
		if s.cfg.Env == "production" {
			_ = s.repo.FlagUserForReview(ctx, user.ID)
		}
	}

	tokens, err := s.issueTokenPair(ctx, user)
	if err != nil {
		return nil, nil, err
	}

	return tokens, user, nil
}

// VerifyOTPCode checks a code against the latest stored OTP for a phone.
// Used by the admin panel to verify a driver's phone without creating a session.
func (s *Service) VerifyOTPCode(ctx context.Context, phone, code string) error {
	record, err := s.repo.FindLatestOTP(ctx, phone, "ADMIN_DRIVER_VERIFY")
	if err != nil {
		return apperrors.ErrInvalidOTP
	}
	if err := bcrypt.CompareHashAndPassword([]byte(record.OTPHash), []byte(code)); err != nil {
		return apperrors.ErrInvalidOTP
	}
	return s.repo.MarkOTPUsed(ctx, record.ID)
}

// RefreshTokens validates a refresh token and issues a new access token.
func (s *Service) RefreshTokens(ctx context.Context, refreshToken string) (*TokenPair, error) {
	claims := &middleware.Claims{}
	token, err := jwt.ParseWithClaims(refreshToken, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, apperrors.ErrTokenInvalid
		}
		return []byte(s.cfg.JWT.RefreshSecret), nil
	})

	if err != nil || !token.Valid {
		return nil, apperrors.ErrTokenInvalid
	}

	if claims.TokenType != "refresh" {
		return nil, apperrors.ErrTokenInvalid
	}

	key := rkeys.K.Session(claims.UserID, claims.ID)
	val, err := s.redis.Get(ctx, key).Result()
	if err != nil || val == "revoked" {
		return nil, apperrors.ErrTokenRevoked
	}

	user, err := s.repo.FindUserByID(ctx, claims.UserID)
	if err != nil {
		return nil, err
	}

	// Suspended users cannot refresh — catches suspension that happened after last
	// login. Auto-lift an expired temp-ban first so a 24h cancellation ban ends
	// on its own without admin intervention.
	s.liftExpiredSuspension(ctx, user)
	if user.IsSuspended {
		// Revoke the session so further refresh attempts also fail immediately.
		_ = s.redis.Set(ctx, key, "revoked", s.cfg.JWT.RefreshExpiry).Err()
		return nil, apperrors.New(403, "ACCOUNT_SUSPENDED", "Your account has been suspended. Contact support.")
	}

	return s.issueTokenPair(ctx, user)
}

// liftExpiredSuspension auto-clears a temporary ban whose window has elapsed, so
// a 24h cancellation ban lifts itself without admin action. An indefinite
// suspension (suspension_until = NULL) is left untouched.
func (s *Service) liftExpiredSuspension(ctx context.Context, user *User) {
	if !user.IsSuspended || user.SuspensionUntil == nil || user.SuspensionUntil.After(time.Now()) {
		return
	}
	if err := s.repo.ClearSuspension(ctx, user.ID); err != nil {
		s.log.Warn().Err(err).Str("user_id", user.ID).Msg("auth: failed to lift expired suspension")
		return
	}
	user.IsSuspended = false
	user.SuspensionUntil = nil
}

// Logout revokes the access and refresh sessions in Redis.
func (s *Service) Logout(ctx context.Context, userID, jti string, refreshToken string) error {
	// Revoke access token session key
	accessKey := rkeys.K.Session(userID, jti)
	if err := s.redis.Set(ctx, accessKey, "revoked", s.cfg.JWT.AccessExpiry).Err(); err != nil {
		return err
	}

	// If refresh token is provided, decode and revoke its session too
	if refreshToken != "" {
		claims := &middleware.Claims{}
		token, err := jwt.ParseWithClaims(refreshToken, claims, func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, apperrors.ErrTokenInvalid
			}
			return []byte(s.cfg.JWT.RefreshSecret), nil
		})
		if err == nil && token.Valid && claims.TokenType == "refresh" && claims.UserID == userID {
			refreshKey := rkeys.K.Session(userID, claims.ID)
			if err := s.redis.Set(ctx, refreshKey, "revoked", s.cfg.JWT.RefreshExpiry).Err(); err != nil {
				return err
			}
		}
	}
	return nil
}

// ——————————————————————————————————————————————————————
// Internal helpers
// ——————————————————————————————————————————————————————

// TokenPair is returned on successful auth.
type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	RoleState    string `json:"role_state"`
}

func (s *Service) issueTokenPair(ctx context.Context, user *User) (*TokenPair, error) {
	accessJTI := uuid.New().String()
	refreshJTI := uuid.New().String()

	accessClaims := &middleware.Claims{
		UserID:      user.ID,
		RoleState:   user.RoleState,
		TokenType:   "access",
		IsSuspended: user.IsSuspended,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        accessJTI,
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(s.cfg.JWT.AccessExpiry)),
		},
	}

	accessToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims).
		SignedString([]byte(s.cfg.JWT.AccessSecret))
	if err != nil {
		return nil, fmt.Errorf("sign access token: %w", err)
	}

	refreshClaims := &middleware.Claims{
		UserID:    user.ID,
		RoleState: user.RoleState,
		TokenType: "refresh",
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        refreshJTI,
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(s.cfg.JWT.RefreshExpiry)),
		},
	}

	refreshToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims).
		SignedString([]byte(s.cfg.JWT.RefreshSecret))
	if err != nil {
		return nil, fmt.Errorf("sign refresh token: %w", err)
	}

	// Refresh-token session — checked by RefreshTokens to validate the refresh token.
	refreshSessionKey := rkeys.K.Session(user.ID, refreshJTI)
	if err := s.redis.Set(ctx, refreshSessionKey, "valid", s.cfg.JWT.RefreshExpiry).Err(); err != nil {
		return nil, fmt.Errorf("persist refresh session: %w", err)
	}

	// Access-token session — checked by the Authenticate middleware on every API call.
	// TTL matches the access token's lifetime so the key expires naturally.
	accessSessionKey := rkeys.K.Session(user.ID, accessJTI)
	if err := s.redis.Set(ctx, accessSessionKey, "valid", s.cfg.JWT.AccessExpiry).Err(); err != nil {
		return nil, fmt.Errorf("persist access session: %w", err)
	}

	return &TokenPair{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		RoleState:    user.RoleState,
	}, nil
}

// DeleteAccount anonymizes the user profile and terminates all active sessions.
func (s *Service) DeleteAccount(ctx context.Context, userID string) error {
	// 1. Scrub/anonymize user profile and related resources in the database
	if err := s.repo.AnonymizeUser(ctx, userID); err != nil {
		return err
	}

	// 2. Scan and delete all active session keys in Redis for this user
	var cursor uint64
	for {
		keys, nextCursor, err := s.redis.Scan(ctx, cursor, "session:"+userID+":*", 100).Result()
		if err == nil && len(keys) > 0 {
			s.redis.Del(ctx, keys...)
		}
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	return nil
}

func generateOTP() (string, error) {
	max := big.NewInt(1_000_000)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}
