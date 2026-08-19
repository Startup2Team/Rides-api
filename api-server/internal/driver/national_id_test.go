package driver

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/config"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// nationalIDConflictErr fakes the exact Postgres error a 23505 violation of
// uq_users_national_id produces.
func nationalIDConflictErr() error {
	return &pgconn.PgError{Code: "23505", ConstraintName: "uq_users_national_id"}
}

// otherConflictErr fakes a 23505 on driver_profiles' OWN unique column
// (vehicle plate), so tests can confirm the two are told apart.
func otherConflictErr() error {
	return &pgconn.PgError{Code: "23505", ConstraintName: "driver_profiles_vehicle_plate_key"}
}

func TestIsNationalIDConflict(t *testing.T) {
	assert.True(t, isNationalIDConflict(nationalIDConflictErr()))
	assert.False(t, isNationalIDConflict(otherConflictErr()), "a 23505 on a different constraint must not match")
	assert.False(t, isNationalIDConflict(errors.New("boom")))
	assert.False(t, isNationalIDConflict(nil))
}

func TestSetUserNationalIDTx_NoopWhenNotProvided(t *testing.T) {
	// Both empty (additive "not supplied" case): must return nil WITHOUT
	// touching the transaction — passing a nil pgx.Tx here would panic on any
	// method call, so a passing test proves the early return fires.
	assert.NoError(t, setUserNationalIDTx(context.Background(), nil, "user-1", "", ""))
	assert.NoError(t, setUserNationalIDTx(context.Background(), nil, "user-1", "RW", ""))
	assert.NoError(t, setUserNationalIDTx(context.Background(), nil, "user-1", "", "1234567890123456"))
}

// ── mapApplyErr: the duplicate (23505) path Apply() relies on ─────────────

func TestMapApplyErr_NationalIDConflict(t *testing.T) {
	err := mapApplyErr(ErrNationalIDTaken)
	appErr, ok := err.(*apperrors.AppError)
	require.True(t, ok, "expected *apperrors.AppError, got %T", err)
	assert.Equal(t, http.StatusConflict, appErr.StatusCode)
	assert.Equal(t, "NATIONAL_ID_ALREADY_REGISTERED", appErr.Code)
}

func TestMapApplyErr_NationalIDConflict_Wrapped(t *testing.T) {
	// setUserNationalIDTx / repository callers may wrap ErrNationalIDTaken —
	// mapApplyErr must still recognize it via errors.Is, not ==.
	wrapped := errors.Join(errors.New("update users: "), ErrNationalIDTaken)
	err := mapApplyErr(wrapped)
	appErr, ok := err.(*apperrors.AppError)
	require.True(t, ok)
	assert.Equal(t, "NATIONAL_ID_ALREADY_REGISTERED", appErr.Code)
}

func TestMapApplyErr_OtherUniqueViolation(t *testing.T) {
	err := mapApplyErr(errors.New(`duplicate key value violates unique constraint "driver_profiles_vehicle_plate_key" (23505)`))
	appErr, ok := err.(*apperrors.AppError)
	require.True(t, ok)
	assert.Equal(t, http.StatusConflict, appErr.StatusCode)
	assert.Equal(t, "DUPLICATE_CREDENTIALS", appErr.Code)
}

func TestMapApplyErr_PassthroughOtherErrors(t *testing.T) {
	orig := errors.New("connection reset by peer")
	assert.Equal(t, orig, mapApplyErr(orig))
}

func TestMapApplyErr_Nil(t *testing.T) {
	assert.NoError(t, mapApplyErr(nil))
}

// ── Apply(): national ID is MANDATORY, rejected before any DB call ───────

func TestApply_InvalidNationalIDFormat_RejectsBeforeAnyDBCall(t *testing.T) {
	// repo wraps a nil *pgxpool.Pool: if Apply reached a real query it would
	// panic, so a clean 400 here proves the format guard runs FIRST.
	repo := NewRepository(nil)
	svc := NewService(repo, nil, nil, &config.Config{}, zerolog.Nop())

	// Both fields present but format-invalid — the fields exist, so this is a
	// format problem (INVALID_NATIONAL_ID), not a missing-field problem.
	cases := []struct {
		name    string
		country string
		number  string
	}{
		{"too short for RW", "RW", "123"},
		{"unsupported country", "KE", "1234567890123456"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Apply(context.Background(), ApplyInput{
				UserID:            "user-1",
				NationalIDCountry: tc.country,
				NationalIDNumber:  tc.number,
			})
			require.Error(t, err)
			appErr, ok := err.(*apperrors.AppError)
			require.True(t, ok, "expected *apperrors.AppError, got %T: %v", err, err)
			assert.Equal(t, http.StatusBadRequest, appErr.StatusCode)
			assert.Equal(t, "INVALID_NATIONAL_ID", appErr.Code)
		})
	}
}

