package reports

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/workspace/ride-platform/pkg/respond"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// GET /api/v1/admin/reports
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	format := r.URL.Query().Get("format")
	limit := parseIntDefault(r.URL.Query().Get("limit"), 20)
	offset := parseIntDefault(r.URL.Query().Get("offset"), 0)

	reps, total, err := h.svc.List(r.Context(), status, format, limit, offset)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"reports": reps, "total": total})
}

// GET /api/v1/admin/reports/:id
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rep, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, rep)
}

// POST /api/v1/admin/reports
func (h *Handler) Generate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Template  string  `json:"template"`
		Format    string  `json:"format"`
		DateRange string  `json:"date_range"`
		CreatedBy *string `json:"created_by"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Template == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "template is required")
		return
	}
	if body.Format == "" {
		body.Format = "PDF"
	}
	rep, err := h.svc.Generate(r.Context(), body.Template, body.Format, body.DateRange, body.CreatedBy)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.Created(w, rep)
}

// GET /api/v1/admin/reports/scheduled
func (h *Handler) ListScheduled(w http.ResponseWriter, r *http.Request) {
	sched, err := h.svc.ListScheduled(r.Context())
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]interface{}{"scheduled": sched})
}

// POST /api/v1/admin/reports/scheduled
func (h *Handler) CreateScheduled(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Template   string   `json:"template"`
		Format     string   `json:"format"`
		Frequency  string   `json:"frequency"`
		Recipients []string `json:"recipients"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Template == "" || body.Frequency == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "template and frequency are required")
		return
	}
	if body.Format == "" {
		body.Format = "PDF"
	}
	sr, err := h.svc.CreateScheduled(r.Context(), body.Template, body.Format, body.Frequency, body.Recipients)
	if err != nil {
		respond.Error(w, err)
		return
	}
	respond.Created(w, sr)
}

// POST /api/v1/admin/reports/scheduled/:id/toggle
func (h *Handler) ToggleScheduled(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.svc.ToggleScheduled(r.Context(), id); err != nil {
		respond.Error(w, err)
		return
	}
	respond.NoContent(w)
}

// DELETE /api/v1/admin/reports/:id
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.svc.Delete(r.Context(), id); err != nil {
		respond.Error(w, err)
		return
	}
	respond.OK(w, map[string]string{"message": "deleted"})
}

// GET /api/v1/admin/reports/:id/download
func (h *Handler) Download(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rep, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		respond.Error(w, err)
		return
	}
	if rep.Status != "READY" {
		respond.ErrorMsg(w, http.StatusConflict, "REPORT_NOT_READY", "report is not ready for download")
		return
	}
	// In production this would redirect to a presigned S3 URL or stream the file.
	// For now return the file path in JSON so the frontend can fetch it separately.
	respond.OK(w, map[string]interface{}{"file_path": rep.FilePath, "format": rep.Format})
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
