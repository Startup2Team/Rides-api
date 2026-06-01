package upload

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	appcfg "github.com/workspace/ride-platform/config"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/respond"
)

type Handler struct {
	cfg       *appcfg.Config
	presigner *s3.PresignClient
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
		if strings.EqualFold(cfg.Storage.Provider, "r2") {
			o.UsePathStyle = true
			o.BaseEndpoint = aws.String(fmt.Sprintf("https://%s.r2.cloudflarestorage.com", cfg.Storage.KeyID))
		}
	})
	return &Handler{cfg: cfg, presigner: s3.NewPresignClient(s3Client)}, nil
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
	if body.Purpose != "driver_document" {
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
	objectKey := "documents/" + key

	req, err := h.presigner.PresignPutObject(r.Context(), &s3.PutObjectInput{
		Bucket:      aws.String(h.cfg.Storage.Bucket),
		Key:         aws.String(objectKey),
		ContentType: aws.String(body.ContentType),
		ACL:         types.ObjectCannedACLPublicRead,
	}, s3.WithPresignExpires(5*time.Minute))
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
