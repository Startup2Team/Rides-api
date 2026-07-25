//go:build integration

package dbit

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/internal/admin"
	"github.com/workspace/ride-platform/internal/auth"
)

// With no notifier wired, a campaign is recorded and delivered feed-only via the
// set-based insert. This verifies the campaign row persists and the target user
// actually receives an in-app feed row against the real schema.
func TestNotificationCampaign_FeedDelivery(t *testing.T) {
	ctx := context.Background()

	phone := uniquePhone()
	u, err := auth.NewRepository(pool).CreateUser(ctx, phone, "dev-camp", "android", nil, nil)
	require.NoError(t, err)

	svc := admin.NewService(pool, zerolog.Nop()) // no notifier → feed-only path
	camp, err := svc.CreateNotificationCampaign(ctx, admin.CampaignInput{
		Title: "Promo " + uniqueKey("t"), Body: "Body text", Audience: "ALL", CreatedBy: "admin-test",
	})
	require.NoError(t, err)
	require.NotEmpty(t, camp["id"])

	// Campaign record persisted.
	var recCount int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM admin_notifications WHERE id = $1`, camp["id"]).Scan(&recCount))
	require.Equal(t, 1, recCount)

	// The ALL-audience delivery reached our (non-suspended) user's feed.
	var feedCount int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM notifications WHERE user_id = $1 AND type = 'promo'`, u.ID).Scan(&feedCount))
	require.GreaterOrEqual(t, feedCount, 1, "ALL-audience campaign must deliver a feed row to the user")
}

// A draft must be recorded and NOT delivered — it used to broadcast to every
// user the moment an admin clicked "Save as draft".
func TestNotificationCampaign_DraftIsNotDelivered(t *testing.T) {
	ctx := context.Background()

	u, err := auth.NewRepository(pool).CreateUser(ctx, uniquePhone(), "dev-draft", "android", nil, nil)
	require.NoError(t, err)

	svc := admin.NewService(pool, zerolog.Nop())
	title := "Draft " + uniqueKey("d")
	camp, err := svc.CreateNotificationCampaign(ctx, admin.CampaignInput{
		Title: title, Body: "Held back", Audience: "ALL", Status: "DRAFT", CreatedBy: "admin-test",
	})
	require.NoError(t, err)
	require.Equal(t, "DRAFT", camp["status"])

	var status string
	var sentAt *time.Time
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT status, sent_at FROM admin_notifications WHERE id = $1`, camp["id"]).Scan(&status, &sentAt))
	require.Equal(t, "DRAFT", status)
	require.Nil(t, sentAt, "a draft has not been delivered, so sent_at must be NULL")

	var feedCount int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM notifications WHERE user_id = $1 AND title = $2`, u.ID, title).Scan(&feedCount))
	require.Zero(t, feedCount, "a draft must not reach any user's feed")

	// Sending it later delivers in place — no second campaign row.
	sent, err := svc.SendNotificationCampaignNow(ctx, camp["id"].(string), "admin-test")
	require.NoError(t, err)
	require.Equal(t, "SENT", sent["status"])

	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM notifications WHERE user_id = $1 AND title = $2`, u.ID, title).Scan(&feedCount))
	require.Equal(t, 1, feedCount)

	var rowCount int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM admin_notifications WHERE title = $1`, title).Scan(&rowCount))
	require.Equal(t, 1, rowCount, "sending a draft must not create a duplicate history row")
}

// A direct notice to one driver reaches only that driver, is looked up by
// driver_profiles.id (what the drivers console passes), and is typed as a driver
// notice rather than a promo.
func TestNotifyDriver_TargetsOnlyThatDriver(t *testing.T) {
	ctx := context.Background()
	repo := auth.NewRepository(pool)

	driverUser, err := repo.CreateUser(ctx, uniquePhone(), "dev-notify-d", "android", nil, nil)
	require.NoError(t, err)
	other, err := repo.CreateUser(ctx, uniquePhone(), "dev-notify-o", "android", nil, nil)
	require.NoError(t, err)

	profileID := insertDriverProfile(t, ctx, driverUser.ID, "MOTO_BIKE")

	svc := admin.NewService(pool, zerolog.Nop())
	title := "Notice " + uniqueKey("n")
	camp, err := svc.NotifyDriver(ctx, profileID, title, "Renew your licence", "document_expiry", "admin-test")
	require.NoError(t, err)
	require.Equal(t, "SINGLE_DRIVER", camp["audience"])
	require.Equal(t, driverUser.ID, camp["target_driver_id"])

	var nType string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT type FROM notifications WHERE user_id = $1 AND title = $2`, driverUser.ID, title).Scan(&nType))
	require.Equal(t, "driver", nType)

	var otherCount int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM notifications WHERE user_id = $1 AND title = $2`, other.ID, title).Scan(&otherCount))
	require.Zero(t, otherCount, "a direct driver notice must not reach anyone else")

	// The campaign row records who it was for.
	var target string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT target_driver_id::text FROM admin_notifications WHERE id = $1`, camp["id"]).Scan(&target))
	require.Equal(t, driverUser.ID, target)
}

// A per-vehicle audience only reaches drivers of that vehicle type.
func TestNotificationCampaign_VehicleAudienceFiltersByTransportType(t *testing.T) {
	ctx := context.Background()
	repo := auth.NewRepository(pool)

	moto, err := repo.CreateUser(ctx, uniquePhone(), "dev-moto", "android", nil, nil)
	require.NoError(t, err)
	cab, err := repo.CreateUser(ctx, uniquePhone(), "dev-cab", "android", nil, nil)
	require.NoError(t, err)

	insertDriverProfile(t, ctx, moto.ID, "MOTO_BIKE")
	insertDriverProfile(t, ctx, cab.ID, "CAB_TAXI")

	svc := admin.NewService(pool, zerolog.Nop())
	title := "Moto only " + uniqueKey("v")
	_, err = svc.CreateNotificationCampaign(ctx, admin.CampaignInput{
		Title: title, Body: "Moto riders only", Audience: "DRIVER_MOTO", CreatedBy: "admin-test",
	})
	require.NoError(t, err)

	var motoCount, cabCount int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM notifications WHERE user_id = $1 AND title = $2`, moto.ID, title).Scan(&motoCount))
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM notifications WHERE user_id = $1 AND title = $2`, cab.ID, title).Scan(&cabCount))
	require.Equal(t, 1, motoCount, "the moto rider must receive it")
	require.Zero(t, cabCount, "the cab driver must not")
}

// insertDriverProfile creates a minimally-valid driver_profiles row (plate and
// licence are UNIQUE NOT NULL) and returns its id.
func insertDriverProfile(t *testing.T, ctx context.Context, userID, vehicle string) string {
	t.Helper()
	key := uniqueKey("d")
	var profileID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO driver_profiles
		    (user_id, transport_type, vehicle_plate, license_number, date_of_birth, city, momo_pay_code, approval_status)
		VALUES ($1, $2, $3, $4, '1995-01-01', 'Kigali', '+250788000000', 'APPROVED')
		RETURNING id`,
		userID, vehicle, "RA "+key[len(key)-6:], "DL-"+key).Scan(&profileID))
	return profileID
}
