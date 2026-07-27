package auth

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"github.com/go-playground/validator/v10"
	"github.com/rs/zerolog/log"

	"github.com/workspace/ride-platform/internal/middleware"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/respond"
)

var validate = validator.New()

// driverOfflineOnLogout is the subset of driver.Service needed by the logout handler.
// Using an interface avoids an import cycle (auth → driver is fine; keeping it minimal).
type driverOfflineOnLogout interface {
	ForceOffline(ctx context.Context, userID string) error
}

// Handler exposes auth HTTP endpoints.
type Handler struct {
	svc       *Service
	env       string // "development" | "production"
	driverSvc driverOfflineOnLogout
}

func NewHandler(svc *Service, env string) *Handler {
	return &Handler{svc: svc, env: env}
}

// SetDriverService wires the driver service so logout can force the driver offline.
// Called after both services are constructed in main to avoid an import cycle.
func (h *Handler) SetDriverService(svc driverOfflineOnLogout) {
	h.driverSvc = svc
}

// POST /api/v1/auth/register
// Accepts phone_number, full_name, device_id, platform. Sends OTP. Returns 204.
func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PhoneNumber string  `json:"phone_number" validate:"required,e164"`
		FullName    string  `json:"full_name"    validate:"omitempty,min=2"`
		Email       *string `json:"email"`
		DeviceID    string  `json:"device_id"    validate:"required"`
		Platform    string  `json:"platform"     validate:"required,oneof=ios android"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if err := validate.Struct(body); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", err.Error())
		return
	}

	r.Header.Set("X-Phone-Number", body.PhoneNumber)

	devOTP, err := h.svc.InitiateOTP(r.Context(), body.PhoneNumber, PurposeRegistration, body.DeviceID, body.Platform, body.FullName, body.Email)
	if err != nil {
		respond.Error(w, err)
		return
	}

	// In development, echo the OTP back so the mobile app can auto-fill the input
	// without requiring developers to read Docker logs.
	// NEVER do this in production.
	if h.env != "production" && devOTP != "" {
		respond.OK(w, map[string]string{"dev_otp": devOTP})
		return
	}

	respond.NoContent(w)
}

// POST /api/v1/auth/verify-otp
func (h *Handler) VerifyOTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PhoneNumber string `json:"phone_number" validate:"required"`
		OTP         string `json:"otp"          validate:"required,len=6"`
		DeviceID    string `json:"device_id"    validate:"required"`
		Platform    string `json:"platform"`
		AppVersion  string `json:"app_version"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if err := validate.Struct(body); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", err.Error())
		return
	}

	purpose := PurposeRegistration
	ipAddr := realIP(r)

	tokens, user, err := h.svc.VerifyOTP(r.Context(), body.PhoneNumber, body.OTP, purpose, body.DeviceID, body.Platform, body.AppVersion, ipAddr)
	if err != nil {
		respond.Error(w, err)
		return
	}

	respond.OK(w, map[string]interface{}{
		"access_token":  tokens.AccessToken,
		"refresh_token": tokens.RefreshToken,
		"role_state":    user.RoleState,
		"user_id":       user.ID,
	})
}

