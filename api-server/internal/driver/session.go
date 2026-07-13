package driver

// GET /driver/session — the one-call bootstrap the mobile app needs after
// login/reconnect: who am I as a driver, which vehicle am I on, am I mid-ride,
// and are any of my documents expiring. Read-only aggregation of existing data.

import (
	"context"
	"time"
)

// DocumentAlert flags a driver document that is expired or expiring soon.
type DocumentAlert struct {
	Document  string `json:"document"` // license | insurance | authorization
	ExpiresOn string `json:"expires_on"`
	DaysLeft  int    `json:"days_left"` // negative = already expired
	Status    string `json:"status"`    // EXPIRED | EXPIRING_SOON
}

// documentAlertWindowDays is how far ahead we warn about an expiring document.
const documentAlertWindowDays = 30

// documentAlerts derives expiry alerts from a profile's document dates.
func documentAlerts(p *Profile, now time.Time) []DocumentAlert {
	return alertsFromDates(p.LicenseExpiryDate, p.InsuranceExpiryDate, p.AuthorizationExpiryDate, now)
}

// alertsFromDates is the pure core shared by the session response (inline
// banner) and the daily expiry notifier (push), so both always agree on what
// "expiring" means.
func alertsFromDates(license, insurance, authorization *time.Time, now time.Time) []DocumentAlert {
	docs := []struct {
		name string
		date *time.Time
	}{
		{"license", license},
		{"insurance", insurance},
		{"authorization", authorization},
	}

	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	alerts := []DocumentAlert{}
	for _, d := range docs {
		if d.date == nil {
			continue
		}
		exp := time.Date(d.date.Year(), d.date.Month(), d.date.Day(), 0, 0, 0, 0, time.UTC)
		daysLeft := int(exp.Sub(today).Hours() / 24)
		if daysLeft > documentAlertWindowDays {
			continue
		}
		status := "EXPIRING_SOON"
		if daysLeft < 0 {
			status = "EXPIRED"
		}
		alerts = append(alerts, DocumentAlert{
			Document:  d.name,
			ExpiresOn: exp.Format("2006-01-02"),
			DaysLeft:  daysLeft,
			Status:    status,
		})
	}
	return alerts
}

// Session is the driver's current-state snapshot.
type Session struct {
	Profile        *Profile        `json:"profile"`
	ActiveVehicle  *Vehicle        `json:"active_vehicle"` // null when none registered
	VehicleCount   int             `json:"vehicle_count"`
	HasActiveRide  bool            `json:"has_active_ride"`
	DocumentAlerts []DocumentAlert `json:"document_alerts"`
}

// GetSession aggregates the driver's profile, active vehicle, ride state and
// document alerts. ListVehicles is used (not a raw query) so legacy profiles
// get their vehicle row lazily backfilled on first call.
func (s *Service) GetSession(ctx context.Context, userID string) (*Session, error) {
	profile, err := s.repo.FindProfileByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}

	vehicles, err := s.ListVehicles(ctx, userID)
	if err != nil {
		return nil, err
	}
	var active *Vehicle
	for _, v := range vehicles {
		if v.IsActive {
			active = v
			break
		}
	}

	return &Session{
		Profile:        profile,
		ActiveVehicle:  active,
		VehicleCount:   len(vehicles),
		HasActiveRide:  s.repo.HasActiveRide(ctx, userID),
		DocumentAlerts: documentAlerts(profile, time.Now()),
	}, nil
}
