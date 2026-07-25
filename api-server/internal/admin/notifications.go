package admin

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// CampaignInput is one admin notification campaign as composed in the console.
type CampaignInput struct {
	Title    string
	Body     string
	Audience string // see audienceVehicleCodes + ALL / DRIVERS / CUSTOMERS / SINGLE_DRIVER
	Status   string // SENT (default), DRAFT, or SCHEDULED
	// ScheduledAt is required for SCHEDULED. A time already past sends immediately.
	ScheduledAt *time.Time
	// TargetDriverRef is required for SINGLE_DRIVER: either a driver_profiles.id
	// or the driver's users.id — the console lets staff paste whichever they have.
	TargetDriverRef string
	// Reason is an optional notice category (document_expiry, account_warning, …)
	// carried in the push payload so the app can route/label the message.
	Reason    string
	CreatedBy string
}

// audienceVehicleCodes maps the console's per-vehicle audiences onto canonical
// driver_profiles.transport_type codes. "Rifani" (a.k.a. lifani) is the local
// name for the tuk-tuk.
var audienceVehicleCodes = map[string]string{
	"DRIVER_MOTO":   "MOTO_BIKE",
	"DRIVER_CAB":    "CAB_TAXI",
	"DRIVER_HILUX":  "LIGHT_HILUX",
	"DRIVER_FUSO":   "HEAVY_FUSO",
	"DRIVER_RIFANI": "TUK_TUK",
}

const (
	campaignStatusSent      = "SENT"
	campaignStatusDraft     = "DRAFT"
	campaignStatusScheduled = "SCHEDULED"
)

// CreateNotificationCampaign records an admin notification campaign and, when it
// is being sent now, delivers it to the target audience. Delivery is a REAL
// push: for each target user it persists an in-app feed row AND pushes to every
// registered device (via the notifier). Delivery runs in the background so the
// admin request returns immediately; the campaign row is the record of intent.
// When no notifier is wired (e.g. tests), it falls back to a feed-only insert.
//
// DRAFT and SCHEDULED campaigns are recorded but NOT delivered — a scheduled one
// fires later via RunScheduledCampaignDispatcher, a draft only once an admin
// sends it. (Both used to be broadcast to everyone the moment they were saved.)
func (s *Service) CreateNotificationCampaign(ctx context.Context, in CampaignInput) (map[string]interface{}, error) {
	if in.Title == "" || in.Body == "" || in.Audience == "" {
		return nil, apperrors.New(http.StatusBadRequest, "INVALID_INPUT", "title, body, and audience are required")
	}
	if _, ok := audienceVehicleCodes[in.Audience]; !ok {
		switch in.Audience {
		case "ALL", "DRIVERS", "CUSTOMERS", "SINGLE_DRIVER":
			// valid
		default:
			return nil, apperrors.New(http.StatusBadRequest, "INVALID_AUDIENCE",
				"audience must be ALL, DRIVERS, CUSTOMERS, SINGLE_DRIVER, or DRIVER_<MOTO|CAB|HILUX|FUSO|RIFANI>")
		}
	}

	// SINGLE_DRIVER carries a target; resolve it up front so a bad ID fails the
	// request instead of silently delivering to nobody in the background.
	var targetUserID *string
	if in.Audience == "SINGLE_DRIVER" {
		uid, err := s.resolveDriverUserID(ctx, in.TargetDriverRef)
		if err != nil {
			return nil, err
		}
		targetUserID = &uid
	}

	status := in.Status
	if status == "" {
		status = campaignStatusSent
	}
	switch status {
	case campaignStatusSent, campaignStatusDraft:
		// A draft/immediate send has no schedule.
		in.ScheduledAt = nil
	case campaignStatusScheduled:
		if in.ScheduledAt == nil {
			return nil, apperrors.New(http.StatusBadRequest, "INVALID_INPUT",
				"scheduled_at is required when status is SCHEDULED")
		}
		// A schedule in the past is a send-now.
		if !in.ScheduledAt.After(time.Now()) {
			status = campaignStatusSent
			in.ScheduledAt = nil
		}
	default:
		return nil, apperrors.New(http.StatusBadRequest, "INVALID_STATUS", "status must be SENT, DRAFT, or SCHEDULED")
	}

	// sent_at records actual delivery, so it stays NULL until the campaign fires.
	var sentAt *time.Time
	if status == campaignStatusSent {
		now := time.Now()
		sentAt = &now
	}

	var campaignID string
	var createdAt time.Time
	err := s.db.QueryRow(ctx, `
		INSERT INTO admin_notifications (title, body, audience, status, created_by, target_driver_id, scheduled_at, sent_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, created_at
	`, in.Title, in.Body, in.Audience, status, in.CreatedBy, targetUserID, in.ScheduledAt, sentAt).
		Scan(&campaignID, &createdAt)
	if err != nil {
		return nil, err
	}

	if status == campaignStatusSent {
		s.deliverCampaign(ctx, campaignID, in.Title, in.Body, in.Audience, in.Reason, targetUserID)
	}

	out := map[string]interface{}{
		"id":           campaignID,
		"title":        in.Title,
		"body":         in.Body,
		"audience":     in.Audience,
		"status":       status,
		"sent_at":      sentAt,
		"scheduled_at": in.ScheduledAt,
		"created_by":   in.CreatedBy,
		"created_at":   createdAt,
	}
	if targetUserID != nil {
		out["target_driver_id"] = *targetUserID
	}
	return out, nil
}

