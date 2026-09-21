package intercity

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-playground/validator/v10"

	"github.com/workspace/ride-platform/internal/middleware"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/geo"
	"github.com/workspace/ride-platform/pkg/respond"
)

var validate = validator.New()

// Handler exposes the intercity HTTP surface (§7).
//
// Role middleware is NOT ownership: middleware/role.go:36 checks only
// role_state, and the customer route group admits DRIVER_ACTIVE (so a rival
// driver needs no second account) while the driver group admits
// DRIVER_PENDING. Every handler below therefore passes claims.UserID into a
// service call whose SQL carries the ownership predicate, and the driver routes
// are additionally mounted under RequireRole(RoleDriverActive) alone.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// paginate reads limit/offset with the house defaults (ride/handler.go).
func paginate(r *http.Request) (limit, offset int) {
	limit, offset = 20, 0
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if n, err := strconv.Atoi(o); err == nil && n >= 0 {
			offset = n
		}
	}
	return limit, offset
}

func decode(w http.ResponseWriter, r *http.Request, body interface{}) bool {
	if err := json.NewDecoder(r.Body).Decode(body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return false
	}
	if err := validate.Struct(body); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", err.Error())
		return false
	}
	return true
}

// ── Customer ─────────────────────────────────────────────────────────────────

// GET /api/v1/customer/intercity/trips?corridor&date&seats
//
// Paginated, and it never returns a driver phone: driver contact belongs only
// to a confirmed passenger, at the gate (§7).
func (h *Handler) ListTrips(w http.ResponseWriter, r *http.Request) {
	limit, offset := paginate(r)

	seats := 1
	if s := r.URL.Query().Get("seats"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			seats = n
		}
	}

	trips, err := h.svc.SearchTrips(r.Context(), r.URL.Query().Get("corridor"),
		r.URL.Query().Get("date"), seats, limit, offset)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"trips": trips, "limit": limit, "offset": offset})
}

// GET /api/v1/customer/intercity/trips/{id}
func (h *Handler) GetTrip(w http.ResponseWriter, r *http.Request) {
	trip, err := h.svc.GetTrip(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, trip)
}

type holdSeatsRequest struct {
	Seats          int    `json:"seats"           validate:"required,gte=1,lte=30"`
	IdempotencyKey string `json:"idempotency_key" validate:"required,max=120"`
}

// POST /api/v1/customer/intercity/trips/{id}/hold
func (h *Handler) HoldSeats(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	var body holdSeatsRequest
	if !decode(w, r, &body) {
		return
	}

	booking, err := h.svc.HoldSeats(r.Context(), chi.URLParam(r, "id"), claims.UserID,
		body.Seats, body.IdempotencyKey)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.Created(w, booking)
}

// POST /api/v1/customer/intercity/bookings/{id}/confirm
func (h *Handler) ConfirmBooking(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	booking, err := h.svc.ConfirmBooking(r.Context(), chi.URLParam(r, "id"), claims.UserID)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, booking)
}

// GET /api/v1/customer/intercity/bookings
func (h *Handler) ListBookings(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	limit, offset := paginate(r)

	bookings, err := h.svc.ListBookings(r.Context(), claims.UserID, limit, offset)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"bookings": bookings, "limit": limit, "offset": offset})
}

// GET /api/v1/customer/intercity/bookings/{id}
func (h *Handler) GetBooking(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	booking, err := h.svc.GetBooking(r.Context(), chi.URLParam(r, "id"), claims.UserID)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, booking)
}

// DELETE /api/v1/customer/intercity/bookings/{id}
func (h *Handler) CancelBooking(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	if err := h.svc.CancelBooking(r.Context(), chi.URLParam(r, "id"), claims.UserID); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// ── Driver ───────────────────────────────────────────────────────────────────

// publishTripRequest is the driver publish payload.
//
// There is no driver_id field, deliberately: the driver is the JWT subject.
// Coordinates carry no `required` tag for the same reason as
// ride.createRideRequest — validator reads required as "reject the zero value",
// which would reject the equator and the prime meridian; geo.Point.Validate()
// in the service is the real range check.
type publishTripRequest struct {
	Corridor        string   `json:"corridor"            validate:"required,max=40"`
	VehicleID       string   `json:"vehicle_id"          validate:"required,uuid"`
	OriginName      string   `json:"origin_name"         validate:"required"`
	DestinationName string   `json:"destination_name"    validate:"required"`
	OriginLat       float64  `json:"origin_lat"          validate:"min=-90,max=90"`
	OriginLng       float64  `json:"origin_lng"          validate:"min=-180,max=180"`
	DestLat         float64  `json:"dest_lat"            validate:"min=-90,max=90"`
	DestLng         float64  `json:"dest_lng"            validate:"min=-180,max=180"`
	StagingAddress  string   `json:"staging_address"     validate:"required"`
	StagingLat      *float64 `json:"staging_lat"         validate:"omitempty,gte=-90,lte=90"`
	StagingLng      *float64 `json:"staging_lng"         validate:"omitempty,gte=-180,lte=180"`
	DepartAt        string   `json:"depart_at"           validate:"required"`
	TotalSeats      int      `json:"total_seats"         validate:"required,gte=1,lte=30"`
	PricePerSeatRWF int      `json:"price_per_seat_rwf"  validate:"required,gte=1,lte=500000"`
}

// POST /api/v1/driver/intercity/trips
func (h *Handler) PublishTrip(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	var body publishTripRequest
	if !decode(w, r, &body) {
		return
	}
	departAt, err := time.Parse(time.RFC3339, body.DepartAt)
	if err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", "depart_at must be an RFC3339 timestamp")
		return
	}

	trip, err := h.svc.PublishTrip(r.Context(), claims.UserID, PublishTripInput{
		Corridor:        body.Corridor,
		VehicleID:       body.VehicleID,
		OriginName:      body.OriginName,
		DestinationName: body.DestinationName,
		Origin:          geo.Point{Lat: body.OriginLat, Lng: body.OriginLng},
		Destination:     geo.Point{Lat: body.DestLat, Lng: body.DestLng},
		StagingAddress:  body.StagingAddress,
		StagingLat:      body.StagingLat,
		StagingLng:      body.StagingLng,
		DepartAt:        departAt,
		TotalSeats:      body.TotalSeats,
		PricePerSeatRWF: body.PricePerSeatRWF,
	})
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.Created(w, trip)
}

