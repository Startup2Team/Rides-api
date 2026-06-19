package rating

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"

	"github.com/workspace/ride-platform/internal/middleware"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/respond"
)

type Handler struct {
	repo *Repository
	log  zerolog.Logger
}

func NewHandler(repo *Repository, log zerolog.Logger) *Handler {
	return &Handler{repo: repo, log: log}
}

// POST /api/v1/rides/{ride_id}/rate
func (h *Handler) SubmitRating(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	rideID := chi.URLParam(r, "ride_id")

	var body struct {
		Score   int      `json:"score"`
		Comment *string  `json:"comment"`
		Tags    []string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	if body.Score < 1 || body.Score > 5 {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", "score must be 1-5")
		return
	}

	customerID, driverUserID, status, err := h.repo.FindRideParticipants(r.Context(), rideID)
	if err != nil {
		respond.Error(w, apperrors.ErrNotFound)
		return
	}
	if status != "COMPLETED" {
		respond.ErrorMsg(w, http.StatusBadRequest, "RIDE_NOT_COMPLETED", "can only rate completed rides")
		return
	}

	var raterID, ratedID, direction string
	switch claims.UserID {
	case customerID:
		raterID = customerID
		ratedID = driverUserID
		direction = "CUSTOMER_TO_DRIVER"
	case driverUserID:
		raterID = driverUserID
		ratedID = customerID
		direction = "DRIVER_TO_CUSTOMER"
	default:
		respond.Error(w, apperrors.ErrForbidden)
		return
	}

	if ratedID == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "NO_COUNTERPART", "ride has no assigned driver")
		return
	}

	rat, err := h.repo.Create(r.Context(), rideID, raterID, ratedID, direction, body.Score, body.Comment, body.Tags)
	if err != nil {
		h.log.Error().Err(err).Str("ride_id", rideID).Msg("rating: failed to insert")
		respond.Error(w, err)
		return
	}

	go h.updateCachedRating(ratedID, direction)

	respond.Created(w, rat)
}

func (h *Handler) updateCachedRating(ratedID, direction string) {
	ctx := context.Background()
	avg, _, err := h.repo.AvgScore(ctx, ratedID)
	if err != nil {
		h.log.Error().Err(err).Str("rated_id", ratedID).Msg("rating: failed to compute avg")
		return
	}
	switch direction {
	case "CUSTOMER_TO_DRIVER":
		_ = h.repo.UpdateDriverProfileRating(ctx, ratedID, avg)
	case "DRIVER_TO_CUSTOMER":
		_ = h.repo.UpdateCustomerProfileRating(ctx, ratedID, avg)
	}
}

// GET /api/v1/rides/{ride_id}/rating
func (h *Handler) GetRideRating(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	rideID := chi.URLParam(r, "ride_id")

	rat, err := h.repo.FindByRideAndRater(r.Context(), rideID, claims.UserID)
	if err != nil {
		respond.Error(w, err)
		return
	}
	if rat == nil {
		respond.Error(w, apperrors.ErrNotFound)
		return
	}
	respond.OK(w, rat)
}

// GET /api/v1/users/me/ratings
func (h *Handler) MyRatings(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	limit := 20
	offset := 0
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

	ratings, err := h.repo.ListByRated(r.Context(), claims.UserID, limit, offset)
	if err != nil {
		respond.Error(w, err)
		return
	}
	if ratings == nil {
		ratings = []*Rating{}
	}
	respond.OK(w, map[string]interface{}{
		"ratings": ratings,
		"limit":   limit,
		"offset":  offset,
	})
}
