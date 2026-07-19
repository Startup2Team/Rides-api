//go:build integration

package dbit

import (
	"context"
	"testing"

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
	camp, err := svc.CreateNotificationCampaign(ctx, "Promo "+uniqueKey("t"), "Body text", "ALL", "admin-test")
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
