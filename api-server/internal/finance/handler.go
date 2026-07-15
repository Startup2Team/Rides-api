package finance

import (
	"fmt"
	"net/http"
	"time"

	"github.com/workspace/ride-platform/internal/export"
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

// GET /api/v1/admin/finance/export?report=ledger|trial-balance|balance-sheet&format=csv|xlsx|pdf
// Streams the requested finance report in the requested format (default:
// general ledger as CSV).
func (h *Handler) ExportFinanceReport(w http.ResponseWriter, r *http.Request) {
	start := parseDateQuery(r.URL.Query().Get("start"))
	end := parseDateQuery(r.URL.Query().Get("end"))
	format := export.Parse(r.URL.Query().Get("format"))
	report := r.URL.Query().Get("report")
	if report == "" {
		report = "ledger"
	}

	var table export.Table
	var err error
	switch report {
	case "trial-balance":
		table, err = h.svc.TrialBalanceTable(r.Context(), start, end)
	case "balance-sheet":
		table, err = h.svc.BalanceSheetTable(r.Context(), end)
	default:
		report = "general_ledger"
		table, err = h.svc.LedgerTable(r.Context(), start, end)
	}
	if err != nil {
		respond.Error(w, err)
		return
	}

	data, err := export.Encode(table, format)
	if err != nil {
		respond.Error(w, err)
		return
	}
	w.Header().Set("Content-Type", format.ContentType())
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.%s"`, report, format.Ext()))
	_, _ = w.Write(data)
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
