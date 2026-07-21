package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/workspace/ride-platform/internal/middleware"
	"github.com/workspace/ride-platform/pkg/audit"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/respond"
)

// Handler exposes admin HTTP endpoints.
type Handler struct {
	svc   AdminService
	auth  AuthService
	audit *audit.Logger
	env   string
}

func NewHandler(svc AdminService, auth AuthService, auditLog *audit.Logger, env string) *Handler {
	return &Handler{svc: svc, auth: auth, audit: auditLog, env: env}
}

// adminCtx pulls the admin id + role off the request claims for audit entries.
func adminCtx(r *http.Request) (id, role string) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		return "", ""
	}
	return claims.UserID, claims.AdminRole
}

// ── Drivers ───────────────────────────────────────────────────────────────

// GET /api/v1/admin/drivers
func (h *Handler) ListDrivers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := paginate(r)
	drivers, total, err := h.svc.ListDrivers(r.Context(),
		q.Get("status"), q.Get("vehicle_type"), q.Get("search"), q.Get("sort"),
		limit, offset)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"drivers": drivers, "total": total, "limit": limit, "offset": offset})
}

// GET /api/v1/admin/drivers/overview
func (h *Handler) DriverOverview(w http.ResponseWriter, r *http.Request) {
	vehicleType := r.URL.Query().Get("vehicle_type")
	data, err := h.svc.DriverOverview(r.Context(), vehicleType)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, data)
}

// POST /api/v1/admin/drivers/:id/approve
func (h *Handler) ApproveDriver(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	profileID := chi.URLParam(r, "id")
	if err := h.svc.ApproveDriver(r.Context(), profileID, claims.UserID); err != nil {
		respond.Error(w, err)
		return
	}
	adminID, role := adminCtx(r)
	h.audit.Record(r.Context(), adminID, role, "driver.approve", "driver", profileID, "Approved driver application", nil)
	respond.NoContent(w)
}

// POST /api/v1/admin/drivers/:id/reject
func (h *Handler) RejectDriver(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	profileID := chi.URLParam(r, "id")
	var body struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if err := h.svc.RejectDriver(r.Context(), profileID, claims.UserID, body.Reason); err != nil {
		respond.Error(w, err)
		return
	}
	adminID, role := adminCtx(r)
	h.audit.Record(r.Context(), adminID, role, "driver.reject", "driver", profileID, "Rejected driver application", map[string]any{"reason": body.Reason})
	respond.NoContent(w)
}

// POST /api/v1/admin/drivers/:id/request-more-info
func (h *Handler) RequestDriverMoreInfo(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	profileID := chi.URLParam(r, "id")
	var body struct {
		Reason    string `json:"reason"`
		Documents []struct {
			DocumentType string `json:"document_type"`
			Comment      string `json:"comment"`
		} `json:"documents"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if err := h.svc.RequestDriverMoreInfo(r.Context(), profileID, claims.UserID, body.Reason); err != nil {
		respond.Error(w, err)
		return
	}
	adminID, role := adminCtx(r)
	meta := map[string]any{"reason": body.Reason}
	if len(body.Documents) > 0 {
		meta["documents"] = body.Documents
	}
	h.audit.Record(r.Context(), adminID, role, "driver.request_more_info", "driver", profileID, "Requested more driver onboarding info", meta)
	respond.NoContent(w)
}

// POST /api/v1/admin/drivers/:id/suspend
func (h *Handler) SuspendDriver(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	profileID := chi.URLParam(r, "id")
	var body struct {
		Reason        string `json:"reason"`
		DurationHours int    `json:"duration_hours"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.DurationHours <= 0 {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if err := h.svc.SuspendDriver(r.Context(), profileID, claims.UserID, body.Reason, body.DurationHours); err != nil {
		respond.Error(w, err)
		return
	}
	adminID, role := adminCtx(r)
	h.audit.Record(r.Context(), adminID, role, "driver.suspend", "driver", profileID, "Suspended driver", map[string]any{"reason": body.Reason, "duration_hours": body.DurationHours})
	respond.NoContent(w)
}

// POST /api/v1/admin/drivers/:id/reinstate
func (h *Handler) ReinstateDriver(w http.ResponseWriter, r *http.Request) {
	profileID := chi.URLParam(r, "id")
	if err := h.svc.ReinstateDriver(r.Context(), profileID); err != nil {
		respond.Error(w, err)
		return
	}
	adminID, role := adminCtx(r)
	h.audit.Record(r.Context(), adminID, role, "driver.reinstate", "driver", profileID, "Reinstated driver", nil)
	respond.NoContent(w)
}

// ── Customers ─────────────────────────────────────────────────────────────

// GET /api/v1/admin/customers/overview
func (h *Handler) CustomerOverview(w http.ResponseWriter, r *http.Request) {
	data, err := h.svc.CustomerOverview(r.Context())
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, data)
}

// GET /api/v1/admin/customers
func (h *Handler) ListCustomers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := paginate(r)
	customers, total, err := h.svc.ListCustomers(r.Context(),
		q.Get("status"), q.Get("search"), q.Get("sort"),
		limit, offset)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"customers": customers, "total": total, "limit": limit, "offset": offset})
}

