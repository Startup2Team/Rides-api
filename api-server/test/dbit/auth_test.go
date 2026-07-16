//go:build integration

package dbit

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/internal/auth"
)

func TestAuth_CreateAndFindUser(t *testing.T) {
	ctx := context.Background()
	repo := auth.NewRepository(pool)
	phone := uniquePhone()
	name := "Integration Tester"

	u, err := repo.CreateUser(ctx, phone, "dev-abc", "ios", &name, nil)
	require.NoError(t, err)
	require.NotEmpty(t, u.ID)
	require.Equal(t, phone, u.PhoneNumber)

	found, err := repo.FindUserByPhone(ctx, phone)
	require.NoError(t, err)
	require.Equal(t, u.ID, found.ID)

	byID, err := repo.FindUserByID(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, phone, byID.PhoneNumber)
}

func TestAuth_OTPLifecycle(t *testing.T) {
	ctx := context.Background()
	repo := auth.NewRepository(pool)
	phone := uniquePhone()

	require.NoError(t, repo.CreateOTP(ctx, phone, "hashed-otp", "registration", time.Now().Add(10*time.Minute)))

	rec, err := repo.FindLatestOTP(ctx, phone, "registration")
	require.NoError(t, err)
	require.Equal(t, "hashed-otp", rec.OTPHash)
	require.False(t, rec.IsUsed)

	require.NoError(t, repo.MarkOTPUsed(ctx, rec.ID))

	// The row is now flagged used …
	var isUsed bool
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT is_used FROM otp_verifications WHERE id = $1`, rec.ID).Scan(&isUsed))
	require.True(t, isUsed, "MarkOTPUsed must set is_used = true")

	// … and FindLatestOTP (which only returns unused, unexpired codes) no longer
	// returns it — so a used OTP can't be replayed.
	_, err = repo.FindLatestOTP(ctx, phone, "registration")
	require.Error(t, err, "a used OTP must not be findable/replayable")
}