// POST /api/v1/auth/login
// Phone-only login (no OTP). The number was verified at registration, so a
// returning user signs in on any device with just the number. 404 if the
// number was never registered (the app routes the user to register).
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PhoneNumber string `json:"phone_number" validate:"required,e164"`
		DeviceID    string `json:"device_id"    validate:"required"`
		Platform    string `json:"platform"`
		AppVersion  string `json:"app_version"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if err := validate.Struct(body); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", err.Error())
		return
	}

	tokens, user, err := h.svc.Login(r.Context(), body.PhoneNumber, body.DeviceID, body.Platform, body.AppVersion, realIP(r))
	if err != nil {
		respond.Error(w, err)
		return
	}

	respond.OK(w, map[string]interface{}{
		"access_token":  tokens.AccessToken,
		"refresh_token": tokens.RefreshToken,
		"role_state":    user.RoleState,
		"user_id":       user.ID,
	})
}

// POST /api/v1/customer/phone/change/request  (authenticated)
// Sends an OTP to a new phone number the signed-in user wants to switch to.
func (h *Handler) RequestPhoneChange(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	var body struct {
		NewPhone string `json:"new_phone" validate:"required,e164"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if err := validate.Struct(body); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", err.Error())
		return
	}

	devOTP, err := h.svc.RequestPhoneChange(r.Context(), claims.UserID, body.NewPhone)
	if err != nil {
		respond.Error(w, err)
		return
	}
	if h.env != "production" && devOTP != "" {
		respond.OK(w, map[string]string{"dev_otp": devOTP})
		return
	}
	respond.NoContent(w)
}

// POST /api/v1/customer/phone/change/verify  (authenticated)
// Validates the OTP and swaps the signed-in user's phone number.
func (h *Handler) VerifyPhoneChange(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	var body struct {
		NewPhone string `json:"new_phone" validate:"required,e164"`
		OTP      string `json:"otp"       validate:"required,len=6"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if err := validate.Struct(body); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", err.Error())
		return
	}

	if err := h.svc.VerifyPhoneChange(r.Context(), claims.UserID, body.NewPhone, body.OTP); err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]string{"phone_number": body.NewPhone})
}

// POST /api/v1/auth/refresh
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RefreshToken string `json:"refresh_token" validate:"required"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if err := validate.Struct(body); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", err.Error())
		return
	}

	tokens, err := h.svc.RefreshTokens(r.Context(), body.RefreshToken)
	if err != nil {
		respond.Error(w, err)
		return
	}

	respond.OK(w, tokens)
}

// POST /api/v1/auth/logout
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, apperrors.ErrUnauthorized)
		return
	}

	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	if err := h.svc.Logout(r.Context(), claims.UserID, claims.ID, body.RefreshToken); err != nil {
		respond.Error(w, err)
		return
	}

	// If the user is a driver, force them offline and clean up Redis state.
	// This runs even if they still have an "active" ride in Redis (stale key)
	// so logout is never blocked by ghost Redis state.
	if h.driverSvc != nil {
		go func() {
			if err := h.driverSvc.ForceOffline(context.Background(), claims.UserID); err != nil {
				log.Error().Err(err).Str("user_id", claims.UserID).Msg("failed to force-offline driver on logout")
			}
		}()
	}

	respond.NoContent(w)
}

// DELETE /api/v1/auth/account
func (h *Handler) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, apperrors.ErrUnauthorized)
		return
	}

	if err := h.svc.DeleteAccount(r.Context(), claims.UserID); err != nil {
		respond.Error(w, err)
		return
	}

	// Force the driver offline to clean up geo-indexes and matching state
	if h.driverSvc != nil {
		go func() {
			if err := h.driverSvc.ForceOffline(context.Background(), claims.UserID); err != nil {
				log.Error().Err(err).Str("user_id", claims.UserID).Msg("failed to force-offline deleted user")
			}
		}()
	}

	respond.NoContent(w)
}

// POST /api/v1/auth/ws-ticket
func (h *Handler) WSTicket(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, apperrors.ErrUnauthorized)
		return
	}

	ticket, err := h.svc.GenerateWSTicket(r.Context(), claims.UserID, claims.RoleState)
	if err != nil {
		respond.Error(w, err)
		return
	}

	respond.OK(w, map[string]string{"ticket": ticket})
}

func realIP(r *http.Request) string {
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	if ip := r.Header.Get("X-Forwarded-For"); ip != "" {
		// X-Forwarded-For can be a comma-separated list; take the first.
		if idx := strings.Index(ip, ","); idx != -1 {
			ip = ip[:idx]
		}
		return strings.TrimSpace(ip)
	}
	// r.RemoteAddr is "host:port" — strip the port for a valid INET value.
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}