// NotifyDriver sends a one-off admin message to a single driver (in-app feed row
// plus a push to each of their devices) and records it in the campaign history so
// the notice is auditable. driverRef is a driver_profiles.id or a users.id.
func (s *Service) NotifyDriver(ctx context.Context, driverRef, title, body, reason, createdBy string) (map[string]interface{}, error) {
	return s.CreateNotificationCampaign(ctx, CampaignInput{
		Title:           title,
		Body:            body,
		Audience:        "SINGLE_DRIVER",
		Status:          campaignStatusSent,
		TargetDriverRef: driverRef,
		Reason:          reason,
		CreatedBy:       createdBy,
	})
}

// resolveDriverUserID turns whatever the admin pasted into a users.id: the admin
// driver consoles work in driver_profiles.id, but the compose form accepts either.
func (s *Service) resolveDriverUserID(ctx context.Context, ref string) (string, error) {
	if ref == "" {
		return "", apperrors.New(http.StatusBadRequest, "INVALID_INPUT", "a target driver is required")
	}
	// Non-UUID text would make Postgres error out (22P02) rather than miss.
	if _, err := uuid.Parse(ref); err != nil {
		return "", apperrors.New(http.StatusBadRequest, "INVALID_INPUT", "target driver must be a driver profile ID or user ID")
	}

	var userID string
	err := s.db.QueryRow(ctx, `SELECT user_id FROM driver_profiles WHERE id = $1`, ref).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = s.db.QueryRow(ctx, `SELECT id FROM users WHERE id = $1`, ref).Scan(&userID)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return "", apperrors.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return userID, nil
}

// deliverCampaign dispatches an already-recorded campaign. With a notifier wired
// it fans out pushes in the background; without one it writes in-app feed rows
// only, inline.
func (s *Service) deliverCampaign(ctx context.Context, campaignID, title, body, audience, reason string, targetUserID *string) {
	if s.notifier != nil {
		// Detached context: the admin request returns before delivery finishes.
		go s.deliverCampaignPush(campaignID, title, body, audience, reason, targetUserID)
		return
	}
	// No push wired — at least populate the in-app feed. Non-fatal: the campaign
	// is already recorded.
	if err := s.deliverCampaignFeedOnly(ctx, title, body, audience, targetUserID); err != nil {
		s.log.Warn().Err(err).Str("campaign_id", campaignID).Msg("notifications: feed-only delivery failed")
	}
}

// deliverCampaignPush fans the campaign out to every target user: an in-app feed
// row plus a push to each of their devices. Runs in its own goroutine with a
// detached, time-bounded context. Best-effort per user (SendToAllDevices logs
// and prunes dead tokens internally).
// NOTE (scale): this is a per-user loop — fine for now; move to a queue/batch
// worker when audiences get large or delivery must survive a process restart.
func (s *Service) deliverCampaignPush(campaignID, title, body, audience, reason string, targetUserID *string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	ids, err := s.targetUserIDs(ctx, audience, targetUserID)
	if err != nil {
		s.log.Error().Err(err).Str("campaign_id", campaignID).Msg("notifications: resolve audience failed")
		return
	}
	s.log.Info().Str("campaign_id", campaignID).Str("audience", audience).Int("recipients", len(ids)).Msg("notifications: campaign delivery started")

	nType, data := campaignPayload(campaignID, audience, reason)
	// Bounded-concurrency fan-out: a strictly sequential loop over 100k+ users
	// (each = DB insert + token query + FCM round-trip) cannot finish before the
	// deadline and silently drops the tail. A fixed worker pool delivers in
	// parallel while capping load on the DB pool / FCM. (Durability across a
	// process restart still needs a real queue — tracked as a follow-up.)
	const workers = 16
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	delivered := 0
	for _, uid := range ids {
		select {
		case <-ctx.Done():
			s.log.Warn().Str("campaign_id", campaignID).Int("delivered", delivered).Int("total", len(ids)).
				Msg("notifications: campaign delivery deadline reached — remaining recipients NOT delivered")
			wg.Wait()
			return
		case sem <- struct{}{}:
		}
		wg.Add(1)
		delivered++
		go func(uid string) {
			defer wg.Done()
			defer func() { <-sem }()
			s.notifier.SendToAllDevices(ctx, uid, title, body, nType, data)
		}(uid)
	}
	wg.Wait()
	s.log.Info().Str("campaign_id", campaignID).Int("delivered", len(ids)).Msg("notifications: campaign delivery finished")
}

