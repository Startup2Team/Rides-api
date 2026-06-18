package driver

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-playground/validator/v10"

	"github.com/workspace/ride-platform/internal/middleware"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/geo"
	"github.com/workspace/ride-platform/pkg/respond"
)

var validate = validator.New()

// Handler exposes driver HTTP endpoints.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// POST /api/v1/driver/apply
func (h *Handler) Apply(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	var body struct {
		TransportType  string `json:"transport_type"   validate:"required,oneof=MOTO_BIKE CAB_TAXI HEAVY_FUSO LIGHT_HILUX TUK_TUK"`
		VehiclePlate   string `json:"vehicle_plate"    validate:"required"`
		LicenseNumber  string `json:"license_number"   validate:"required"`
		DateOfBirth    string `json:"date_of_birth"    validate:"required"` // YYYY-MM-DD
		City           string `json:"city"             validate:"required"`
		MomoPayCode    string `json:"momo_pay_code"    validate:"required"`
		MomoProvider   string `json:"momo_provider"    validate:"required,oneof=mtn airtel"`
		Province       string `json:"province"         validate:"required"`
		District       string `json:"district"         validate:"required"`
		Sector         string `json:"sector"           validate:"required"`
		Cell           string `json:"cell"             validate:"required"`
		Village        string `json:"village"          validate:"required"`
		PassengerSeats *int   `json:"passenger_seats"`
		LoadCapacityKg *int   `json:"load_capacity_kg"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if err := validate.Struct(body); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", err.Error())
		return
	}

	dob, err := time.Parse("2006-01-02", body.DateOfBirth)
	if err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "INVALID_DOB", "date_of_birth must be YYYY-MM-DD")
		return
	}

	profile, err := h.svc.Apply(r.Context(), ApplyInput{
		UserID:         claims.UserID,
		TransportType:  body.TransportType,
		VehiclePlate:   body.VehiclePlate,
		LicenseNumber:  body.LicenseNumber,
		City:           body.City,
		MomoPayCode:    body.MomoPayCode,
		MomoProvider:   body.MomoProvider,
		Province:       body.Province,
		District:       body.District,
		Sector:         body.Sector,
		Cell:           body.Cell,
		Village:        body.Village,
		PassengerSeats: body.PassengerSeats,
		LoadCapacityKg: body.LoadCapacityKg,
		DateOfBirth:    dob,
	})
	if err != nil {
		respond.Error(w, err)
		return
	}

	respond.Created(w, profile)
}

// GET /api/v1/driver/profile
func (h *Handler) GetProfile(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	profile, err := h.svc.GetProfile(r.Context(), claims.UserID)
	if err != nil {
		respond.Error(w, err)
		return
	}

	respond.OK(w, profile)
}

// PUT /api/v1/driver/profile
func (h *Handler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	var body struct {
		City         *string `json:"city"`
		MomoPayCode  *string `json:"momo_pay_code"`
		MomoProvider *string `json:"momo_provider" validate:"omitempty,oneof=mtn airtel"`
		FCMToken     *string `json:"fcm_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if err := validate.Struct(body); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", err.Error())
		return
	}

	if err := h.svc.UpdateProfile(r.Context(), claims.UserID, body.City, body.MomoPayCode, body.MomoProvider, body.FCMToken); err != nil {
		respond.Error(w, err)
		return
	}

	respond.NoContent(w)
}

// POST /api/v1/driver/availability
func (h *Handler) SetAvailability(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	var body struct {
		IsOnline bool `json:"is_online"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}

	if err := h.svc.SetAvailability(r.Context(), claims.UserID, body.IsOnline); err != nil {
		respond.Error(w, err)
		return
	}

	respond.NoContent(w)
}

// POST /api/v1/driver/location
func (h *Handler) UpdateLocation(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	var body struct {
		Lat      float64  `json:"lat"       validate:"required,min=-90,max=90"`
		Lng      float64  `json:"lng"       validate:"required,min=-180,max=180"`
		SpeedKMH *float64 `json:"speed_kmh"`
		Heading  *float64 `json:"heading"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if err := validate.Struct(body); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", err.Error())
		return
	}

	update := LocationUpdate{
		Lat:      body.Lat,
		Lng:      body.Lng,
		SpeedKMH: body.SpeedKMH,
		Heading:  body.Heading,
	}

	if err := h.svc.UpdateLocation(r.Context(), claims.UserID, update); err != nil {
		respond.Error(w, err)
		return
	}

	respond.NoContent(w)
}

// POST /api/v1/driver/documents
// Accepts a document_type from the onboarding KYC set and a stored file_url
// (produced by the /uploads flow). Repeated uploads of the same type replace
// the prior file (UpsertDocument), so re-taking a photo overwrites cleanly.
func (h *Handler) UploadDocument(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	var body struct {
		DocumentType string `json:"document_type" validate:"required,oneof=LICENCE_FRONT LICENCE_BACK NATIONAL_ID_FRONT NATIONAL_ID_BACK VEHICLE_INSURANCE VEHICLE_AUTHORIZATION SELFIE"`
		FileURL      string `json:"file_url"      validate:"required,url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if err := validate.Struct(body); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", err.Error())
		return
	}

	if err := h.svc.UploadDocument(r.Context(), claims.UserID, body.DocumentType, body.FileURL); err != nil {
		respond.Error(w, err)
		return
	}

	respond.NoContent(w)
}

// GET /api/v1/driver/documents
func (h *Handler) ListDocuments(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	docs, err := h.svc.ListDocuments(r.Context(), claims.UserID)
	if err != nil {
		respond.Error(w, err)
		return
	}

	respond.OK(w, map[string]interface{}{"documents": docs})
}

// GET /api/v1/driver/earnings/daily
func (h *Handler) DailyEarnings(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	total, ridesToday, err := h.svc.GetDailyEarnings(r.Context(), claims.UserID)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"total_rwf": total, "rides_today": ridesToday, "period": "today"})
}

// GET /api/v1/driver/earnings/weekly
func (h *Handler) WeeklyEarnings(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	total, err := h.svc.GetWeeklyEarnings(r.Context(), claims.UserID)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"total_rwf": total, "period": "last_7_days"})
}

// GET /api/v1/driver/stats
func (h *Handler) Stats(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	stats, err := h.svc.GetStats(r.Context(), claims.UserID)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, stats)
}

// POST /api/v1/customer/location (nearby drivers for customer)
// transport_type is optional — omit or send "" to get all vehicle types.
func NearbyDriversHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Lat           float64 `json:"lat"            validate:"required"`
			Lng           float64 `json:"lng"            validate:"required"`
			TransportType string  `json:"transport_type" validate:"omitempty,oneof=MOTO_BIKE CAB_TAXI HEAVY_FUSO LIGHT_HILUX TUK_TUK"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			respond.Error(w, apperrors.ErrBadRequest)
			return
		}
		if err := validate.Struct(body); err != nil {
			respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", err.Error())
			return
		}

		loc := geo.Point{Lat: body.Lat, Lng: body.Lng}
		drivers, err := svc.GetNearbyDrivers(r.Context(), loc, body.TransportType)
		if err != nil {
			respond.Error(w, err)
			return
		}

		respond.OK(w, map[string]interface{}{"drivers": drivers})
	}
}

// POST /api/v1/driver/policy/accept
func (h *Handler) AcceptPolicy(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	if err := h.svc.AcceptPolicy(r.Context(), claims.UserID); err != nil {
		respond.Error(w, err)
		return
	}

	respond.NoContent(w)
}