// GET /api/v1/admin/customers/:id
func (h *Handler) GetCustomer(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "id")
	customer, err := h.svc.GetCustomer(r.Context(), userID)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, customer)
}

// GET /api/v1/admin/users  (kept for backwards compat — delegates to ListCustomers)
func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) {
	h.ListCustomers(w, r)
}

// POST /api/v1/admin/users/:id/suspend
func (h *Handler) SuspendUser(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "id")
	var body struct {
		DurationHours int `json:"duration_hours"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.DurationHours <= 0 {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if err := h.svc.SuspendUser(r.Context(), userID, body.DurationHours); err != nil {
		respond.Error(w, err)
		return
	}
	adminID, role := adminCtx(r)
	h.audit.Record(r.Context(), adminID, role, "customer.suspend", "customer", userID, "Suspended customer", map[string]any{"duration_hours": body.DurationHours})
	respond.NoContent(w)
}

// POST /api/v1/admin/customers/:id/reinstate
func (h *Handler) ReinstateUser(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "id")
	if err := h.svc.ReinstateUser(r.Context(), userID); err != nil {
		respond.Error(w, err)
		return
	}
	adminID, role := adminCtx(r)
	h.audit.Record(r.Context(), adminID, role, "customer.reinstate", "customer", userID, "Reinstated customer", nil)
	respond.NoContent(w)
}

// ── Rides ─────────────────────────────────────────────────────────────────

// GET /api/v1/admin/rides
func (h *Handler) ListRides(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := paginate(r)
	rides, total, err := h.svc.ListRides(r.Context(),
		q.Get("status"), q.Get("transport_type"), q.Get("search"),
		limit, offset)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"rides": rides, "total": total, "limit": limit, "offset": offset})
}

// GET /api/v1/admin/rides/:id
func (h *Handler) GetRide(w http.ResponseWriter, r *http.Request) {
	rideID := chi.URLParam(r, "id")
	ride, err := h.svc.GetRide(r.Context(), rideID)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, ride)
}

// ── Negotiations ──────────────────────────────────────────────────────────

// GET /api/v1/admin/negotiations/stats
func (h *Handler) NegotiationsStats(w http.ResponseWriter, r *http.Request) {
	data, err := h.svc.NegotiationsStats(r.Context())
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, data)
}

// GET /api/v1/admin/negotiations
func (h *Handler) ListNegotiations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := paginate(r)
	negs, total, err := h.svc.ListNegotiations(r.Context(),
		q.Get("status"), q.Get("search"),
		limit, offset)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"negotiations": negs, "total": total, "limit": limit, "offset": offset})
}

// ── Revenue / transactions ────────────────────────────────────────────────

// GET /api/v1/admin/revenue/kpis
func (h *Handler) RevenueKPIs(w http.ResponseWriter, r *http.Request) {
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "today"
	}
	data, err := h.svc.RevenueKPIs(r.Context(), period)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, data)
}

// GET /api/v1/admin/revenue/transactions
func (h *Handler) ListTransactions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := paginate(r)
	txns, total, err := h.svc.ListTransactions(r.Context(),
		q.Get("status"), q.Get("sort"),
		limit, offset)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"transactions": txns, "total": total, "limit": limit, "offset": offset})
}

// ── Safety flags ──────────────────────────────────────────────────────────

// GET /api/v1/admin/flags/gps-anomalies
func (h *Handler) GPSAnomalies(w http.ResponseWriter, r *http.Request) {
	data, err := h.svc.GPSAnomalies(r.Context(), 200)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, data)
}

// GET /api/v1/admin/flags/device-collisions
func (h *Handler) DeviceCollisions(w http.ResponseWriter, r *http.Request) {
	data, err := h.svc.DeviceCollisions(r.Context())
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, data)
}

// POST /api/v1/admin/drivers
func (h *Handler) CreateDriver(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	var body struct {
		FullName        string `json:"full_name"`
		Phone           string `json:"phone"`
		TransportType   string `json:"transport_type"`
		VehiclePlate    string `json:"vehicle_plate"`
		LicenseNumber   string `json:"license_number"`
		DateOfBirth     string `json:"date_of_birth"`
		Province        string `json:"province"`
		District        string `json:"district"`
		Sector          string `json:"sector"`
		Cell            string `json:"cell"`
		Village         string `json:"village"`
		City            string `json:"city"`
		MomoProvider    string `json:"momo_provider"`
		MomoPayCode     string `json:"momo_pay_code"`
		MerchantPayCode string `json:"merchant_pay_code"`
		ProfileImageURL string `json:"profile_image_url"`
		PassengerSeats  *int   `json:"passenger_seats"`
		LoadCapacityKg  *int   `json:"load_capacity_kg"`
		Documents       []struct {
			DocumentType string `json:"document_type"`
			FileURL      string `json:"file_url"`
		} `json:"documents"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON")
		return
	}
	// Required fields — mirrors mobile onboarding step 0 + step 1
	switch {
	case body.Phone == "":
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "phone is required")
		return
	case body.TransportType == "":
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "transport_type is required")
		return
	case body.VehiclePlate == "":
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "vehicle_plate is required")
		return
	case body.LicenseNumber == "":
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "license_number is required")
		return
	case len(body.LicenseNumber) != 16:
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "license_number must be exactly 16 characters")
		return
	case body.DateOfBirth == "":
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "date_of_birth is required")
		return
	case body.Province == "" || body.District == "" || body.Sector == "" || body.Cell == "" || body.Village == "":
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "province, district, sector, cell, and village are required")
		return
	case body.MomoPayCode == "" && body.MerchantPayCode == "":
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "at least one of momo_pay_code or merchant_pay_code is required")
		return
	}
	docs := make([]DriverDocumentInput, 0, len(body.Documents))
	for _, d := range body.Documents {
		if d.DocumentType != "" && d.FileURL != "" {
			docs = append(docs, DriverDocumentInput{DocumentType: d.DocumentType, FileURL: d.FileURL})
		}
	}
	out, err := h.svc.CreateDriverFromAdmin(r.Context(), AdminCreateDriverInput{
		AdminUserID: claims.UserID,
		FullName:    body.FullName, Phone: body.Phone,
		TransportType: body.TransportType, VehiclePlate: body.VehiclePlate,
		LicenseNumber: body.LicenseNumber, DateOfBirth: body.DateOfBirth,
		Province: body.Province, District: body.District, Sector: body.Sector,
		Cell: body.Cell, Village: body.Village, City: body.City,
		MomoProvider: body.MomoProvider, MomoPayCode: body.MomoPayCode,
		MerchantPayCode: body.MerchantPayCode, ProfileImageURL: body.ProfileImageURL,
		PassengerSeats: body.PassengerSeats, LoadCapacityKg: body.LoadCapacityKg,
		Documents: docs,
	})
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.Created(w, out)
}