// campaignPayload picks the in-app notification type and push data for a
// campaign. A direct notice to one driver is driver-scoped, not a promo, so the
// app can badge it under account notices instead of marketing.
func campaignPayload(campaignID, audience, reason string) (string, map[string]string) {
	data := map[string]string{"kind": "campaign", "campaign_id": campaignID}
	if reason != "" {
		data["reason"] = reason
	}
	if audience == "SINGLE_DRIVER" {
		data["type"] = "admin_notice"
		return "driver", data
	}
	return "promo", data
}

// targetUserIDs resolves the user IDs for an audience (validated by the caller).
// Suspended users are skipped — except for a directly targeted driver, since a
// suspension notice is exactly the kind of message that must still land.
func (s *Service) targetUserIDs(ctx context.Context, audience string, targetUserID *string) ([]string, error) {
	if audience == "SINGLE_DRIVER" {
		if targetUserID == nil || *targetUserID == "" {
			return nil, apperrors.New(http.StatusBadRequest, "INVALID_INPUT", "a target driver is required")
		}
		return []string{*targetUserID}, nil
	}

	var q string
	args := []any{}
	switch {
	case audience == "DRIVERS":
		q = `SELECT DISTINCT dp.user_id FROM driver_profiles dp
		       JOIN users u ON u.id = dp.user_id
		      WHERE u.is_suspended = FALSE`
	case audience == "CUSTOMERS":
		q = `SELECT id FROM users
		      WHERE is_suspended = FALSE
		        AND id NOT IN (SELECT DISTINCT user_id FROM driver_profiles)`
	case audienceVehicleCodes[audience] != "":
		q = `SELECT DISTINCT dp.user_id FROM driver_profiles dp
		       JOIN users u ON u.id = dp.user_id
		      WHERE u.is_suspended = FALSE AND dp.transport_type = $1`
		args = append(args, audienceVehicleCodes[audience])
	default: // ALL
		q = `SELECT id FROM users WHERE is_suspended = FALSE`
	}
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// deliverCampaignFeedOnly writes in-app feed rows (no push) with one set-based
// insert. Fallback used only when no notifier is wired.
func (s *Service) deliverCampaignFeedOnly(ctx context.Context, title, body, audience string, targetUserID *string) error {
	nType, _ := campaignPayload("", audience, "")
	var q string
	args := []any{title, body, nType}
	switch {
	case audience == "SINGLE_DRIVER":
		if targetUserID == nil || *targetUserID == "" {
			return apperrors.New(http.StatusBadRequest, "INVALID_INPUT", "a target driver is required")
		}
		q = `INSERT INTO notifications (user_id, title, body, type, data)
		     VALUES ($4, $1, $2, $3, '{}'::jsonb)`
		args = append(args, *targetUserID)
	case audience == "DRIVERS":
		q = `INSERT INTO notifications (user_id, title, body, type, data)
		     SELECT DISTINCT dp.user_id, $1, $2, $3, '{}'::jsonb
		       FROM driver_profiles dp JOIN users u ON u.id = dp.user_id
		      WHERE u.is_suspended = FALSE`
	case audience == "CUSTOMERS":
		q = `INSERT INTO notifications (user_id, title, body, type, data)
		     SELECT id, $1, $2, $3, '{}'::jsonb FROM users
		      WHERE is_suspended = FALSE
		        AND id NOT IN (SELECT DISTINCT user_id FROM driver_profiles)`
	case audienceVehicleCodes[audience] != "":
		q = `INSERT INTO notifications (user_id, title, body, type, data)
		     SELECT DISTINCT dp.user_id, $1, $2, $3, '{}'::jsonb
		       FROM driver_profiles dp JOIN users u ON u.id = dp.user_id
		      WHERE u.is_suspended = FALSE AND dp.transport_type = $4`
		args = append(args, audienceVehicleCodes[audience])
	default: // ALL
		q = `INSERT INTO notifications (user_id, title, body, type, data)
		     SELECT id, $1, $2, $3, '{}'::jsonb FROM users WHERE is_suspended = FALSE`
	}
	_, err := s.db.Exec(ctx, q, args...)
	return err
}

// RunScheduledCampaignDispatcher delivers campaigns whose scheduled_at has
// arrived. Without it a "Schedule for later" campaign would sit in the table
// forever. Poll-based (once a minute) — minute granularity is what the console's
// datetime picker offers anyway.
func (s *Service) RunScheduledCampaignDispatcher(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.dispatchDueCampaigns(ctx); err != nil {
				s.log.Error().Err(err).Msg("notifications: scheduled campaign dispatch failed")
			}
		}
	}
}

