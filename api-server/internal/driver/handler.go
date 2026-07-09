package driver

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
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

// Handler exposes driver HTTP endpoints.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// GET /api/v1/driver/demand-heatmap?lat=&lng=&radius_km=&window_min=
// Bucketed pickup demand so a driver can see where riders are requesting.
// lat+lng (both, optional) scope the result to radius_km around that point;
// omit them for the busiest cells platform-wide. Defaults: window 120 min,
// radius 5 km. Bounds: window 15–1440 min, radius 0.5–50 km.
func (h *Handler) DemandHeatmap(w http.ResponseWriter, r *http.Request) {
	windowMin, radiusM, center, err := parseDemandHeatmapParams(r.URL.Query())
	if err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", err.Error())
		return
	}

	cells, err := h.svc.DemandHeatmap(r.Context(), windowMin, center, radiusM)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{
		"window_minutes": windowMin,
		"radius_meters":  radiusM,
		"scoped":         center != nil,
		"points":         cells,
	})
}

// errHeatmapCoords is returned when lat/lng are present but not a valid pair.
var errHeatmapCoords = errors.New("lat and lng must both be valid coordinates")

// parseDemandHeatmapParams reads + clamps the demand-heatmap query params, kept
// pure (no *http.Request) so every bound can be unit-tested:
//   - window_min: default 120, clamped to [15, 1440] minutes
//   - radius_km:  default 5 km, clamped to [0.5, 50] km, returned as metres
//   - lat+lng:    optional, but must appear TOGETHER and be valid coordinates;
//     when present they set center for a radius-scoped query. A lone/invalid
//     coordinate is a hard error (not silently ignored) so the caller can't
//     accidentally get a platform-wide result when they meant a scoped one.
func parseDemandHeatmapParams(q url.Values) (windowMin, radiusM int, center *geo.Point, err error) {
	windowMin = 120
	if v, e := strconv.Atoi(q.Get("window_min")); e == nil && v > 0 {
		windowMin = v
	}
	if windowMin < 15 {
		windowMin = 15
	} else if windowMin > 1440 {
		windowMin = 1440
	}

	radiusM = 5000
	if v, e := strconv.ParseFloat(q.Get("radius_km"), 64); e == nil && v > 0 {
		radiusM = int(v * 1000)
	}
	if radiusM < 500 {
		radiusM = 500
	} else if radiusM > 50000 {
		radiusM = 50000
	}

	latStr, lngStr := q.Get("lat"), q.Get("lng")
	if latStr != "" || lngStr != "" {
		lat, e1 := strconv.ParseFloat(latStr, 64)
		lng, e2 := strconv.ParseFloat(lngStr, 64)
		if e1 != nil || e2 != nil || lat < -90 || lat > 90 || lng < -180 || lng > 180 {
			return 0, 0, nil, errHeatmapCoords
		}
		center = &geo.Point{Lat: lat, Lng: lng}
	}
	return windowMin, radiusM, center, nil
}