// POST /api/v1/admin/drivers/:id/force-offline
func (h *Handler) ForceDriverOffline(w http.ResponseWriter, r *http.Request) {
	profileID := chi.URLParam(r, "id")
	if err := h.svc.ForceDriverOffline(r.Context(), profileID); err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]string{"message": "driver forced offline"})
}

// GET /api/v1/admin/drivers/:id
func (h *Handler) GetDriver(w http.ResponseWriter, r *http.Request) {
	profileID := chi.URLParam(r, "id")
	driver, err := h.svc.GetDriver(r.Context(), profileID)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, driver)
}

// GET /api/v1/admin/drivers/:id/referrals
func (h *Handler) GetDriverReferrals(w http.ResponseWriter, r *http.Request) {
	profileID := chi.URLParam(r, "id")
	referrals, err := h.svc.GetDriverReferrals(r.Context(), profileID)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, referrals)
}

// PATCH /api/v1/admin/drivers/:id
func (h *Handler) UpdateDriver(w http.ResponseWriter, r *http.Request) {
	profileID := chi.URLParam(r, "id")
	var fields map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&fields); err != nil || len(fields) == 0 {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if err := h.svc.UpdateDriver(r.Context(), profileID, fields); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// DELETE /api/v1/admin/drivers/:id
func (h *Handler) DeleteDriver(w http.ResponseWriter, r *http.Request) {
	profileID := chi.URLParam(r, "id")
	if err := h.svc.DeleteDriver(r.Context(), profileID); err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]string{"message": "deleted"})
}