// TestApply_NationalIDMissing_RejectsBeforeAnyDBCall proves the DB-1 round 2
// product decision: with NATIONAL_ID_REQUIRED on, national ID is MANDATORY to
// submit a driver application — omitting either field (or both) is rejected
// with NATIONAL_ID_REQUIRED before Apply ever reaches the repository (a nil
// pool would panic if it did, so a clean 400 here proves the guard runs
// first).
func TestApply_NationalIDMissing_RejectsBeforeAnyDBCall(t *testing.T) {
	repo := NewRepository(nil)
	svc := NewService(repo, nil, nil, &config.Config{Driver: config.DriverConfig{NationalIDRequired: true}}, zerolog.Nop())

	cases := []struct {
		name    string
		country string
		number  string
	}{
		{"both omitted", "", ""},
		{"country without number", "RW", ""},
		{"number without country", "", "1234567890123456"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Apply(context.Background(), ApplyInput{
				UserID:            "user-1",
				NationalIDCountry: tc.country,
				NationalIDNumber:  tc.number,
			})
			require.Error(t, err)
			appErr, ok := err.(*apperrors.AppError)
			require.True(t, ok, "expected *apperrors.AppError, got %T: %v", err, err)
			assert.Equal(t, http.StatusBadRequest, appErr.StatusCode)
			assert.Equal(t, "NATIONAL_ID_REQUIRED", appErr.Code)
		})
	}
}

// ── resolveNationalIDInput: NATIONAL_ID_REQUIRED staged-rollout gate ─────
//
// These test the pure decision function directly (no DB involved), covering
// the ON/OFF matrix Apply relies on.

func TestResolveNationalIDInput_FlagOff_Missing_NoErrorNoValue(t *testing.T) {
	// Flag off + nothing supplied: old app versions that don't send these
	// fields yet must keep applying exactly as before — no error, no value.
	country, number, err := resolveNationalIDInput(false, "", "")
	require.NoError(t, err)
	assert.Empty(t, country)
	assert.Empty(t, number)
}

func TestResolveNationalIDInput_FlagOff_Partial_TreatedAsNotSupplied(t *testing.T) {
	cases := []struct{ country, number string }{
		{"RW", ""},
		{"", "1234567890123456"},
	}
	for _, tc := range cases {
		country, number, err := resolveNationalIDInput(false, tc.country, tc.number)
		require.NoError(t, err)
		assert.Empty(t, country)
		assert.Empty(t, number)
	}
}

func TestResolveNationalIDInput_FlagOn_Missing_Required(t *testing.T) {
	_, _, err := resolveNationalIDInput(true, "", "")
	require.Error(t, err)
	appErr, ok := err.(*apperrors.AppError)
	require.True(t, ok)
	assert.Equal(t, http.StatusBadRequest, appErr.StatusCode)
	assert.Equal(t, "NATIONAL_ID_REQUIRED", appErr.Code)
}

func TestResolveNationalIDInput_BothPresent_NormalizesAndValidates_RegardlessOfFlag(t *testing.T) {
	// Capture/validation stays active whenever a value IS supplied — the flag
	// only gates whether it must be present at all.
	for _, required := range []bool{true, false} {
		country, number, err := resolveNationalIDInput(required, "rw", "1234 5678-9012 3456")
		require.NoError(t, err)
		assert.Equal(t, "RW", country)
		assert.Equal(t, "1234567890123456", number)
	}
}

func TestResolveNationalIDInput_InvalidFormat_RejectedRegardlessOfFlag(t *testing.T) {
	for _, required := range []bool{true, false} {
		_, _, err := resolveNationalIDInput(required, "RW", "123")
		require.Error(t, err)
		appErr, ok := err.(*apperrors.AppError)
		require.True(t, ok)
		assert.Equal(t, "INVALID_NATIONAL_ID", appErr.Code)
	}
}
