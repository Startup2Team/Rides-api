package admin

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/respond"
)

const maxUploadBytes = 10 * 1024 * 1024

var allowedUploadMIME = map[string]string{
	"image/jpeg":      ".jpg",
	"image/png":       ".png",
	"image/heic":      ".heic",
	"application/pdf": ".pdf",
}

// UploadDriverFile stores a driver document locally (dev / when object storage is unavailable).
// POST /api/v1/admin/uploads/file  multipart field: file
func (h *Handler) UploadDriverFile(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "file too large or invalid multipart form")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "file is required")
		return
	}
	defer file.Close()

	contentType := header.Header.Get("Content-Type")
	if contentType == "" || contentType == "application/octet-stream" {
		contentType = mimeFromFilename(header.Filename)
	}
	ext, ok := allowedUploadMIME[contentType]
	if !ok {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", "unsupported file type")
		return
	}

	dir := filepath.Join("var", "uploads", "documents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		respond.Error(w, apperrors.ErrInternal)
		return
	}

	key, err := randomUploadKey(ext)
	if err != nil {
		respond.Error(w, apperrors.ErrInternal)
		return
	}
	destPath := filepath.Join(dir, key)
	out, err := os.Create(destPath)
	if err != nil {
		respond.Error(w, apperrors.ErrInternal)
		return
	}
	defer out.Close()

	limited := io.LimitReader(file, maxUploadBytes+1)
	n, err := io.Copy(out, limited)
	if err != nil {
		_ = os.Remove(destPath)
		respond.Error(w, apperrors.ErrInternal)
		return
	}
	if n > maxUploadBytes {
		_ = os.Remove(destPath)
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "file exceeds 10MB limit")
		return
	}

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	fileURL := fmt.Sprintf("%s://%s/api/v1/media/documents/%s", scheme, host, key)

	respond.OK(w, map[string]interface{}{
		"file_url": fileURL,
		"key":      key,
	})
}

// ServeDriverMedia serves locally stored driver documents.
// GET /api/v1/media/documents/{filename}
func (h *Handler) ServeDriverMedia(w http.ResponseWriter, r *http.Request) {
	filename := filepath.Base(strings.TrimSpace(chi.URLParam(r, "filename")))
	if filename == "" || strings.Contains(filename, "..") {
		respond.ErrorMsg(w, http.StatusBadRequest, "BAD_REQUEST", "invalid filename")
		return
	}
	path := filepath.Join("var", "uploads", "documents", filename)
	if _, err := os.Stat(path); err != nil {
		respond.Error(w, apperrors.ErrNotFound)
		return
	}
	http.ServeFile(w, r, path)
}

func randomUploadKey(ext string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b) + ext, nil
}

func mimeFromFilename(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".jpg"), strings.HasSuffix(lower, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(lower, ".png"):
		return "image/png"
	case strings.HasSuffix(lower, ".heic"):
		return "image/heic"
	case strings.HasSuffix(lower, ".pdf"):
		return "application/pdf"
	default:
		return ""
	}
}
