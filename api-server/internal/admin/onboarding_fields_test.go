package admin

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/config"
	"github.com/workspace/ride-platform/pkg/adminrole"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// ── CreateDriverFromAdmin: gender round-trip (FEAT-onboarding-fields) ────
//
// Admin-created drivers previously had no way to record gender at all — this
// proves the value supplied on the request reaches the driver_profiles
// INSERT.

func TestCreateDriverFromAdmin_GenderRoundTrip(t *testing.T) {
	var insertArgs []any
	tx := &customMockTx{
		queryRowFn: func(ctx context.Context, sql string, args ...any) pgx.Row {
			switch {
			case strings.Contains(sql, "FROM users WHERE phone_number"):
				return errRow(pgx.ErrNoRows) // user does not exist yet
			case strings.Contains(sql, "INSERT INTO users"):
				return scanRow("new-user-gender-1")
			case strings.Contains(sql, "FROM driver_profiles WHERE user_id"):
				return errRow(pgx.ErrNoRows) // no existing driver profile yet
			case strings.Contains(sql, "INSERT INTO driver_profiles"):
				insertArgs = args
				return scanRow("new-profile-gender-1")
			}
			t.Fatalf("unexpected QueryRow: %s", sql)
			return nil
		},
		execFn: func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
			return pgconn.CommandTag{}, nil
		},
	}
	svc := newTestService(&mockDB{
		beginFn: func(ctx context.Context) (pgx.Tx, error) { return tx, nil },
	})

	_, err := svc.CreateDriverFromAdmin(context.Background(), AdminCreateDriverInput{
		Phone: "+250700000020", FullName: "Gender Test",
		TransportType: "MOTO_BIKE", VehiclePlate: "RAA000Z", LicenseNumber: "1112223334445556",
		NationalIDCountry: "RW", NationalIDNumber: "1112223334445556",
		Gender: "female",
	})
	require.NoError(t, err)
	require.NotEmpty(t, insertArgs, "INSERT INTO driver_profiles must have been reached")
	assert.Contains(t, insertArgs, "female", "gender must be passed through to the driver_profiles INSERT")
	assert.True(t, tx.committed)
}

func TestCreateDriverFromAdmin_GenderOmitted_RoundTripsEmpty(t *testing.T) {
	// Gender is OPTIONAL — omitting it must not break registration, and the
	// value written is the zero value (""), same as the self-registration
	// path's plain (non-pointer) Gender field.
	var insertArgs []any
	tx := &customMockTx{
		queryRowFn: func(ctx context.Context, sql string, args ...any) pgx.Row {
			switch {
			case strings.Contains(sql, "FROM users WHERE phone_number"):
				return errRow(pgx.ErrNoRows)
			case strings.Contains(sql, "INSERT INTO users"):
				return scanRow("new-user-gender-2")
			case strings.Contains(sql, "FROM driver_profiles WHERE user_id"):
				return errRow(pgx.ErrNoRows)
			case strings.Contains(sql, "INSERT INTO driver_profiles"):
				insertArgs = args
				return scanRow("new-profile-gender-2")
			}
			t.Fatalf("unexpected QueryRow: %s", sql)
			return nil
		},
		execFn: func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
			return pgconn.CommandTag{}, nil
		},
	}
	svc := newTestService(&mockDB{
		beginFn: func(ctx context.Context) (pgx.Tx, error) { return tx, nil },
	})

	_, err := svc.CreateDriverFromAdmin(context.Background(), AdminCreateDriverInput{
		Phone: "+250700000021", FullName: "No Gender",
		TransportType: "MOTO_BIKE", VehiclePlate: "RAA000Y", LicenseNumber: "6665554443332221",
		NationalIDCountry: "RW", NationalIDNumber: "6665554443332221",
	})
	require.NoError(t, err)
	require.NotEmpty(t, insertArgs)
	assert.Contains(t, insertArgs, "", "an omitted gender is passed through as the zero value, not dropped")
}

// ── GetDriver: gender exposure ────────────────────────────────────────────