// GET /api/v1/driver/intercity/trips
func (h *Handler) ListDriverTrips(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	limit, offset := paginate(r)

	trips, err := h.svc.ListDriverTrips(r.Context(), claims.UserID, limit, offset)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"trips": trips, "limit": limit, "offset": offset})
}

// GET /api/v1/driver/intercity/trips/{id}/manifest
func (h *Handler) GetManifest(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	manifest, err := h.svc.GetManifest(r.Context(), chi.URLParam(r, "id"), claims.UserID)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, manifest)
}

// patchTripRequest is the §7 whitelist. Every field is a pointer so "absent"
// and "set to the zero value" are distinguishable.
type patchTripRequest struct {
	StagingAddress  *string  `json:"staging_address"`
	StagingLat      *float64 `json:"staging_lat"        validate:"omitempty,gte=-90,lte=90"`
	StagingLng      *float64 `json:"staging_lng"        validate:"omitempty,gte=-180,lte=180"`
	TotalSeats      *int     `json:"total_seats"        validate:"omitempty,gte=1,lte=30"`
	OriginName      *string  `json:"origin_name"`
	DestinationName *string  `json:"destination_name"`
	DepartAt        *string  `json:"depart_at"`
	PricePerSeatRWF *int     `json:"price_per_seat_rwf" validate:"omitempty,gte=1,lte=500000"`
}

// PATCH /api/v1/driver/intercity/trips/{id}
func (h *Handler) PatchTrip(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	var body patchTripRequest
	if !decode(w, r, &body) {
		return
	}

	in := PatchTripInput{
		StagingAddress:  body.StagingAddress,
		StagingLat:      body.StagingLat,
		StagingLng:      body.StagingLng,
		TotalSeats:      body.TotalSeats,
		OriginName:      body.OriginName,
		DestinationName: body.DestinationName,
		PricePerSeatRWF: body.PricePerSeatRWF,
	}
	if body.DepartAt != nil {
		departAt, err := time.Parse(time.RFC3339, *body.DepartAt)
		if err != nil {
			respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", "depart_at must be an RFC3339 timestamp")
			return
		}
		in.DepartAt = &departAt
	}

	trip, err := h.svc.PatchTrip(r.Context(), chi.URLParam(r, "id"), claims.UserID, in)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, trip)
}

type bookingMarkRequest struct {
	BookingID string `json:"booking_id" validate:"required,uuid"`
}

// POST /api/v1/driver/intercity/trips/{id}/board
//
// The booking_id comes from the body but is only ever used inside a statement
// that also asserts booking.trip_id = {id} and the trip's ownership, so a
// foreign booking_id mutates nothing.
func (h *Handler) BoardPassenger(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	var body bookingMarkRequest
	if !decode(w, r, &body) {
		return
	}
	if err := h.svc.BoardPassenger(r.Context(), chi.URLParam(r, "id"), body.BookingID, claims.UserID); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// POST /api/v1/driver/intercity/trips/{id}/no-show
func (h *Handler) MarkNoShow(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	var body bookingMarkRequest
	if !decode(w, r, &body) {
		return
	}
	if err := h.svc.MarkNoShow(r.Context(), chi.URLParam(r, "id"), body.BookingID, claims.UserID); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// POST /api/v1/driver/intercity/trips/{id}/start
func (h *Handler) StartTrip(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	if err := h.svc.StartTrip(r.Context(), chi.URLParam(r, "id"), claims.UserID); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// POST /api/v1/driver/intercity/trips/{id}/complete
func (h *Handler) CompleteTrip(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	if err := h.svc.CompleteTrip(r.Context(), chi.URLParam(r, "id"), claims.UserID); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

type cancelTripRequest struct {
	// REQUIRED (§7 / D4): the reason is fanned out to every passenger whose
	// booking this cancels, so there is no silent cancellation.
	CancelReason string `json:"cancel_reason" validate:"required,max=200"`
}

// DELETE /api/v1/driver/intercity/trips/{id}
func (h *Handler) CancelTrip(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	var body cancelTripRequest
	if !decode(w, r, &body) {
		return
	}
	if err := h.svc.CancelTrip(r.Context(), chi.URLParam(r, "id"), claims.UserID, body.CancelReason); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}
