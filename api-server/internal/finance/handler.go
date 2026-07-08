package finance

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"time"

	"github.com/workspace/ride-platform/pkg/respond"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// GET /api/v1/admin/finance/ledger
func (h *Handler) GetGeneralLedger(w http.ResponseWriter, r *http.Request) {
	start := parseDateQuery(r.URL.Query().Get("start"))
	end := parseDateQuery(r.URL.Query().Get("end"))

	entries, err := h.svc.GetGeneralLedger(r.Context(), start, end)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"entries": entries})
}

// GET /api/v1/admin/finance/trial-balance
func (h *Handler) GetTrialBalance(w http.ResponseWriter, r *http.Request) {
	start := parseDateQuery(r.URL.Query().Get("start"))
	end := parseDateQuery(r.URL.Query().Get("end"))

	tb, err := h.svc.GetTrialBalance(r.Context(), start, end)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, tb)
}

// GET /api/v1/admin/finance/balance-sheet
func (h *Handler) GetBalanceSheet(w http.ResponseWriter, r *http.Request) {
	asOf := parseDateQuery(r.URL.Query().Get("as_of"))

	bs, err := h.svc.GetBalanceSheet(r.Context(), asOf)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, bs)
}

// GET /api/v1/admin/finance/export
func (h *Handler) ExportFinanceReport(w http.ResponseWriter, r *http.Request) {
	start := parseDateQuery(r.URL.Query().Get("start"))
	end := parseDateQuery(r.URL.Query().Get("end"))

	entries, err := h.svc.GetGeneralLedger(r.Context(), start, end)
	if err != nil {
		respond.Error(w, err)
		return
	}

	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", `attachment; filename="general_ledger.csv"`)

	writer := csv.NewWriter(w)
	_ = writer.Write([]string{"Date", "Transaction ID", "Account", "Description", "Debit (RWF)", "Credit (RWF)", "Reference"})

	for _, e := range entries {
		row := []string{
			e.Date.Format(time.RFC3339),
			e.TransactionID,
			e.Account,
			e.Description,
			strconv.FormatInt(e.Debit, 10),
			strconv.FormatInt(e.Credit, 10),
			e.Reference,
		}
		_ = writer.Write(row)
	}
	writer.Flush()
}

// GET /api/v1/admin/finance/staff-analytics
func (h *Handler) GetStaffAnalytics(w http.ResponseWriter, r *http.Request) {
	analytics, err := h.svc.GetStaffAnalytics(r.Context())
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, analytics)
}

func parseDateQuery(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t2, err2 := time.Parse("2006-01-02", s)
		if err2 == nil {
			return &t2
		}
		return nil
	}
	return &t
}
