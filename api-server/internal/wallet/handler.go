package wallet

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/workspace/ride-platform/internal/middleware"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/respond"
)

// Handler exposes wallet HTTP endpoints (customer + driver — same wallet).
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// GET /api/v1/wallet
// Returns the caller's wallet balance.
func (h *Handler) GetWallet(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, apperrors.ErrUnauthorized)
		return
	}
	wallet, err := h.svc.GetWallet(r.Context(), claims.UserID)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, wallet)
}

// GET /api/v1/wallet/transactions?limit=20&offset=0
func (h *Handler) GetTransactions(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, apperrors.ErrUnauthorized)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 {
		limit = 20
	}
	txs, err := h.svc.GetTransactions(r.Context(), claims.UserID, limit, offset)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"transactions": txs, "limit": limit, "offset": offset})
}

// POST /api/v1/wallet/top-up
// Body: { "amount_rwf": 5000, "phone_number": "078XXXXXXX" }
func (h *Handler) TopUp(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, apperrors.ErrUnauthorized)
		return
	}
	var body struct {
		AmountRWF   int64  `json:"amount_rwf"`
		PhoneNumber string `json:"phone_number"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.AmountRWF <= 0 || body.PhoneNumber == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", "amount_rwf and phone_number are required")
		return
	}
	t, err := h.svc.TopUp(r.Context(), claims.UserID, body.AmountRWF, body.PhoneNumber)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.Created(w, t)
}

// POST /api/v1/wallet/withdraw
// Body: { "amount_rwf": 5000, "phone_number": "078XXXXXXX" }
func (h *Handler) Withdraw(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, apperrors.ErrUnauthorized)
		return
	}
	var body struct {
		AmountRWF   int64  `json:"amount_rwf"`
		PhoneNumber string `json:"phone_number"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.AmountRWF <= 0 || body.PhoneNumber == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", "amount_rwf and phone_number are required")
		return
	}
	t, err := h.svc.Withdraw(r.Context(), claims.UserID, body.AmountRWF, body.PhoneNumber)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, t)
}
