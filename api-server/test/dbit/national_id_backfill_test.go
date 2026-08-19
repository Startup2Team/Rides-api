//go:build integration

package dbit

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/internal/auth"
	"github.com/workspace/ride-platform/internal/driver"
	"github.com/workspace/ride-platform/internal/nationalidbackfill"
)

// These are the two properties cmd/national-id-backfill's unit tests
// (internal/nationalidbackfill/backfill_test.go) can't prove, because they
// need a REAL `driver_profiles` JOIN against a REAL Postgres:
//
//  1. a non-driver user (no driver_profiles row at all) must never be touched
//     by -apply — the candidate query JOINs driver_profiles, so a customer-only
//     account must never surface as a candidate.
//  2. re-running -apply a second time must backfill ZERO — the command's
//     idempotency claim (candidate SELECT + the UPDATE's own WHERE
//     national_id_number IS NULL guard).

// TestNationalIDBackfill_NonDriverUser_LeftUntouched proves a plain user
// (registered but never applied to drive, so no driver_profiles row exists)
// is excluded from the candidate set and left completely alone by -apply.
func TestNationalIDBackfill_NonDriverUser_LeftUntouched(t *testing.T) {
	ctx := context.Background()
	authRepo := auth.NewRepository(pool)

	nonDriver, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-nidbf-1", "android", nil, nil)
	require.NoError(t, err)
	require.Nil(t, queryNationalIDNumber(t, ctx, nonDriver.ID), "sanity: freshly created user has no national ID on file")

	var stderr bytes.Buffer
	_, err = nationalidbackfill.Run(ctx, pool, true, &stderr)
	require.NoError(t, err)

	require.Nil(t, queryNationalIDNumber(t, ctx, nonDriver.ID),
		"a user with no driver_profiles row must never be assigned a placeholder national ID")
}

// queryNationalIDNumber reads users.national_id_number straight from
// Postgres — auth.User (the repository's own read model) doesn't carry this
// column, and driver.Profile only exists for users who have a driver_profiles
// row, which is exactly the case TestNationalIDBackfill_NonDriverUser_LeftUntouched
// needs to check for a user who deliberately has neither.
func queryNationalIDNumber(t *testing.T, ctx context.Context, userID string) *string {
	t.Helper()
	var number *string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT national_id_number FROM users WHERE id = $1`, userID,
	).Scan(&number))
	return number
}

// TestNationalIDBackfill_SecondApplyRun_BackfillsZero proves the command is
// safe to re-run: once every eligible driver has a national ID on file (real
// or a placeholder from the first run), a second -apply run must find and
// write nothing.
func TestNationalIDBackfill_SecondApplyRun_BackfillsZero(t *testing.T) {
	ctx := context.Background()
	authRepo := auth.NewRepository(pool)
	driverRepo := driver.NewRepository(pool)

	u, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-nidbf-2", "android", nil, nil)
	require.NoError(t, err)

	// A driver with driver_profiles but NO national ID captured — exactly the
	// candidate shape this command targets. NATIONAL_ID_REQUIRED gating lives
	// above the repository (service/handler layer), so CreateProfile with no
	// national ID fields succeeds regardless of the flag, same as the
	// pre-DB-1 drivers this command exists to backfill.
	in := newDriverApplyInput(t, u.ID)
	profile, err := driverRepo.CreateProfile(ctx, in)
	require.NoError(t, err)
	require.Nil(t, profile.NationalIDNumber, "sanity: driver has no national ID on file yet")

	var stderr1 bytes.Buffer
	first, err := nationalidbackfill.Run(ctx, pool, true, &stderr1)
	require.NoError(t, err)
	require.GreaterOrEqual(t, first.Candidates, 1, "our newly created driver must show up as a candidate")
	require.GreaterOrEqual(t, first.Backfilled, 1, "the first -apply run must backfill at least our driver")
	require.Empty(t, stderr1.String(), "the first run must not warn about anything")

	afterFirst, err := driverRepo.FindProfileByUserID(ctx, u.ID)
	require.NoError(t, err)
	require.NotNil(t, afterFirst.NationalIDNumber, "the driver must have a national ID on file after the first run")
	assignedID := *afterFirst.NationalIDNumber

	var stderr2 bytes.Buffer
	second, err := nationalidbackfill.Run(ctx, pool, true, &stderr2)
	require.NoError(t, err)
	require.Equal(t, 0, second.Candidates, "every driver now has a national ID on file — the candidate query must return zero rows")
	require.Equal(t, 0, second.Backfilled, "a second -apply run must backfill nothing")
	require.Empty(t, stderr2.String())

	afterSecond, err := driverRepo.FindProfileByUserID(ctx, u.ID)
	require.NoError(t, err)
	require.NotNil(t, afterSecond.NationalIDNumber)
	require.Equal(t, assignedID, *afterSecond.NationalIDNumber,
		"the second run must not overwrite the ID the first run assigned")
}
