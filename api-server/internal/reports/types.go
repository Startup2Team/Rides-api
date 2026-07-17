package reports

import "time"

type Report struct {
	ID          string     `json:"id"`
	Template    string     `json:"template"`
	Status      string     `json:"status"`
	Format      string     `json:"format"`
	DateRange   *string    `json:"date_range"`
	FileSize    *string    `json:"file_size"`
	FilePath    *string    `json:"file_path"`
	FileData    []byte     `json:"-"`
	ContentType string     `json:"content_type,omitempty"`
	FileName    string     `json:"file_name,omitempty"`
	GeneratedAt *time.Time `json:"generated_at"`
	CreatedBy   *string    `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
}

type ScheduledReport struct {
	ID         string     `json:"id"`
	Template   string     `json:"template"`
	Format     string     `json:"format"`
	Frequency  string     `json:"frequency"`
	Recipients []string   `json:"recipients"`
	IsActive   bool       `json:"is_active"`
	NextRun    *time.Time `json:"next_run"`
	CreatedAt  time.Time  `json:"created_at"`
}
