package admin

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// nationalIDConflictErr fakes the exact Postgres error a 23505 violation of
// uq_users_national_id produces — the thing isNationalIDConflict must match
// on to tell "this national ID is taken" apart from any other unique
// constraint (phone, plate, license) a users/driver_profiles write might hit.
func nationalIDConflictErr() error {
	return &pgconn.PgError{Code: "23505", ConstraintName: "uq_users_national_id"}
}

// otherConflictErr fakes a 23505 on an UNRELATED constraint, so tests can
// confirm isNationalIDConflict does not fire on every unique violation.
func otherConflictErr() error {
	return &pgconn.PgError{Code: "23505", ConstraintName: "driver_profiles_vehicle_plate_key"}
}

func TestIsNationalIDConflict(t *testing.T) {
	assert.True(t, isNationalIDConflict(nationalIDConflictErr()))
	assert.False(t, isNationalIDConflict(otherConflictErr()), "a 23505 on a different constraint must not match")
	assert.False(t, isNationalIDConflict(errors.New("some other error")))
	assert.False(t, isNationalIDConflict(nil))
}

// ── SetDriverNationalID (admin-only edit path) ─────────────────────────────

func TestSetDriverNationalID_Success(t *testing.T) {
	// Confirms Normalize runs before the write: a lowercase country and a
	// dash/space-cluttered number reach the DB in canonical form.
	db := &mockDB{
		queryRowFn: func(ctx context.Context, sql string, args ...any) pgx.Row {
			if strings.Contains(sql, "SELECT user_id FROM driver_profiles") {
				return scanRow("user-1")
			}
			return errRow(pgx.ErrNoRows)
		},
		execFn: func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
			require.Contains(t, sql, "UPDATE users SET national_id_number")
			require.Equal(t, "1234567890ABCD", args[0])
			require.Equal(t, "UG", args[1])
			require.Equal(t, "user-1", args[2])
			return pgconn.CommandTag{}, nil
		},
	}
	svc := newTestService(db)

	masked, country, err := svc.SetDriverNationalID(context.Background(), "profile-1", "ug", "1234-5678-90abcd")
	require.NoError(t, err)
	assert.Equal(t, "UG", country)
	assert.Equal(t, "**********ABCD", masked)
}

func TestSetDriverNationalID_MaskedReturn(t *testing.T) {
	db := &mockDB{
		queryRowFn: func(ctx context.Context, sql string, args ...any) pgx.Row {
			return scanRow("user-1")
		},
		execFn: func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
			return pgconn.CommandTag{}, nil
		},
	}
	svc := newTestService(db)

	masked, country, err := svc.SetDriverNationalID(context.Background(), "profile-1", "RW", "1234567890123456")
	require.NoError(t, err)
	assert.Equal(t, "RW", country)
	assert.Equal(t, "************3456", masked, "SetDriverNationalID must return the MASKED number for audit logging, never the raw one")
}

func TestSetDriverNationalID_DriverNotFound(t *testing.T) {
	db := &mockDB{
		queryRowFn: func(ctx context.Context, sql string, args ...any) pgx.Row {
			return errRow(pgx.ErrNoRows)
		},
	}
	svc := newTestService(db)

	_, _, err := svc.SetDriverNationalID(context.Background(), "missing-profile", "RW", "1234567890123456")
	assert.ErrorIs(t, err, apperrors.ErrNotFound)
}

func TestSetDriverNationalID_InvalidFormat(t *testing.T) {
	db := &mockDB{
		queryRowFn: func(ctx context.Context, sql string, args ...any) pgx.Row {
			return scanRow("user-1")
		},
		execFn: func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
			t.Fatal("must not reach the DB write when the format is invalid")
			return pgconn.CommandTag{}, nil
		},
	}
	svc := newTestService(db)

	_, _, err := svc.SetDriverNationalID(context.Background(), "profile-1", "RW", "123") // too short
	require.Error(t, err)
	appErr, ok := err.(*apperrors.AppError)
	require.True(t, ok)
	assert.Equal(t, http.StatusBadRequest, appErr.StatusCode)
	assert.Equal(t, "INVALID_NATIONAL_ID", appErr.Code)
}

func TestSetDriverNationalID_DuplicateAcrossAccounts(t *testing.T) {
	db := &mockDB{
		queryRowFn: func(ctx context.Context, sql string, args ...any) pgx.Row {
			return scanRow("user-2")
		},
		execFn: func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
			return pgconn.CommandTag{}, nationalIDConflictErr()
		},
	}
	svc := newTestService(db)

	_, _, err := svc.SetDriverNationalID(context.Background(), "profile-2", "RW", "1234567890123456")
	require.Error(t, err)
	appErr, ok := err.(*apperrors.AppError)
	require.True(t, ok)
	assert.Equal(t, http.StatusConflict, appErr.StatusCode)
	assert.Equal(t, "NATIONAL_ID_ALREADY_REGISTERED", appErr.Code)
}

// ── CreateDriverFromAdmin national-ID paths ────────────────────────────────