// PATCH /api/v1/admin/drivers/:id/verify  (unified approve/reject)
func (h *Handler) VerifyDriver(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	profileID := chi.URLParam(r, "id")
	var body struct {
		Action string `json:"action"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Action == "" {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	switch body.Action {
	case "approve":
		if err := h.svc.ApproveDriver(r.Context(), profileID, claims.UserID); err != nil {
			respond.Error(w, err)
			return
		}
		respond.OK(w, map[string]string{"message": "driver approved"})
	case "reject":
		if body.Reason == "" {
			respond.ErrorMsg(w, http.StatusBadRequest, "REASON_REQUIRED", "reason is required for rejection")
			return
		}
		if err := h.svc.RejectDriver(r.Context(), profileID, claims.UserID, body.Reason); err != nil {
			respond.Error(w, err)
			return
		}
		respond.OK(w, map[string]string{"message": "driver rejected"})
	default:
		respond.ErrorMsg(w, http.StatusBadRequest, "INVALID_ACTION", "action must be approve or reject")
	}
}

// PATCH /api/v1/admin/drivers/:id/status  (unified suspend/reinstate)
func (h *Handler) UpdateDriverStatus(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	profileID := chi.URLParam(r, "id")
	var body struct {
		Status        string `json:"status"`
		Reason        string `json:"reason"`
		DurationHours int    `json:"duration_hours"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Status == "" {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	switch body.Status {
	case "Suspended":
		if body.DurationHours <= 0 {
			body.DurationHours = 24
		}
		if err := h.svc.SuspendDriver(r.Context(), profileID, claims.UserID, body.Reason, body.DurationHours); err != nil {
			respond.Error(w, err)
			return
		}
	case "Active":
		if err := h.svc.ReinstateDriver(r.Context(), profileID); err != nil {
			respond.Error(w, err)
			return
		}
	default:
		respond.ErrorMsg(w, http.StatusBadRequest, "INVALID_STATUS", "status must be Active or Suspended")
		return
	}
	respond.OK(w, map[string]string{"status": body.Status})
}

// PATCH /api/v1/admin/customers/:id
func (h *Handler) UpdateCustomer(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "id")
	var body struct {
		Status string `json:"status"`
		Notes  string `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if err := h.svc.UpdateCustomer(r.Context(), userID, body.Status, body.Notes); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// PATCH /api/v1/admin/customers/:id/ban
func (h *Handler) BanCustomer(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "id")
	var body struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Reason == "" {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if err := h.svc.BanCustomer(r.Context(), userID, body.Reason); err != nil {
		respond.Error(w, err)
		return
	}
	adminID, role := adminCtx(r)
	h.audit.Record(r.Context(), adminID, role, "customer.ban", "customer", userID, "Banned customer", map[string]any{"reason": body.Reason})
	respond.OK(w, map[string]string{"status": "Banned"})
}

// GET /api/v1/admin/rides/live/stats
func (h *Handler) LiveRidesStats(w http.ResponseWriter, r *http.Request) {
	data, err := h.svc.LiveRidesStats(r.Context())
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, data)
}

// GET /api/v1/admin/rides/live
func (h *Handler) ListLiveRides(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := paginate(r)
	rides, total, err := h.svc.ListLiveRides(r.Context(),
		q.Get("status"), q.Get("district"), q.Get("search"),
		limit, offset)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"rides": rides, "total": total})
}

// GET /api/v1/admin/rides/live/:id
func (h *Handler) GetLiveRide(w http.ResponseWriter, r *http.Request) {
	rideID := chi.URLParam(r, "id")
	ride, err := h.svc.GetLiveRide(r.Context(), rideID)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, ride)
}

// POST /api/v1/admin/rides/live/:id/intervene
func (h *Handler) InterveneRide(w http.ResponseWriter, r *http.Request) {
	rideID := chi.URLParam(r, "id")
	var body struct {
		Action string `json:"action"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Action == "" {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if err := h.svc.InterveneRide(r.Context(), rideID, body.Action, body.Reason); err != nil {
		respond.Error(w, err)
		return
	}
	adminID, role := adminCtx(r)
	h.audit.Record(r.Context(), adminID, role, "ride.intervene", "ride", rideID, "Intervened on live ride", map[string]any{"action": body.Action, "reason": body.Reason})
	respond.OK(w, map[string]string{"message": "action applied"})
}

// GET /api/v1/admin/negotiations/:id
func (h *Handler) GetNegotiation(w http.ResponseWriter, r *http.Request) {
	rideID := chi.URLParam(r, "id")
	neg, err := h.svc.GetNegotiation(r.Context(), rideID)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, neg)
}

// GET /api/v1/admin/revenue  (unified)
func (h *Handler) Revenue(w http.ResponseWriter, r *http.Request) {
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "month"
	}
	data, err := h.svc.Revenue(r.Context(), period)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, data)
}

