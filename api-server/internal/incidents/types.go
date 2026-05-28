package incidents

import "time"

type Incident struct {
	ID             string          `json:"id"`
	Type           string          `json:"type"`
	Severity       string          `json:"severity"`
	Status         string          `json:"status"`
	Description    *string         `json:"description"`
	RideID         *string         `json:"ride_id"`
	ReporterUserID *string         `json:"reporter_user_id"`
	ReporterName   *string         `json:"reporter_name"`
	ReporterPhone  *string         `json:"reporter_phone"`
	ReporterRole   *string         `json:"reporter_role"`
	LocationText   *string         `json:"location_text"`
	District       *string         `json:"district"`
	Notes          *string         `json:"notes"`
	ReportedAt     time.Time       `json:"reported_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	Timeline       []IncidentEvent `json:"timeline,omitempty"`
}

type IncidentEvent struct {
	ID         string    `json:"id"`
	IncidentID string    `json:"incident_id"`
	EventText  string    `json:"event_text"`
	Kind       string    `json:"kind"`
	CreatedAt  time.Time `json:"created_at"`
}

type ListFilter struct {
	Status   string
	Severity string
	Type     string
	Search   string
	Limit    int
	Offset   int
}
