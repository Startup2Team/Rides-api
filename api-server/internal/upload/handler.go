package upload

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/go-chi/chi/v5"

	appcfg "github.com/workspace/ride-platform/config"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/respond"
)

const maxUploadBytes = 10 * 1024 * 1024 // 10 MB

type Handler struct {
	cfg       *appcfg.Config
	client    *s3.Client
	presigner *s3.PresignClient
	// proxy mode streams uploads/downloads through this API instead of handing
	// the client a direct presigned S3 URL. Used for MinIO in dev, where the
	// storage host is not publicly reachable by the mobile device — the API
	// (already reached over ngrok) brokers the bytes to/from MinIO server-side.
	proxy bool
}

func NewHandler(cfg *appcfg.Config) (*Handler, error) {
	if cfg.Storage.Bucket == "" || cfg.Storage.KeyID == "" || cfg.Storage.Secret == "" || cfg.Storage.CDNURL == "" {
		return nil, fmt.Errorf("storage env vars are incomplete")
	}
	awsCfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion(cfg.Storage.Region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.Storage.KeyID, cfg.Storage.Secret, "")),
	)
	if err != nil {
		return nil, err
	}
	s3Client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		switch {
		case cfg.Storage.Endpoint != "":
			// S3-compatible store with an explicit endpoint (MinIO in dev,
			// self-hosted gateways). Path-style avoids vhost DNS per bucket.
			o.UsePathStyle = true
			o.BaseEndpoint = aws.String(cfg.Storage.Endpoint)
		case strings.EqualFold(cfg.Storage.Provider, "r2"):
			o.UsePathStyle = true
			o.BaseEndpoint = aws.String(fmt.Sprintf("https://%s.r2.cloudflarestorage.com", cfg.Storage.KeyID))
		}
	})
	return &Handler{
		cfg:       cfg,
		client:    s3Client,
		presigner: s3.NewPresignClient(s3Client),
		proxy:     strings.EqualFold(cfg.Storage.Provider, "minio"),
	}, nil
}