// POST /api/v1/admin/revenue/payouts/disburse
func (h *Handler) DisbursePayouts(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TransactionIDs []string `json:"transactionIds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.TransactionIDs) == 0 {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	count, total, err := h.svc.DisbursePayouts(r.Context(), body.TransactionIDs)
	if err != nil {
		respond.Error(w, err)
		return
	}
	adminID, role := adminCtx(r)
	h.audit.Record(r.Context(), adminID, role, "revenue.disburse", "payout", "", "Disbursed driver payouts", map[string]any{"transaction_ids": body.TransactionIDs, "total": total, "count": count})
	respond.OK(w, map[string]interface{}{"disbursed": count, "totalAmount": total})
}

// ── Account assist ───────────────────────────────────────────────────────

// POST /api/v1/admin/customers/:id/clear-otp-lockout
func (h *Handler) ClearOTPLockout(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "id")
	if err := h.svc.ClearOTPLockout(r.Context(), userID); err != nil {
		respond.Error(w, err)
		return
	}
	adminID, role := adminCtx(r)
	h.audit.Record(r.Context(), adminID, role, "account.clear_otp_lockout", "user", userID, "Cleared OTP rate-limit lockout", nil)
	respond.OK(w, map[string]string{"message": "OTP lockout cleared"})
}

// POST /api/v1/admin/drivers/:id/clear-gps-flags
func (h *Handler) ClearGPSFlags(w http.ResponseWriter, r *http.Request) {
	profileID := chi.URLParam(r, "id")
	if err := h.svc.ClearGPSFlags(r.Context(), profileID); err != nil {
		respond.Error(w, err)
		return
	}
	adminID, role := adminCtx(r)
	h.audit.Record(r.Context(), adminID, role, "account.clear_gps_flags", "driver", profileID, "Cleared GPS anomaly flags", nil)
	respond.OK(w, map[string]string{"message": "GPS flags cleared"})
}

// POST /api/v1/admin/users/:id/clear-device-collision
func (h *Handler) ClearDeviceCollisionFlag(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "id")
	var body struct {
		DeviceID string `json:"device_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.DeviceID == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "device_id is required")
		return
	}
	if err := h.svc.ClearDeviceCollisionFlag(r.Context(), userID, body.DeviceID); err != nil {
		respond.Error(w, err)
		return
	}
	adminID, role := adminCtx(r)
	h.audit.Record(r.Context(), adminID, role, "account.clear_device_collision", "user", userID, "Cleared device collision flag", map[string]any{"device_id": body.DeviceID})
	respond.OK(w, map[string]string{"message": "device collision flag cleared"})
}

// GET /api/v1/admin/users/:id/timeline
func (h *Handler) GetAccountTimeline(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "id")
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	data, err := h.svc.GetAccountTimeline(r.Context(), userID, limit)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, data)
}

