package reports

import (
	"context"
	"fmt"
	"net/http"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// ReportBuilder produces the actual bytes of a report for a given template,
// format, and date range. Injected from main so this package doesn't import
// finance/analytics directly. Returns the bytes, HTTP content type, and a
// download filename.
type ReportBuilder func(ctx context.Context, template, format string, dateRange *string) (data []byte, contentType, filename string, err error)

type Service struct {
	repo  *Repository
	build ReportBuilder
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

// SetBuilder wires the concrete report-content generator.
func (s *Service) SetBuilder(b ReportBuilder) { s.build = b }

func (s *Service) List(ctx context.Context, status, format string, limit, offset int) ([]*Report, int, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	return s.repo.List(ctx, status, format, limit, offset)
}

func (s *Service) GetByID(ctx context.Context, id string) (*Report, error) {
	rep, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, apperrors.ErrNotFound
	}
	return rep, nil
}

func formatFileSize(size int) string {
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}
	return fmt.Sprintf("%.1f KB", float64(size)/1024.0)
}

func (s *Service) Generate(ctx context.Context, template, format, dateRange string, createdBy *string) (*Report, error) {
	rep, err := s.repo.Create(ctx, template, format, dateRange, createdBy)
	if err != nil {
		return nil, err
	}

	var data []byte
	var contentType, filename string
	if s.build != nil {
		var buildErr error
		data, contentType, filename, buildErr = s.build(ctx, template, format, &dateRange)
		if buildErr != nil {
			_ = s.repo.MarkFailed(ctx, rep.ID)
			return nil, buildErr
		}
	} else {
		contentType = "text/csv"
		filename = template + ".csv"
	}

	sizeStr := formatFileSize(len(data))
	if err := s.repo.MarkReady(ctx, rep.ID, filename, sizeStr, data, contentType, filename); err != nil {
		_ = s.repo.MarkFailed(ctx, rep.ID)
		return nil, err
	}

	rep.Status = "READY"
	rep.FilePath = &filename
	rep.FileSize = &sizeStr
	rep.ContentType = contentType
	rep.FileName = filename
	rep.FileData = data
	return rep, nil
}

// Build generates the bytes for a report from live data using the injected
// builder. Used by the download endpoint to stream a real file.
func (s *Service) Build(ctx context.Context, id string) (data []byte, contentType, filename string, err error) {
	rep, err := s.GetByID(ctx, id)
	if err != nil {
		return nil, "", "", err
	}
	if len(rep.FileData) > 0 {
		return rep.FileData, rep.ContentType, rep.FileName, nil
	}
	if s.build == nil {
		return nil, "", "", apperrors.New(http.StatusNotImplemented, "NOT_IMPLEMENTED", "report generation is not configured")
	}
	return s.build(ctx, rep.Template, rep.Format, rep.DateRange)
}

func (s *Service) ListScheduled(ctx context.Context) ([]*ScheduledReport, error) {
	return s.repo.ListScheduled(ctx)
}

func (s *Service) CreateScheduled(ctx context.Context, template, format, frequency string, recipients []string) (*ScheduledReport, error) {
	return s.repo.CreateScheduled(ctx, template, format, frequency, recipients)
}

func (s *Service) ToggleScheduled(ctx context.Context, id string) error {
	return s.repo.ToggleScheduled(ctx, id)
}

func (s *Service) Delete(ctx context.Context, id string) error {
	if _, err := s.repo.FindByID(ctx, id); err != nil {
		return apperrors.ErrNotFound
	}
	return s.repo.Delete(ctx, id)
}

func (s *Service) Stats(ctx context.Context) (map[string]interface{}, error) {
	return s.repo.Stats(ctx)
}

func (s *Service) GetFilePath(ctx context.Context, id string) (string, error) {
	fp, err := s.repo.GetFilePath(ctx, id)
	if err != nil {
		return "", apperrors.ErrNotFound
	}
	return fp, nil
}
