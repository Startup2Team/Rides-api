package driver

// Daily document-expiry notifier: warns drivers before their license,
// insurance or route authorization expires, via FCM push + persisted in-app
// notification. Runs once a day; a driver is notified only on the day-marks in
// expiryNotifyDays (and the day after expiry), so a 30-day window doesn't spam
// 30 pushes. Uses the same alertsFromDates logic as GET /driver/session.

import (
	"context"
	"fmt"
	"time"
)

// expiryNotifyDays are the days-left marks that trigger a notification.
// 0 = expires today; the notifier also fires once at -1 (expired yesterday).
var expiryNotifyDays = map[int]bool{30: true, 14: true, 7: true, 3: true, 1: true, 0: true, -1: true}

// shouldNotifyExpiry reports whether a document with daysLeft to expiry gets a
// notification today. Pure, so the day-mark policy is unit-testable.
func shouldNotifyExpiry(daysLeft int) bool {
	return expiryNotifyDays[daysLeft]
}

// expiryMessage builds the notification title+body for one alert.
func expiryMessage(a DocumentAlert) (title, body string) {
	docName := map[string]string{
		"license":       "driving license",
		"insurance":     "vehicle insurance",
		"authorization": "route authorization",
	}[a.Document]

	switch {
	case a.DaysLeft < 0:
		return "Document expired", fmt.Sprintf("Your %s expired on %s. Renew it and update your documents to keep driving.", docName, a.ExpiresOn)
	case a.DaysLeft == 0:
		return "Document expires today", fmt.Sprintf("Your %s expires today (%s). Renew it to keep driving.", docName, a.ExpiresOn)
	case a.DaysLeft == 1:
		return "Document expires tomorrow", fmt.Sprintf("Your %s expires tomorrow (%s). Renew it to avoid interruption.", docName, a.ExpiresOn)
	default:
		return "Document expiring soon", fmt.Sprintf("Your %s expires in %d days (%s). Renew it to avoid interruption.", docName, a.DaysLeft, a.ExpiresOn)
	}
}

// expiryNotifier is the piece of the notification service the job needs —
// an interface so tests can fake it and to keep the driver→notification
// coupling minimal.
type expiryNotifier interface {
	SendToUser(ctx context.Context, userID, fcmToken, title, body, nType string, data map[string]string) error
}

// SetExpiryNotifier wires the notification service (called from main after
// both services exist).
func (s *Service) SetExpiryNotifier(n expiryNotifier) {
	s.expiryNotifier = n
}

// expiringDocsRow is one driver whose documents are inside the alert window.
type expiringDocsRow struct {
	UserID                  string
	FCMToken                *string
	LicenseExpiryDate       *time.Time
	InsuranceExpiryDate     *time.Time
	AuthorizationExpiryDate *time.Time
}

// ListProfilesWithExpiringDocs returns approved drivers with any document
// expiring within windowDays (or already expired up to graceDays ago).
func (r *Repository) ListProfilesWithExpiringDocs(ctx context.Context, windowDays, graceDays int) ([]expiringDocsRow, error) {
	rows, err := r.db.Query(ctx, `
		SELECT dp.user_id, u.fcm_token,
		       dp.license_expiry_date, dp.insurance_expiry_date, dp.authorization_expiry_date
		FROM driver_profiles dp
		JOIN users u ON u.id = dp.user_id
		WHERE dp.approval_status = 'APPROVED'
		  AND (
		        dp.license_expiry_date       BETWEEN CURRENT_DATE - $2::int AND CURRENT_DATE + $1::int
		     OR dp.insurance_expiry_date     BETWEEN CURRENT_DATE - $2::int AND CURRENT_DATE + $1::int
		     OR dp.authorization_expiry_date BETWEEN CURRENT_DATE - $2::int AND CURRENT_DATE + $1::int
		  )
	`, windowDays, graceDays)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []expiringDocsRow
	for rows.Next() {
		var row expiringDocsRow
		if err := rows.Scan(&row.UserID, &row.FCMToken, &row.LicenseExpiryDate, &row.InsuranceExpiryDate, &row.AuthorizationExpiryDate); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// notifyExpiringDocuments runs one pass: find drivers in the window, apply the
// day-mark policy, send push + in-app notification for each hit.
func (s *Service) notifyExpiringDocuments(ctx context.Context) {
	if s.expiryNotifier == nil {
		return
	}
	drivers, err := s.repo.ListProfilesWithExpiringDocs(ctx, documentAlertWindowDays, 1)
	if err != nil {
		s.log.Error().Err(err).Msg("expiry notifier: query failed")
		return
	}

	now := time.Now()
	sent := 0
	for _, d := range drivers {
		token := ""
		if d.FCMToken != nil {
			token = *d.FCMToken
		}
		for _, a := range alertsFromDates(d.LicenseExpiryDate, d.InsuranceExpiryDate, d.AuthorizationExpiryDate, now) {
			if !shouldNotifyExpiry(a.DaysLeft) {
				continue
			}
			title, body := expiryMessage(a)
			data := map[string]string{
				"type":       "document_expiry",
				"document":   a.Document,
				"expires_on": a.ExpiresOn,
				"status":     a.Status,
			}
			if err := s.expiryNotifier.SendToUser(ctx, d.UserID, token, title, body, "document", data); err != nil {
				s.log.Warn().Err(err).Str("user_id", d.UserID).Str("document", a.Document).Msg("expiry notifier: send failed")
				continue
			}
			sent++
		}
	}
	s.log.Info().Int("drivers_in_window", len(drivers)).Int("notifications_sent", sent).Msg("expiry notifier: daily pass complete")
}

// RunDocumentExpiryNotifier runs the daily loop. Call as a goroutine from main.
// First pass runs shortly after startup (so a redeploy never skips a day),
// then every 24h.
func (s *Service) RunDocumentExpiryNotifier(ctx context.Context) {
	const startupDelay = 2 * time.Minute
	select {
	case <-ctx.Done():
		return
	case <-time.After(startupDelay):
	}
	s.notifyExpiringDocuments(ctx)

	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.notifyExpiringDocuments(ctx)
		}
	}
}
