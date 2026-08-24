//go:build integration

package dbit

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/internal/waitlist"
)

// Migration 087 (phone-optional waitlist signups): the original
// UNIQUE(role, phone) constraint never dedupes NULL phones, so an email-only
// signup needs the partial unique index added alongside it. These are the
// tests the unit suite (fakeRepo in internal/waitlist/service_test.go) can't
// be — they need the REAL constraint/index to fire a REAL 23505 against the
// REAL migrated schema, inside repository.Create's real ON CONFLICT handling.

func newWaitlistInput(role string) waitlist.CreateInput {
	return waitlist.CreateInput{
		Role:          role,
		Name:          "Test User",
		ConsentLaunch: true,
	}
}

func TestWaitlistRepository_PhoneDedupe_StillWorks(t *testing.T) {
	if pool == nil {
		t.Skip("no database")
	}
	ctx := context.Background()
	repo := waitlist.NewRepository(pool)

	in := newWaitlistInput(waitlist.RoleCustomer)
	in.Phone = uniquePhone()

	first, created, err := repo.Create(ctx, in)
	require.NoError(t, err)
	require.True(t, created)

	second, created, err := repo.Create(ctx, in)
	require.NoError(t, err)
	assert.False(t, created, "resubmitting the same (role, phone) must be idempotent")
	assert.Equal(t, first.ID, second.ID)
	assert.Equal(t, first.ReferralCode, second.ReferralCode)
}

func TestWaitlistRepository_PhoneAbsent_Succeeds_NoDedupeAcrossRuns(t *testing.T) {
	if pool == nil {
		t.Skip("no database")
	}
	ctx := context.Background()
	repo := waitlist.NewRepository(pool)

	in := newWaitlistInput(waitlist.RoleCustomer) // no phone, no email

	first, created, err := repo.Create(ctx, in)
	require.NoError(t, err)
	require.True(t, created)
	assert.Empty(t, first.Phone, "an absent phone must persist (and read back) as empty, not error")

	// Neither phone nor email: nothing to dedupe against — this is the
	// accepted gap from migration 087. A second identical submission is a
	// brand new row, not an idempotent no-op.
	second, created, err := repo.Create(ctx, in)
	require.NoError(t, err)
	assert.True(t, created, "a signup with neither phone nor email has no dedupe key — every submission is new")
	assert.NotEqual(t, first.ID, second.ID)
}

func TestWaitlistRepository_EmailOnlyDedupes(t *testing.T) {
	if pool == nil {
		t.Skip("no database")
	}
	ctx := context.Background()
	repo := waitlist.NewRepository(pool)

	email := uniqueKey("waitlist-email") + "@example.com"
	in := newWaitlistInput(waitlist.RoleDriver)
	in.Email = &email
	vt := "MOTO_BIKE"
	in.VehicleType = &vt

	first, created, err := repo.Create(ctx, in)
	require.NoError(t, err)
	require.True(t, created)
	require.NotNil(t, first.Email)
	assert.Equal(t, email, *first.Email)

	// Resubmitting the same (role, email) with no phone must dedupe via the
	// partial unique index (migration 087), not create a second row.
	second, created, err := repo.Create(ctx, in)
	require.NoError(t, err)
	assert.False(t, created, "resubmitting the same (role, email) with no phone must be idempotent")
	assert.Equal(t, first.ID, second.ID)
	assert.Equal(t, first.ReferralCode, second.ReferralCode)
}

func TestWaitlistRepository_EmailOnlyDedupe_IsPerRole(t *testing.T) {
	if pool == nil {
		t.Skip("no database")
	}
	ctx := context.Background()
	repo := waitlist.NewRepository(pool)

	email := uniqueKey("waitlist-email-role") + "@example.com"

	customerIn := newWaitlistInput(waitlist.RoleCustomer)
	customerIn.Email = &email
	_, created, err := repo.Create(ctx, customerIn)
	require.NoError(t, err)
	require.True(t, created)

	driverIn := newWaitlistInput(waitlist.RoleDriver)
	driverIn.Email = &email
	vt := "CAB_TAXI"
	driverIn.VehicleType = &vt
	_, created, err = repo.Create(ctx, driverIn)
	require.NoError(t, err)
	assert.True(t, created, "the same email on a different role is a distinct waitlist entry, not a duplicate")
}