func (h *Handler) PresignedURL(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ContentType string `json:"content_type"`
		Purpose     string `json:"purpose"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, apperrors.ErrBadRequest)
		return
	}
	prefix := purposePrefix(body.Purpose)
	if prefix == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", "unsupported purpose")
		return
	}
	if !allowedContentType(body.ContentType) {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", "unsupported content_type")
		return
	}

	ext := extensionByType(body.ContentType)
	key, err := randomKey(ext)
	if err != nil {
		respond.Error(w, apperrors.ErrInternal)
		return
	}
	objectKey := prefix + key

	// Proxy mode (MinIO/dev): hand back API routes the device can already reach
	// over ngrok. The client PUTs the bytes to upload_url; this API streams them
	// into MinIO server-side. file_url serves them back the same way.
	if h.proxy {
		base := strings.TrimRight(h.cfg.Storage.CDNURL, "/") + "/" + objectKey
		respond.OK(w, map[string]any{
			"upload_url": base,
			"file_url":   base,
			"expires_in": 300,
			"max_size":   maxUploadBytes,
		})
		return
	}

	putInput := &s3.PutObjectInput{
		Bucket:      aws.String(h.cfg.Storage.Bucket),
		Key:         aws.String(objectKey),
		ContentType: aws.String(body.ContentType),
	}
	// MinIO/custom endpoints ignore canned ACLs (they use bucket policies for
	// public read). Signing the ACL would force the client to replay the exact
	// x-amz-acl header on PUT; skip it there. On S3/R2 keep public-read so the
	// file_url is immediately viewable.
	if h.cfg.Storage.Endpoint == "" {
		putInput.ACL = types.ObjectCannedACLPublicRead
	}
	req, err := h.presigner.PresignPutObject(r.Context(), putInput, s3.WithPresignExpires(5*time.Minute))
	if err != nil {
		respond.Error(w, apperrors.ErrInternal)
		return
	}

	respond.OK(w, map[string]any{
		"upload_url": req.URL,
		"file_url":   strings.TrimRight(h.cfg.Storage.CDNURL, "/") + "/" + objectKey,
		"expires_in": 300,
		"max_size":   10 * 1024 * 1024,
	})
}

// PutObject (proxy mode) streams an authenticated upload straight into MinIO.
// PUT /api/v1/uploads/objects/documents/<key>  — body is the raw file bytes.
func (h *Handler) PutObject(w http.ResponseWriter, r *http.Request) {
	if !h.proxy {
		respond.ErrorMsg(w, http.StatusNotFound, "NOT_FOUND", "direct upload not enabled")
		return
	}
	objectKey, ok := safeObjectKey(chi.URLParam(r, "*"))
	if !ok {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", "invalid object key")
		return
	}
	contentType := r.Header.Get("Content-Type")
	if !allowedContentType(contentType) {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", "unsupported content_type")
		return
	}

	// Cap the body so a client can't stream an unbounded file into MinIO.
	body := http.MaxBytesReader(w, r.Body, maxUploadBytes)
	data, err := io.ReadAll(body)
	if err != nil {
		respond.ErrorMsg(w, http.StatusRequestEntityTooLarge, "TOO_LARGE", "file exceeds 10 MB")
		return
	}

	_, err = h.client.PutObject(r.Context(), &s3.PutObjectInput{
		Bucket:        aws.String(h.cfg.Storage.Bucket),
		Key:           aws.String(objectKey),
		Body:          strings.NewReader(string(data)),
		ContentType:   aws.String(contentType),
		ContentLength: aws.Int64(int64(len(data))),
	})
	if err != nil {
		respond.Error(w, apperrors.ErrInternal)
		return
	}
	respond.NoContent(w)
}

// GetObject (proxy mode) streams a stored object back. Public so <img src> and
// the admin panel can render documents without forwarding a bearer token; keys
// are 128-bit random, so they are unguessable (matches S3 public-read + random
// key design used in production).
// GET /api/v1/uploads/objects/documents/<key>
func (h *Handler) GetObject(w http.ResponseWriter, r *http.Request) {
	if !h.proxy {
		respond.ErrorMsg(w, http.StatusNotFound, "NOT_FOUND", "object serving not enabled")
		return
	}
	objectKey, ok := safeObjectKey(chi.URLParam(r, "*"))
	if !ok {
		respond.ErrorMsg(w, http.StatusBadRequest, "VALIDATION", "invalid object key")
		return
	}
	out, err := h.client.GetObject(r.Context(), &s3.GetObjectInput{
		Bucket: aws.String(h.cfg.Storage.Bucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		respond.ErrorMsg(w, http.StatusNotFound, "NOT_FOUND", "object not found")
		return
	}
	defer out.Body.Close()
	if out.ContentType != nil {
		w.Header().Set("Content-Type", *out.ContentType)
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = io.Copy(w, out.Body)
}

func purposePrefix(p string) string {
	switch p {
	case "driver_document":
		return "documents/"
	case "profile_image":
		return "avatars/"
	default:
		return ""
	}
}

// safeObjectKey constrains proxy reads/writes to allowed prefixes and
// rejects path traversal, so the routes can't touch arbitrary bucket objects.
func safeObjectKey(raw string) (string, bool) {
	raw = strings.TrimPrefix(raw, "/")
	if strings.Contains(raw, "..") {
		return "", false
	}
	if strings.HasPrefix(raw, "documents/") || strings.HasPrefix(raw, "avatars/") {
		return raw, true
	}
	return "", false
}

func allowedContentType(t string) bool {
	switch t {
	case "image/jpeg", "image/png", "image/heic", "application/pdf":
		return true
	default:
		return false
	}
}

func extensionByType(t string) string {
	switch t {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/heic":
		return ".heic"
	case "application/pdf":
		return ".pdf"
	default:
		return ""
	}
}

func randomKey(ext string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return filepath.Base(hex.EncodeToString(b) + ext), nil
}
