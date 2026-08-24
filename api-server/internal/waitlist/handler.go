package waitlist

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-playground/validator/v10"
	"github.com/rs/zerolog/log"

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
	Role string `json:"role" validate:"required,oneof=CUSTOMER DRIVER"`
	Name string `json:"name" validate:"required"`
	// Phone is optional (mirrors Email below) — a submitter can join the
	// waitlist with just a name + area. Format is checked in Service.Submit
	// (normalizePhone), not here, since it accepts Rwandan local-format
	// numbers ("0788...") in addition to E.164, which the validator package's
	// built-in phone tags don't understand.
	Phone            *string `json:"phone,omitempty"`
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
		// The go-playground/validator error string echoes struct/field names
		// and constraint internals — fine in server logs, not something to
		// hand back verbatim to an unauthenticated public client.
		log.Warn().Err(err).Msg("waitlist: request validation failed")
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", "request validation failed")
		return
	}

	var phone string
	if body.Phone != nil {
		phone = *body.Phone
	}

	signup, created, err := h.svc.Submit(r.Context(), SubmitInput{
		Role:             body.Role,
		Name:             body.Name,
		Phone:            phone,
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

	// Uniform status + response shape for new vs. duplicate (role, phone): a
	// 201-vs-200 split, or echoing the EXISTING row's referral_code on the
	// dedupe path, would let a caller probe whether a given phone number is
	// already on the waitlist and learn another person's referral code.
	// referral_code is only ever populated on a genuine new signup.
	data := map[string]interface{}{"role": signup.Role}
	if created {
		data["referral_code"] = signup.ReferralCode
	}
	respond.JSON(w, http.StatusOK, data)
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