// POST /api/v1/admin/drivers/:id/documents
func (h *Handler) UploadDriverDocument(w http.ResponseWriter, r *http.Request) {
	profileID := chi.URLParam(r, "id")
	var body struct {
		DocumentType string `json:"document_type"`
		FileURL      string `json:"file_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON")
		return
	}
	if err := h.svc.UpsertDriverDocument(r.Context(), profileID, body.DocumentType, body.FileURL); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// ── Helpers ───────────────────────────────────────────────────────────────

// AuthService is the subset of auth.Service used by the admin handler.
type AuthService interface {
	InitiateOTP(ctx context.Context, phone, purpose, deviceID, platform, fullName string, email *string) (string, error)
	VerifyOTPCode(ctx context.Context, phone, code string) error
}

// POST /api/v1/admin/drivers/send-otp
// Sends a 6-digit OTP to the driver's phone number for verification.
func (h *Handler) SendDriverOTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Phone string `json:"phone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Phone == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "phone is required")
		return
	}
	devOTP, err := h.auth.InitiateOTP(r.Context(), body.Phone, "ADMIN_DRIVER_VERIFY", "admin", "web", "", nil)
	if err != nil {
		respond.ErrorMsg(w, http.StatusInternalServerError, "OTP_SEND_FAILED", "failed to send OTP")
		return
	}
	if h.env != "production" && devOTP != "" {
		respond.OK(w, map[string]string{"dev_otp": devOTP})
		return
	}
	respond.NoContent(w)
}

// POST /api/v1/admin/drivers/verify-otp
// Verifies the OTP submitted by the admin for the driver's phone.
func (h *Handler) VerifyDriverOTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Phone string `json:"phone"`
		OTP   string `json:"otp"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Phone == "" || body.OTP == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "phone and otp are required")
		return
	}
	if err := h.auth.VerifyOTPCode(r.Context(), body.Phone, body.OTP); err != nil {
		respond.ErrorMsg(w, http.StatusUnauthorized, "INVALID_OTP", "invalid or expired OTP")
		return
	}
	respond.OK(w, map[string]string{"status": "verified"})
}

// GET /api/v1/admin/launch-readiness
func (h *Handler) LaunchReadiness(w http.ResponseWriter, r *http.Request) {
	data, err := h.svc.LaunchReadiness(r.Context())
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, data)
}

// POST /api/v1/admin/notifications
func (h *Handler) CreateNotificationCampaign(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title          string `json:"title"`
		Body           string `json:"body"`
		Audience       string `json:"audience"`
		TargetDriverID string `json:"target_driver_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}

	adminID, role := adminCtx(r)
	campaign, err := h.svc.CreateNotificationCampaign(r.Context(), body.Title, body.Body, body.Audience, adminID, body.TargetDriverID)
	if err != nil {
		respond.Error(w, err)
		return
	}

	h.audit.Record(r.Context(), adminID, role, "notification.send", "admin_notifications", campaign["id"].(string), "Sent notification campaign", map[string]any{
		"title":            body.Title,
		"audience":         body.Audience,
		"target_driver_id": body.TargetDriverID,
	})

	respond.Created(w, campaign)
}

// POST /api/v1/admin/drivers/:id/notify
func (h *Handler) NotifyDriver(w http.ResponseWriter, r *http.Request) {
	driverID := chi.URLParam(r, "id")
	var body struct {
		Title  string `json:"title"`
		Body   string `json:"body"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}

	adminID, role := adminCtx(r)
	result, err := h.svc.NotifyDriver(r.Context(), driverID, body.Title, body.Body, body.Reason, adminID)
	if err != nil {
		respond.Error(w, err)
		return
	}

	h.audit.Record(r.Context(), adminID, role, "driver.notify", "driver_profiles", driverID, "Sent direct notification to driver", map[string]any{
		"title":  body.Title,
		"reason": body.Reason,
	})

	respond.Created(w, result)
}

// GET /api/v1/admin/notifications
func (h *Handler) ListNotificationCampaigns(w http.ResponseWriter, r *http.Request) {
	limit, offset := paginate(r)
	campaigns, total, err := h.svc.ListNotificationCampaigns(r.Context(), limit, offset)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{
		"notifications": campaigns,
		"total":         total,
	})
}

// DELETE /api/v1/admin/notifications/:id
func (h *Handler) DeleteNotificationCampaign(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.svc.DeleteNotificationCampaign(r.Context(), id); err != nil {
		respond.Error(w, err)
		return
	}

	adminID, role := adminCtx(r)
	h.audit.Record(r.Context(), adminID, role, "notification.delete", "admin_notifications", id, "Deleted notification campaign", nil)

	respond.OK(w, map[string]string{"message": "deleted"})
}

func paginate(r *http.Request) (int, int) {
	limit := 20
	offset := 0
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, _ := strconv.Atoi(l); n > 0 && n <= 500 {
			limit = n
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if n, _ := strconv.Atoi(o); n >= 0 {
			offset = n
		}
	}
	return limit, offset
}