func TestGetDriver_ExposesGender(t *testing.T) {
	genderVal := "male"
	svc := newTestService(&mockDB{
		queryRowFn: func(ctx context.Context, sql string, args ...any) pgx.Row {
			if strings.Contains(sql, "FROM driver_profiles dp JOIN users u") {
				return scanRow(
					"profile-1", "user-1", "+250780000000", "Bob Driver", nil,
					"MOTO_BIKE", "RAD001A", "1234567890123456",
					nil, "Kigali",
					"Kigali", "Gasabo", "Kimironko", "Kibagabaga", "Village1",
					nil, nil,
					"mtn", "123456", nil,
					"APPROVED", nil, nil,
					95.0, 10, true,
					nil, nil, nil,
					nil,
					nil, "RW",
					genderVal,
				)
			}
			return errRow(pgx.ErrNoRows)
		},
		queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) { return &emptyRows{}, nil },
	})
	driver, err := svc.GetDriver(context.Background(), "profile-1", adminrole.SuperAdmin)
	require.NoError(t, err)
	require.NotNil(t, driver["gender"])
	assert.Equal(t, "male", *driver["gender"].(*string))
}

func TestGetDriver_NoGenderOnFile_Nil(t *testing.T) {
	svc := newTestService(&mockDB{
		queryRowFn: driverRowQueryRowFn(nil), // trailing gender destination left at zero value (nil)
		queryFn:    func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) { return &emptyRows{}, nil },
	})
	driver, err := svc.GetDriver(context.Background(), "profile-1", adminrole.SuperAdmin)
	require.NoError(t, err)
	assert.Nil(t, driver["gender"])
}

// ── NATIONAL_ID_REQUIRED staged-rollout gate (config.Driver.NationalIDRequired) ──

func TestResolveNationalIDInput_Admin_FlagOff_Missing_NoErrorNoValue(t *testing.T) {
	country, number, err := resolveNationalIDInput(false, "", "")
	require.NoError(t, err)
	assert.Empty(t, country)
	assert.Empty(t, number)
}

func TestResolveNationalIDInput_Admin_FlagOn_Missing_Required(t *testing.T) {
	_, _, err := resolveNationalIDInput(true, "", "")
	require.Error(t, err)
	appErr, ok := err.(*apperrors.AppError)
	require.True(t, ok)
	assert.Equal(t, http.StatusBadRequest, appErr.StatusCode)
	assert.Equal(t, "NATIONAL_ID_REQUIRED", appErr.Code)
}

func TestResolveNationalIDInput_Admin_BothPresent_ValidatesRegardlessOfFlag(t *testing.T) {
	for _, required := range []bool{true, false} {
		country, number, err := resolveNationalIDInput(required, "rw", "1234 5678-9012 3456")
		require.NoError(t, err)
		assert.Equal(t, "RW", country)
		assert.Equal(t, "1234567890123456", number)
	}
}

// TestApproveDriver_NoNationalIDOnFile_AllowedWhenFlagOff proves the DB-1
// staged rollout: with NATIONAL_ID_REQUIRED off (the shipped default), a
// driver approval must succeed exactly as it did before DB-1 introduced the
// mandatory gate — old app versions that don't send national ID yet must not
// be blocked from approval.
func TestApproveDriver_NoNationalIDOnFile_AllowedWhenFlagOff(t *testing.T) {
	execCount := 0
	svc := &Service{
		db: &mockDB{
			queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
				return scanRow("driver-uuid", "MOTO_BIKE", nil) // no national ID on file
			},
			execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
				execCount++
				return pgconn.CommandTag{}, nil
			},
		},
		cfg: &config.Config{Driver: config.DriverConfig{NationalIDRequired: false}},
	}
	err := svc.ApproveDriver(context.Background(), "profile-xyz", "admin-uuid")
	require.NoError(t, err)
	assert.Equal(t, 2, execCount)
}

