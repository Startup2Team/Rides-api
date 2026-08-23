package waitlist

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-playground/validator/v10"

	"github.com/workspace/ride-platform/internal/middleware"
	"github.com/workspace/ride-platform/pkg/respond"
)

var validate = validator.New()

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// submitRequest is the public POST /api/v1/waitlist payload. Named (rather
// than inline) so its validator tags are visible and testable directly —
// mirrors internal/ride's createRideRequest pattern.
type submitRequest struct {
	Role             string  `json:"role" validate:"required,oneof=CUSTOMER DRIVER"`
	Name             string  `json:"name" validate:"required"`
	Phone            string  `json:"phone" validate:"required"`
	Area             *string `json:"area,omitempty"`
	VehicleType      *string `json:"vehicle_type,omitempty" validate:"omitempty,oneof=MOTO_BIKE CAB_TAXI HEAVY_FUSO LIGHT_HILUX TUK_TUK"`
	Email            *string `json:"email,omitempty" validate:"omitempty,email"`
	ReferredBy       *string `json:"referred_by,omitempty"`
	ConsentLaunch    bool    `json:"consent_launch" validate:"required"`
	ConsentMarketing bool    `json:"consent_marketing,omitempty"`
	TurnstileToken   string  `json:"turnstile_token,omitempty"`
	Source           *string `json:"source,omitempty"`
}

// POST /api/v1/waitlist — public, no auth required.
func (h *Handler) Submit(w http.ResponseWriter, r *http.Request) {
	var body submitRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	if err := validate.Struct(body); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", err.Error())
		return
	}

	signup, created, err := h.svc.Submit(r.Context(), SubmitInput{
		Role:             body.Role,
		Name:             body.Name,
		Phone:            body.Phone,
		Email:            body.Email,
		Area:             body.Area,
		VehicleType:      body.VehicleType,
		ReferredBy:       body.ReferredBy,
		ConsentLaunch:    body.ConsentLaunch,
		ConsentMarketing: body.ConsentMarketing,
		Source:           body.Source,
		TurnstileToken:   body.TurnstileToken,
	}, middleware.TrustedIP(r))
	if err != nil {
		respond.Error(w, err)
		return
	}

	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	respond.JSON(w, status, map[string]interface{}{
		"referral_code": signup.ReferralCode,
		"role":          signup.Role,
	})
}

// GET /api/v1/admin/waitlist
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	f := ListFilter{
		Role:   r.URL.Query().Get("role"),
		Area:   r.URL.Query().Get("area"),
		Limit:  parseIntDefault(r.URL.Query().Get("limit"), 20),
		Offset: parseIntDefault(r.URL.Query().Get("offset"), 0),
	}
	signups, total, err := h.svc.List(r.Context(), f)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"signups": signups, "total": total})
}

func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return def
	}
	return n
}