// dispatchDueCampaigns claims every campaign that is now due and delivers it.
// The claim is the UPDATE itself (FOR UPDATE SKIP LOCKED), so running more than
// one API replica cannot double-send.
func (s *Service) dispatchDueCampaigns(ctx context.Context) error {
	rows, err := s.db.Query(ctx, `
		UPDATE admin_notifications SET status = 'SENT', sent_at = NOW()
		WHERE id IN (
			SELECT id FROM admin_notifications
			 WHERE status = 'SCHEDULED' AND scheduled_at IS NOT NULL AND scheduled_at <= NOW()
			 ORDER BY scheduled_at
			 LIMIT 50
			 FOR UPDATE SKIP LOCKED
		)
		RETURNING id, title, body, audience, target_driver_id::text
	`)
	if err != nil {
		return err
	}
	type due struct {
		id, title, body, audience string
		target                    *string
	}
	var claimed []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.title, &d.body, &d.audience, &d.target); err != nil {
			rows.Close()
			return err
		}
		claimed = append(claimed, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, d := range claimed {
		s.log.Info().Str("campaign_id", d.id).Str("audience", d.audience).Msg("notifications: dispatching scheduled campaign")
		s.deliverCampaign(ctx, d.id, d.title, d.body, d.audience, "", d.target)
	}
	return nil
}

// ListNotificationCampaigns lists past admin notification campaigns.
func (s *Service) ListNotificationCampaigns(ctx context.Context, limit, offset int) ([]map[string]interface{}, int, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	var total int
	err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM admin_notifications`).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	rows, err := s.db.Query(ctx, `
		SELECT id, title, body, audience, status, sent_at, scheduled_at,
		       COALESCE(target_driver_id::text, ''), COALESCE(created_by, ''), created_at
		FROM admin_notifications
		ORDER BY created_at DESC
		LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var campaigns []map[string]interface{}
	for rows.Next() {
		var id, title, body, audience, status, targetDriverID, createdBy string
		var sentAt, scheduledAt *time.Time
		var createdAt time.Time
		if err := rows.Scan(&id, &title, &body, &audience, &status, &sentAt, &scheduledAt, &targetDriverID, &createdBy, &createdAt); err != nil {
			return nil, 0, err
		}
		campaign := map[string]interface{}{
			"id":           id,
			"title":        title,
			"body":         body,
			"audience":     audience,
			"status":       status,
			"sent_at":      sentAt,
			"scheduled_at": scheduledAt,
			"created_by":   createdBy,
			"created_at":   createdAt,
		}
		if targetDriverID != "" {
			campaign["target_driver_id"] = targetDriverID
		}
		campaigns = append(campaigns, campaign)
	}

	return campaigns, total, rows.Err()
}

// SendNotificationCampaignNow delivers an existing DRAFT or SCHEDULED campaign
// immediately. Without this the console's "Send now" button had to re-POST the
// composed campaign, leaving the draft behind as a duplicate history row.
func (s *Service) SendNotificationCampaignNow(ctx context.Context, id, sentBy string) (map[string]interface{}, error) {
	var title, body, audience string
	var target *string
	var sentAt, createdAt time.Time
	err := s.db.QueryRow(ctx, `
		UPDATE admin_notifications
		SET status = 'SENT', sent_at = NOW(), scheduled_at = NULL
		WHERE id = $1 AND status IN ('DRAFT', 'SCHEDULED')
		RETURNING title, body, audience, target_driver_id::text, sent_at, created_at
	`, id).Scan(&title, &body, &audience, &target, &sentAt, &createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// Either no such campaign, or it has already been sent.
		var status string
		if qErr := s.db.QueryRow(ctx, `SELECT status FROM admin_notifications WHERE id = $1`, id).Scan(&status); qErr != nil {
			return nil, apperrors.ErrNotFound
		}
		return nil, apperrors.Newf(http.StatusConflict, "INVALID_STATE", "campaign is already %s", status)
	}
	if err != nil {
		return nil, err
	}

	s.deliverCampaign(ctx, id, title, body, audience, "", target)

	out := map[string]interface{}{
		"id":         id,
		"title":      title,
		"body":       body,
		"audience":   audience,
		"status":     campaignStatusSent,
		"sent_at":    sentAt,
		"created_by": sentBy,
		"created_at": createdAt,
	}
	if target != nil {
		out["target_driver_id"] = *target
	}
	return out, nil
}

// DeleteNotificationCampaign deletes a campaign record.
func (s *Service) DeleteNotificationCampaign(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM admin_notifications WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperrors.ErrNotFound
	}
	return nil
}