// POST /api/v1/driver/apply
func (h *Handler) Apply(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	var body struct {
		TransportType           string `json:"transport_type"   validate:"required,oneof=MOTO_BIKE CAB_TAXI HEAVY_FUSO LIGHT_HILUX TUK_TUK"`
		VehiclePlate            string `json:"vehicle_plate"    validate:"required"`
		LicenseNumber           string `json:"license_number"   validate:"required"`
		DateOfBirth             string `json:"date_of_birth"    validate:"required"` // YYYY-MM-DD
		City                    string `json:"city"             validate:"required"`
		MomoPayCode             string `json:"momo_pay_code"    validate:"required"`
		MomoProvider            string `json:"momo_provider"    validate:"required,oneof=mtn airtel"`
		Province                string `json:"province"         validate:"required"`
		District                string `json:"district"         validate:"required"`
		Sector                  string `json:"sector"           validate:"required"`
		Cell                    string `json:"cell"             validate:"required"`
		Village                 string `json:"village"          validate:"required"`
		Gender                  string `json:"gender"           validate:"omitempty,oneof=male female other"`
		PassengerSeats          *int   `json:"passenger_seats"`
		LoadCapacityKg          *int   `json:"load_capacity_kg"`
		LicenseExpiryDate       string `json:"license_expiry_date"`
		InsuranceExpiryDate     string `json:"insurance_expiry_date"`
		AuthorizationExpiryDate string `json:"authorization_expiry_date"`
		// Mobile app camelCase aliases
		LicenseExpiryDateCamel       string `json:"licenseExpiryDate"`
		InsuranceExpiryDateCamel     string `json:"insuranceExpiryDate"`
		AuthorizationExpiryDateCamel string `json:"authorizationExpiryDate"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if err := validate.Struct(body); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", err.Error())
		return
	}

	dob, err := parseFlexibleDate(body.DateOfBirth)
	if err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "INVALID_DOB", "date_of_birth is invalid")
		return
	}

	licenseExpiryStr := body.LicenseExpiryDate
	if licenseExpiryStr == "" {
		licenseExpiryStr = body.LicenseExpiryDateCamel
	}
	var licenseExpiryDate *time.Time
	if licenseExpiryStr != "" {
		parsed, err := parseFlexibleDate(licenseExpiryStr)
		if err != nil {
			respond.ErrorMsg(w, http.StatusBadRequest, "INVALID_LICENSE_EXPIRY", "license_expiry_date is invalid")
			return
		}
		licenseExpiryDate = &parsed
	}

	insuranceExpiryStr := body.InsuranceExpiryDate
	if insuranceExpiryStr == "" {
		insuranceExpiryStr = body.InsuranceExpiryDateCamel
	}
	var insuranceExpiryDate *time.Time
	if insuranceExpiryStr != "" {
		parsed, err := parseFlexibleDate(insuranceExpiryStr)
		if err != nil {
			respond.ErrorMsg(w, http.StatusBadRequest, "INVALID_INSURANCE_EXPIRY", "insurance_expiry_date is invalid")
			return
		}
		insuranceExpiryDate = &parsed
	}

	authorizationExpiryStr := body.AuthorizationExpiryDate
	if authorizationExpiryStr == "" {
		authorizationExpiryStr = body.AuthorizationExpiryDateCamel
	}
	var authorizationExpiryDate *time.Time
	if authorizationExpiryStr != "" {
		parsed, err := parseFlexibleDate(authorizationExpiryStr)
		if err != nil {
			respond.ErrorMsg(w, http.StatusBadRequest, "INVALID_AUTHORIZATION_EXPIRY", "authorization_expiry_date is invalid")
			return
		}
		authorizationExpiryDate = &parsed
	}

	profile, err := h.svc.Apply(r.Context(), ApplyInput{
		UserID:                  claims.UserID,
		TransportType:           body.TransportType,
		VehiclePlate:            body.VehiclePlate,
		LicenseNumber:           body.LicenseNumber,
		City:                    body.City,
		MomoPayCode:             body.MomoPayCode,
		MomoProvider:            body.MomoProvider,
		Province:                body.Province,
		District:                body.District,
		Sector:                  body.Sector,
		Cell:                    body.Cell,
		Village:                 body.Village,
		Gender:                  body.Gender,
		PassengerSeats:          body.PassengerSeats,
		LoadCapacityKg:          body.LoadCapacityKg,
		DateOfBirth:             dob,
		LicenseExpiryDate:       licenseExpiryDate,
		InsuranceExpiryDate:     insuranceExpiryDate,
		AuthorizationExpiryDate: authorizationExpiryDate,
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

// POST /api/v1/driver/locations
func (h *Handler) UpdateLocationsBatch(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)

	var body []BatchLocationUpdate
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}

	for _, update := range body {
		if err := validate.Struct(update); err != nil {
			respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", err.Error())
			return
		}
	}

	if err := h.svc.UpdateLocationBatch(r.Context(), claims.UserID, body); err != nil {
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

func parseFlexibleDate(dateStr string) (time.Time, error) {
	formats := []string{
		"2006-01-02",
		"02/01/2006",
		"01/02/2006",
		"2/1/2006",
		"1/2/2006",
		"2006/01/02",
		"2006/1/2",
		"02-01-2006",
		"01-02-2006",
		"2-1-2006",
		"1-2-2006",
		time.RFC3339,
	}
	var lastErr error
	for _, fmtStr := range formats {
		t, err := time.Parse(fmtStr, dateStr)
		if err == nil {
			return t, nil
		}
		lastErr = err
	}
	return time.Time{}, lastErr
}

// GET /api/v1/driver/vehicles
func (h *Handler) ListVehicles(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	list, err := h.svc.ListVehicles(r.Context(), claims.UserID)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, list)
}

// POST /api/v1/driver/vehicles
func (h *Handler) CreateVehicle(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	var body CreateVehicleInput
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if err := validate.Struct(body); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", err.Error())
		return
	}
	v, err := h.svc.CreateVehicle(r.Context(), claims.UserID, body)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.Created(w, v)
}

// PATCH /api/v1/driver/vehicles/{id}
func (h *Handler) UpdateVehicle(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	id := chi.URLParam(r, "id")
	var body UpdateVehicleInput
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	v, err := h.svc.UpdateVehicle(r.Context(), claims.UserID, id, body)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, v)
}

// DELETE /api/v1/driver/vehicles/{id}
func (h *Handler) DeleteVehicle(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	id := chi.URLParam(r, "id")
	if err := h.svc.DeleteVehicle(r.Context(), claims.UserID, id); err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]string{"status": "deleted"})
}

// POST /api/v1/driver/vehicles/{id}/activate
func (h *Handler) ActivateVehicle(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	id := chi.URLParam(r, "id")
	v, err := h.svc.ActivateVehicle(r.Context(), claims.UserID, id)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, v)
}