// TestApproveDriver_NoNationalIDOnFile_RejectedWhenFlagOn locks in that the
// gate is still fully enforced once the flag is explicitly turned on — same
// assertion as TestApproveDriver_NoNationalIDOnFile_Rejected in
// national_id_test.go, constructed directly here instead of via
// newTestService for clarity that the flag (not the helper's default) is
// what's under test.
func TestApproveDriver_NoNationalIDOnFile_RejectedWhenFlagOn(t *testing.T) {
	svc := &Service{
		db: &mockDB{
			queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
				return scanRow("driver-uuid", "MOTO_BIKE", nil)
			},
		},
		cfg: &config.Config{Driver: config.DriverConfig{NationalIDRequired: true}},
	}
	err := svc.ApproveDriver(context.Background(), "profile-xyz", "admin-uuid")
	require.Error(t, err)
	appErr, ok := err.(*apperrors.AppError)
	require.True(t, ok)
	assert.Equal(t, "NATIONAL_ID_REQUIRED", appErr.Code)
}

// TestApproveDriver_NoNationalIDOnFile_NilConfig_TreatedAsFlagOff proves a
// Service that never had SetConfig called (nil cfg) fails safe to "not
// required", not to a panic or the stricter behaviour.
func TestApproveDriver_NoNationalIDOnFile_NilConfig_TreatedAsFlagOff(t *testing.T) {
	svc := &Service{
		db: &mockDB{
			queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
				return scanRow("driver-uuid", "MOTO_BIKE", nil)
			},
			execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
				return pgconn.CommandTag{}, nil
			},
		},
		// cfg intentionally left nil.
	}
	err := svc.ApproveDriver(context.Background(), "profile-xyz", "admin-uuid")
	require.NoError(t, err)
}

func TestReinstateDriver_NoNationalID_AllowedWhenFlagOff(t *testing.T) {
	tx := &mockTx{}
	svc := &Service{
		db: &mockDB{
			queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
				return scanRow(nil) // national_id_number NULL
			},
			beginFn: func(_ context.Context) (pgx.Tx, error) { return tx, nil },
		},
		cfg: &config.Config{Driver: config.DriverConfig{NationalIDRequired: false}},
	}
	err := svc.ReinstateDriver(context.Background(), "profile-xyz")
	require.NoError(t, err)
	assert.True(t, tx.committed)
}

func TestCreateDriverFromAdmin_NationalIDOmitted_AllowedWhenFlagOff(t *testing.T) {
	tx := &customMockTx{
		queryRowFn: func(ctx context.Context, sql string, args ...any) pgx.Row {
			switch {
			case strings.Contains(sql, "FROM users WHERE phone_number"):
				return errRow(pgx.ErrNoRows)
			case strings.Contains(sql, "INSERT INTO users"):
				return scanRow("new-user-flagoff-1")
			case strings.Contains(sql, "FROM driver_profiles WHERE user_id"):
				return errRow(pgx.ErrNoRows)
			case strings.Contains(sql, "INSERT INTO driver_profiles"):
				return scanRow("new-profile-flagoff-1")
			}
			t.Fatalf("unexpected QueryRow: %s", sql)
			return nil
		},
		execFn: func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
			if strings.Contains(sql, "SET national_id_number") {
				t.Fatal("must not attempt a national-ID capture write when none was supplied")
			}
			return pgconn.CommandTag{}, nil
		},
	}
	svc := &Service{
		db:  &mockDB{beginFn: func(ctx context.Context) (pgx.Tx, error) { return tx, nil }},
		log: newTestService(&mockDB{}).log,
		cfg: &config.Config{Driver: config.DriverConfig{NationalIDRequired: false}},
	}

	_, err := svc.CreateDriverFromAdmin(context.Background(), AdminCreateDriverInput{
		Phone: "+250700000022", FullName: "Flag Off, No National ID",
		TransportType: "MOTO_BIKE", VehiclePlate: "RAA000X", LicenseNumber: "2223334445556667",
	})
	require.NoError(t, err)
	assert.True(t, tx.committed)
}
