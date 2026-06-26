package upload

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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

const maxUploadBytes = 25 * 1024 * 1024 // 25 MB — headroom for full-res phone photos

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
	h := &Handler{
		cfg:       cfg,
		client:    s3Client,
		presigner: s3.NewPresignClient(s3Client),
		proxy:     strings.EqualFold(cfg.Storage.Provider, "minio"),
	}
	if h.proxy {
		go h.SeedDevMockFiles()
	}
	return h, nil
}

func (h *Handler) SeedDevMockFiles() {
	if !h.proxy {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. Create the bucket if it doesn't exist
	_, err := h.client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(h.cfg.Storage.Bucket),
	})
	if err != nil {
		// Ignore if it already exists
	}

	// Set bucket policy to allow anonymous read (MinIO default is private,
	// but the application relies on public-read style URL access for driver images).
	policy := fmt.Sprintf(`{
		"Version": "2012-10-17",
		"Statement": [
			{
				"Sid": "PublicRead",
				"Effect": "Allow",
				"Principal": "*",
				"Action": ["s3:GetObject"],
				"Resource": ["arn:aws:s3:::%s/*"]
			}
		]
	}`, h.cfg.Storage.Bucket)

	_, _ = h.client.PutBucketPolicy(ctx, &s3.PutBucketPolicyInput{
		Bucket: aws.String(h.cfg.Storage.Bucket),
		Policy: aws.String(policy),
	})

	// 2. Read the placeholder image file
	var imgData []byte
	paths := []string{
		"../../rides-web/public/images/driverside-clean.png",
		"../rides-web/public/images/driverside-clean.png",
	}

	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err == nil && len(data) > 0 {
			imgData = data
			break
		}
	}

	// Fallback to a tiny 1x1 transparent PNG if we cannot load the real image
	if len(imgData) == 0 {
		imgData = []byte{
			0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
			0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
			0x89, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x62, 0x60, 0x60, 0x60, 0x60,
			0x00, 0x00, 0x00, 0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45,
			0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
		}
	}

	mockDocs := []string{
		"documents/mock-licence-front.jpg",
		"documents/mock-licence-back.jpg",
		"documents/mock-national-id-front.jpg",
		"documents/mock-national-id-back.jpg",
		"documents/mock-vehicle-insurance.jpg",
		"documents/mock-vehicle-authorization.jpg",
		"documents/mock-selfie.jpg",
	}

	contentType := "image/png"
	if len(imgData) > 0 && imgData[0] == 0xFF && imgData[1] == 0xD8 {
		contentType = "image/jpeg"
	}

	for _, key := range mockDocs {
		_, _ = h.client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:      aws.String(h.cfg.Storage.Bucket),
			Key:         aws.String(key),
			Body:        bytes.NewReader(imgData),
			ContentType: aws.String(contentType),
		})
	}
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
		cdnURL := h.cfg.Storage.CDNURL
		if r.Host != "" {
			if strings.Contains(cdnURL, "localhost:8080") {
				cdnURL = strings.Replace(cdnURL, "localhost:8080", r.Host, 1)
			} else if strings.Contains(cdnURL, "127.0.0.1:8080") {
				cdnURL = strings.Replace(cdnURL, "127.0.0.1:8080", r.Host, 1)
			}
		}
		base := strings.TrimRight(cdnURL, "/") + "/" + objectKey
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
		respond.ErrorMsg(w, http.StatusRequestEntityTooLarge, "TOO_LARGE", "file exceeds 25 MB")
		return
	}

	_, err = h.client.PutObject(r.Context(), &s3.PutObjectInput{
		Bucket:        aws.String(h.cfg.Storage.Bucket),
		Key:           aws.String(objectKey),
		Body:          bytes.NewReader(data),
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