func TestCreateDriverFromAdmin_InvalidNationalIDFormat(t *testing.T) {
	db := &mockDB{
		queryRowFn: func(ctx context.Context, sql string, args ...any) pgx.Row {
			t.Fatal("must not reach the DB when the national ID format is invalid")
			return nil
		},
	}
	svc := newTestService(db)

	_, err := svc.CreateDriverFromAdmin(context.Background(), AdminCreateDriverInput{
		Phone: "+250700000000", TransportType: "MOTO_BIKE", VehiclePlate: "RAA000A",
		LicenseNumber: "1234567890123456",
		NationalIDCountry: "RW", NationalIDNumber: "not-16-digits",
	})
	require.Error(t, err)
	appErr, ok := err.(*apperrors.AppError)
	require.True(t, ok)
	assert.Equal(t, http.StatusBadRequest, appErr.StatusCode)
	assert.Equal(t, "INVALID_NATIONAL_ID", appErr.Code)
}

func TestCreateDriverFromAdmin_NationalIDConflict_NewUser(t *testing.T) {
	db := &mockDB{
		queryRowFn: func(ctx context.Context, sql string, args ...any) pgx.Row {
			switch {
			case strings.Contains(sql, "FROM users WHERE phone_number"):
				return errRow(pgx.ErrNoRows) // user does not exist yet
			case strings.Contains(sql, "INSERT INTO users"):
				return errRow(nationalIDConflictErr())
			}
			t.Fatalf("unexpected QueryRow: %s", sql)
			return nil
		},
	}
	svc := newTestService(db)

	_, err := svc.CreateDriverFromAdmin(context.Background(), AdminCreateDriverInput{
		Phone: "+250700000001", FullName: "New Driver",
		TransportType: "MOTO_BIKE", VehiclePlate: "RAA000B", LicenseNumber: "1234567890123456",
		NationalIDCountry: "RW", NationalIDNumber: "1234567890123456",
	})
	require.Error(t, err)
	appErr, ok := err.(*apperrors.AppError)
	require.True(t, ok, "expected *apperrors.AppError, got %T: %v", err, err)
	assert.Equal(t, http.StatusConflict, appErr.StatusCode)
	assert.Equal(t, "NATIONAL_ID_ALREADY_REGISTERED", appErr.Code)
}

func TestCreateDriverFromAdmin_NationalIDConflict_ExistingUser(t *testing.T) {
	db := &mockDB{
		queryRowFn: func(ctx context.Context, sql string, args ...any) pgx.Row {
			switch {
			case strings.Contains(sql, "FROM users WHERE phone_number"):
				return scanRow("existing-user-1")
			case strings.Contains(sql, "FROM driver_profiles WHERE user_id"):
				return errRow(pgx.ErrNoRows) // no existing driver profile yet
			}
			t.Fatalf("unexpected QueryRow: %s", sql)
			return nil
		},
		execFn: func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
			switch {
			case strings.Contains(sql, "SET national_id_number"):
				return pgconn.CommandTag{}, nationalIDConflictErr()
			case strings.Contains(sql, "SET role_state"):
				return pgconn.CommandTag{}, nil
			}
			t.Fatalf("unexpected Exec: %s", sql)
			return pgconn.CommandTag{}, nil
		},
	}
	svc := newTestService(db)

	_, err := svc.CreateDriverFromAdmin(context.Background(), AdminCreateDriverInput{
		Phone: "+250700000002", FullName: "Existing Driver",
		TransportType: "MOTO_BIKE", VehiclePlate: "RAA000C", LicenseNumber: "6543210987654321",
		NationalIDCountry: "RW", NationalIDNumber: "1234567890123456",
	})
	require.Error(t, err)
	appErr, ok := err.(*apperrors.AppError)
	require.True(t, ok, "expected *apperrors.AppError, got %T: %v", err, err)
	assert.Equal(t, http.StatusConflict, appErr.StatusCode)
	assert.Equal(t, "NATIONAL_ID_ALREADY_REGISTERED", appErr.Code)
}

func TestCreateDriverFromAdmin_NationalIDOmitted_Unaffected(t *testing.T) {
	// Additive contract: a caller that never sends national_id_* fields must
	// behave exactly as it did before this feature existed — no extra DB call,
	// no validation error.
	var insertArgs []any
	db := &mockDB{
		queryRowFn: func(ctx context.Context, sql string, args ...any) pgx.Row {
			switch {
			case strings.Contains(sql, "FROM users WHERE phone_number"):
				return errRow(pgx.ErrNoRows)
			case strings.Contains(sql, "INSERT INTO users"):
				insertArgs = args
				return scanRow("new-user-1")
			case strings.Contains(sql, "FROM driver_profiles WHERE user_id"):
				return errRow(pgx.ErrNoRows)
			case strings.Contains(sql, "INSERT INTO driver_profiles"):
				return scanRow("new-profile-1")
			}
			t.Fatalf("unexpected QueryRow: %s", sql)
			return nil
		},
	}
	svc := newTestService(db)

	out, err := svc.CreateDriverFromAdmin(context.Background(), AdminCreateDriverInput{
		Phone: "+250700000003", FullName: "No National ID",
		TransportType: "MOTO_BIKE", VehiclePlate: "RAA000D", LicenseNumber: "1111222233334444",
	})
	require.NoError(t, err)
	assert.Equal(t, "new-profile-1", out["id"])
	// The two NULLIF($3,''), NULLIF($4,'') args must be empty strings (-> NULL),
	// not some default value invented for the omitted case.
	require.Len(t, insertArgs, 4)
	assert.Equal(t, "", insertArgs[2])
	assert.Equal(t, "", insertArgs[3])
}
