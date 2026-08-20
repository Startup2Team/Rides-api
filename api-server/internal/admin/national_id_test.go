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

	"github.com/workspace/ride-platform/pkg/adminrole"
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
			if strings.Contains(sql, "FROM driver_profiles dp") {
				return scanRow("user-1", nil) // no prior national ID on file
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

	oldMasked, newMasked, country, err := svc.SetDriverNationalID(context.Background(), "profile-1", "ug", "1234-5678-90abcd")
	require.NoError(t, err)
	assert.Equal(t, "UG", country)
	assert.Equal(t, "", oldMasked, "no prior ID on file — nothing to show as the old value")
	assert.Equal(t, "**********ABCD", newMasked)
}

func TestSetDriverNationalID_MaskedReturn(t *testing.T) {
	db := &mockDB{
		queryRowFn: func(ctx context.Context, sql string, args ...any) pgx.Row {
			return scanRow("user-1", nil)
		},
		execFn: func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
			return pgconn.CommandTag{}, nil
		},
	}
	svc := newTestService(db)

	_, newMasked, country, err := svc.SetDriverNationalID(context.Background(), "profile-1", "RW", "1234567890123456")
	require.NoError(t, err)
	assert.Equal(t, "RW", country)
	assert.Equal(t, "************3456", newMasked, "SetDriverNationalID must return the MASKED new number for audit logging, never the raw one")
}

// TestSetDriverNationalID_AuditsOldAndNew proves the DB-1 round 2 fix: a
// correction records BOTH the masked old value and the masked new value, not
// just the new one — otherwise a correction isn't reviewable after the fact.
func TestSetDriverNationalID_AuditsOldAndNew(t *testing.T) {
	db := &mockDB{
		queryRowFn: func(ctx context.Context, sql string, args ...any) pgx.Row {
			return scanRow("user-1", "1111111111111111") // a value already on file
		},
		execFn: func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
			return pgconn.CommandTag{}, nil
		},
	}
	svc := newTestService(db)

	oldMasked, newMasked, _, err := svc.SetDriverNationalID(context.Background(), "profile-1", "RW", "2222222222222222")
	require.NoError(t, err)
	assert.Equal(t, "************1111", oldMasked)
	assert.Equal(t, "************2222", newMasked)
	assert.NotEqual(t, oldMasked, newMasked, "old and new must be distinguishable in the audit trail")
}

func TestSetDriverNationalID_DriverNotFound(t *testing.T) {
	db := &mockDB{
		queryRowFn: func(ctx context.Context, sql string, args ...any) pgx.Row {
			return errRow(pgx.ErrNoRows)
		},
	}
	svc := newTestService(db)

	_, _, _, err := svc.SetDriverNationalID(context.Background(), "missing-profile", "RW", "1234567890123456")
	assert.ErrorIs(t, err, apperrors.ErrNotFound)
}

func TestSetDriverNationalID_InvalidFormat(t *testing.T) {
	db := &mockDB{
		queryRowFn: func(ctx context.Context, sql string, args ...any) pgx.Row {
			return scanRow("user-1", nil)
		},
		execFn: func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
			t.Fatal("must not reach the DB write when the format is invalid")
			return pgconn.CommandTag{}, nil
		},
	}
	svc := newTestService(db)

	_, _, _, err := svc.SetDriverNationalID(context.Background(), "profile-1", "RW", "123") // too short
	require.Error(t, err)
	appErr, ok := err.(*apperrors.AppError)
	require.True(t, ok)
	assert.Equal(t, http.StatusBadRequest, appErr.StatusCode)
	assert.Equal(t, "INVALID_NATIONAL_ID", appErr.Code)
}

func TestSetDriverNationalID_DuplicateAcrossAccounts(t *testing.T) {
	db := &mockDB{
		queryRowFn: func(ctx context.Context, sql string, args ...any) pgx.Row {
			return scanRow("user-2", nil)
		},
		execFn: func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
			return pgconn.CommandTag{}, nationalIDConflictErr()
		},
	}
	svc := newTestService(db)

	_, _, _, err := svc.SetDriverNationalID(context.Background(), "profile-2", "RW", "1234567890123456")
	require.Error(t, err)
	appErr, ok := err.(*apperrors.AppError)
	require.True(t, ok)
	assert.Equal(t, http.StatusConflict, appErr.StatusCode)
	assert.Equal(t, "NATIONAL_ID_ALREADY_REGISTERED", appErr.Code)
}

// ── ApproveDriver: mandatory-national-ID defensive gate ───────────────────

func TestApproveDriver_NoNationalIDOnFile_Rejected(t *testing.T) {
	// DB-1 round 2: ApproveDriver refuses to approve a driver with no
	// national ID captured — this is what makes the uniqueness guard actually
	// prevent ban-evasion (an approved driver with no ID can't be caught by
	// it later).
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return scanRow("driver-uuid", "MOTO_BIKE", nil) // national_id_number NULL
		},
	})
	err := svc.ApproveDriver(context.Background(), "profile-xyz", "admin-uuid")
	require.Error(t, err)
	appErr, ok := err.(*apperrors.AppError)
	require.True(t, ok, "expected *apperrors.AppError, got %T: %v", err, err)
	assert.Equal(t, http.StatusConflict, appErr.StatusCode)
	assert.Equal(t, "NATIONAL_ID_REQUIRED", appErr.Code)
}

func TestApproveDriver_EmptyNationalIDOnFile_Rejected(t *testing.T) {
	svc := newTestService(&mockDB{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return scanRow("driver-uuid", "MOTO_BIKE", "") // present but empty
		},
	})
	err := svc.ApproveDriver(context.Background(), "profile-xyz", "admin-uuid")
	require.Error(t, err)
	appErr, ok := err.(*apperrors.AppError)
	require.True(t, ok)
	assert.Equal(t, "NATIONAL_ID_REQUIRED", appErr.Code)
}

// ── GetDriver: role-gated national ID exposure ────────────────────────────

func driverRowQueryRowFn(nationalID any) func(context.Context, string, ...any) pgx.Row {
	return func(ctx context.Context, sql string, args ...any) pgx.Row {
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
				nationalID, "RW",
			)
		}
		return errRow(pgx.ErrNoRows)
	}
}

func TestGetDriver_SuperAdmin_SeesFullNationalID(t *testing.T) {
	svc := newTestService(&mockDB{
		queryRowFn: driverRowQueryRowFn("1234567890123456"),
		queryFn:    func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) { return &emptyRows{}, nil },
	})
	driver, err := svc.GetDriver(context.Background(), "profile-1", adminrole.SuperAdmin)
	require.NoError(t, err)
	require.NotNil(t, driver["national_id_number"])
	assert.Equal(t, "1234567890123456", *driver["national_id_number"].(*string))
}

func TestGetDriver_OpsManager_SeesFullNationalID(t *testing.T) {
	svc := newTestService(&mockDB{
		queryRowFn: driverRowQueryRowFn("1234567890123456"),
		queryFn:    func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) { return &emptyRows{}, nil },
	})
	driver, err := svc.GetDriver(context.Background(), "profile-1", adminrole.OpsManager)
	require.NoError(t, err)
	require.NotNil(t, driver["national_id_number"])
	assert.Equal(t, "1234567890123456", *driver["national_id_number"].(*string))
}

func TestGetDriver_SupportStaff_SeesMaskedNationalID(t *testing.T) {
	svc := newTestService(&mockDB{
		queryRowFn: driverRowQueryRowFn("1234567890123456"),
		queryFn:    func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) { return &emptyRows{}, nil },
	})
	driver, err := svc.GetDriver(context.Background(), "profile-1", adminrole.SupportStaff)
	require.NoError(t, err)
	require.NotNil(t, driver["national_id_number"])
	assert.Equal(t, "************3456", *driver["national_id_number"].(*string),
		"SupportStaff must never see the full national ID")
}

func TestGetDriver_UnknownRole_SeesMaskedNationalID(t *testing.T) {
	// Defaults closed: an unrecognised/empty role masks, same as SupportStaff.
	svc := newTestService(&mockDB{
		queryRowFn: driverRowQueryRowFn("1234567890123456"),
		queryFn:    func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) { return &emptyRows{}, nil },
	})
	driver, err := svc.GetDriver(context.Background(), "profile-1", "")
	require.NoError(t, err)
	assert.Equal(t, "************3456", *driver["national_id_number"].(*string))
}

func TestGetDriver_NoNationalIDOnFile_NilForEveryRole(t *testing.T) {
	svc := newTestService(&mockDB{
		queryRowFn: driverRowQueryRowFn(nil),
		queryFn:    func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) { return &emptyRows{}, nil },
	})
	driver, err := svc.GetDriver(context.Background(), "profile-1", adminrole.SuperAdmin)
	require.NoError(t, err)
	assert.Nil(t, driver["national_id_number"])
}

// ── CreateDriverFromAdmin: national ID is now MANDATORY ───────────────────

func TestCreateDriverFromAdmin_InvalidNationalIDFormat(t *testing.T) {
	db := &mockDB{
		queryRowFn: func(ctx context.Context, sql string, args ...any) pgx.Row {
			t.Fatal("must not reach the DB when the national ID format is invalid")
			return nil
		},
		beginFn: func(ctx context.Context) (pgx.Tx, error) {
			t.Fatal("must not open a transaction when the national ID format is invalid")
			return nil, nil
		},
	}
	svc := newTestService(db)

	_, err := svc.CreateDriverFromAdmin(context.Background(), AdminCreateDriverInput{
		Phone: "+250700000000", TransportType: "MOTO_BIKE", VehiclePlate: "RAA000A",
		LicenseNumber:     "1234567890123456",
		NationalIDCountry: "RW", NationalIDNumber: "not-16-digits",
	})
	require.Error(t, err)
	appErr, ok := err.(*apperrors.AppError)
	require.True(t, ok)
	assert.Equal(t, http.StatusBadRequest, appErr.StatusCode)
	assert.Equal(t, "INVALID_NATIONAL_ID", appErr.Code)
}

// TestCreateDriverFromAdmin_NationalIDRequired_Rejected proves the DB-1 round
// 2 product decision: CreateDriverFromAdmin sets approval_status = 'APPROVED'
// directly (it never goes through ApproveDriver's defensive gate), so a
// missing national ID must be rejected here too, or admin registration would
// be a wide-open bypass of the whole mandatory-ID feature.
func TestCreateDriverFromAdmin_NationalIDRequired_Rejected(t *testing.T) {
	db := &mockDB{
		queryRowFn: func(ctx context.Context, sql string, args ...any) pgx.Row {
			t.Fatal("must not reach the DB when national_id is omitted")
			return nil
		},
		beginFn: func(ctx context.Context) (pgx.Tx, error) {
			t.Fatal("must not open a transaction when national_id is omitted")
			return nil, nil
		},
	}
	svc := newTestService(db)

	_, err := svc.CreateDriverFromAdmin(context.Background(), AdminCreateDriverInput{
		Phone: "+250700000003", FullName: "No National ID",
		TransportType: "MOTO_BIKE", VehiclePlate: "RAA000D", LicenseNumber: "1111222233334444",
	})
	require.Error(t, err)
	appErr, ok := err.(*apperrors.AppError)
	require.True(t, ok)
	assert.Equal(t, http.StatusBadRequest, appErr.StatusCode)
	assert.Equal(t, "NATIONAL_ID_REQUIRED", appErr.Code)
}

func TestCreateDriverFromAdmin_NationalIDConflict_NewUser(t *testing.T) {
	tx := &customMockTx{
		queryRowFn: func(ctx context.Context, sql string, args ...any) pgx.Row {
			switch {
			case strings.Contains(sql, "FROM users WHERE phone_number"):
				return errRow(pgx.ErrNoRows) // user does not exist yet
			case strings.Contains(sql, "INSERT INTO users"):
				return scanRow("new-user-1")
			case strings.Contains(sql, "FROM driver_profiles WHERE user_id"):
				return errRow(pgx.ErrNoRows) // no existing driver profile yet
			case strings.Contains(sql, "INSERT INTO driver_profiles"):
				return scanRow("new-profile-1")
			}
			t.Fatalf("unexpected QueryRow: %s", sql)
			return nil
		},
		execFn: func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
			if strings.Contains(sql, "SET national_id_number") {
				return pgconn.CommandTag{}, nationalIDConflictErr()
			}
			t.Fatalf("unexpected Exec: %s", sql)
			return pgconn.CommandTag{}, nil
		},
	}
	svc := newTestService(&mockDB{
		beginFn: func(ctx context.Context) (pgx.Tx, error) { return tx, nil },
	})

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
	assert.False(t, tx.committed, "a national-ID conflict must roll back the whole registration, not just skip that one write")
}

func TestCreateDriverFromAdmin_NationalIDConflict_ExistingUser(t *testing.T) {
	tx := &customMockTx{
		queryRowFn: func(ctx context.Context, sql string, args ...any) pgx.Row {
			switch {
			case strings.Contains(sql, "FROM users WHERE phone_number"):
				return scanRow("existing-user-1")
			case strings.Contains(sql, "FROM driver_profiles WHERE user_id"):
				return errRow(pgx.ErrNoRows) // no existing driver profile yet
			case strings.Contains(sql, "INSERT INTO driver_profiles"):
				return scanRow("new-profile-2")
			}
			t.Fatalf("unexpected QueryRow: %s", sql)
			return nil
		},
		execFn: func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
			switch {
			case strings.Contains(sql, "SET role_state"):
				return pgconn.CommandTag{}, nil
			case strings.Contains(sql, "SET national_id_number"):
				return pgconn.CommandTag{}, nationalIDConflictErr()
			}
			t.Fatalf("unexpected Exec: %s", sql)
			return pgconn.CommandTag{}, nil
		},
	}
	svc := newTestService(&mockDB{
		beginFn: func(ctx context.Context) (pgx.Tx, error) { return tx, nil },
	})

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
	assert.False(t, tx.committed)
}

// TestCreateDriverFromAdmin_ExistingAccountDifferentID_RejectedNotDropped
// proves the DB-1 round 2 fix for the silent-drop bug: when the phone number
// already belongs to an account that has a DIFFERENT national ID on file,
// the step-3 capture's first-write-wins WHERE clause (national_id_number IS
// NULL) would match zero rows and let the whole registration succeed anyway
// — silently discarding the admin's input while leaving the old ID in place.
// CreateDriverFromAdmin must catch this BEFORE creating any driver_profiles
// row and return a loud NATIONAL_ID_MISMATCH, not a quiet no-op.
func TestCreateDriverFromAdmin_ExistingAccountDifferentID_RejectedNotDropped(t *testing.T) {
	tx := &customMockTx{
		queryRowFn: func(ctx context.Context, sql string, args ...any) pgx.Row {
			switch {
			case strings.Contains(sql, "FROM users WHERE phone_number"):
				// Existing account already has a DIFFERENT national ID on file.
				return scanRow("existing-user-9", "1234567890123456")
			}
			t.Fatalf("unexpected QueryRow: %s", sql)
			return nil
		},
		execFn: func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
			t.Fatalf("must not write anything once the ID mismatch is detected: %s", sql)
			return pgconn.CommandTag{}, nil
		},
	}
	svc := newTestService(&mockDB{
		beginFn: func(ctx context.Context) (pgx.Tx, error) { return tx, nil },
	})

	_, err := svc.CreateDriverFromAdmin(context.Background(), AdminCreateDriverInput{
		Phone: "+250700000009", FullName: "Different ID",
		TransportType: "MOTO_BIKE", VehiclePlate: "RAA000F", LicenseNumber: "1231231231231234",
		NationalIDCountry: "RW", NationalIDNumber: "9999999999999999", // different from the one on file
	})
	require.Error(t, err)
	appErr, ok := err.(*apperrors.AppError)
	require.True(t, ok, "expected *apperrors.AppError, got %T: %v", err, err)
	assert.Equal(t, http.StatusConflict, appErr.StatusCode)
	assert.Equal(t, "NATIONAL_ID_MISMATCH", appErr.Code)
	assert.False(t, tx.committed, "must never commit — no driver_profiles row should be created for a rejected mismatch")
}

// TestCreateDriverFromAdmin_ExistingAccountSameID_Succeeds proves the mismatch
// guard does NOT fire when the supplied national ID matches what the existing
// account already has on file — re-registering with the correct, unchanged ID
// must still work exactly as before.
func TestCreateDriverFromAdmin_ExistingAccountSameID_Succeeds(t *testing.T) {
	tx := &customMockTx{
		queryRowFn: func(ctx context.Context, sql string, args ...any) pgx.Row {
			switch {
			case strings.Contains(sql, "FROM users WHERE phone_number"):
				return scanRow("existing-user-10", "1234567890123456")
			case strings.Contains(sql, "FROM driver_profiles WHERE user_id"):
				return errRow(pgx.ErrNoRows)
			case strings.Contains(sql, "INSERT INTO driver_profiles"):
				return scanRow("new-profile-10")
			}
			t.Fatalf("unexpected QueryRow: %s", sql)
			return nil
		},
		execFn: func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
			switch {
			case strings.Contains(sql, "SET role_state"):
				return pgconn.CommandTag{}, nil
			case strings.Contains(sql, "SET national_id_number"):
				return pgconn.CommandTag{}, nil // matches existing value — first-write-wins no-op, not an error
			}
			t.Fatalf("unexpected Exec: %s", sql)
			return pgconn.CommandTag{}, nil
		},
	}
	svc := newTestService(&mockDB{
		beginFn: func(ctx context.Context) (pgx.Tx, error) { return tx, nil },
	})

	_, err := svc.CreateDriverFromAdmin(context.Background(), AdminCreateDriverInput{
		Phone: "+250700000010", FullName: "Same ID",
		TransportType: "MOTO_BIKE", VehiclePlate: "RAA000G", LicenseNumber: "4324324324324321",
		NationalIDCountry: "RW", NationalIDNumber: "1234567890123456", // same as on file
	})
	require.NoError(t, err)
	assert.True(t, tx.committed)
}

// TestCreateDriverFromAdmin_ProfileInsertFails_NationalIDNotBound proves the
// DB-1 round 2 atomicity fix: user-create/promote + national-ID capture +
// the driver_profiles insert are ONE transaction. A driver_profiles insert
// failure (a duplicate plate here) must roll back EVERYTHING, including the
// national-ID capture — otherwise a phantom user could permanently own a
// real person's ID with no driver record to show for it.
func TestCreateDriverFromAdmin_ProfileInsertFails_NationalIDNotBound(t *testing.T) {
	tx := &customMockTx{
		queryRowFn: func(ctx context.Context, sql string, args ...any) pgx.Row {
			switch {
			case strings.Contains(sql, "FROM users WHERE phone_number"):
				return errRow(pgx.ErrNoRows)
			case strings.Contains(sql, "INSERT INTO users"):
				return scanRow("new-user-2")
			case strings.Contains(sql, "FROM driver_profiles WHERE user_id"):
				return errRow(pgx.ErrNoRows)
			case strings.Contains(sql, "INSERT INTO driver_profiles"):
				// Message mirrors what a real Postgres 23505 looks like — the
				// constraint name lives in the message text, not a separate
				// field mapAdminCreateDriverError inspects (it substring-
				// matches err.Error(), which is Severity + Message + SQLSTATE).
				return errRow(&pgconn.PgError{
					Code:           "23505",
					Message:        `duplicate key value violates unique constraint "driver_profiles_vehicle_plate_key"`,
					ConstraintName: "driver_profiles_vehicle_plate_key",
				})
			}
			t.Fatalf("unexpected QueryRow: %s", sql)
			return nil
		},
		execFn: func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
			t.Fatal("must not reach the national-ID capture Exec once the profile insert already failed")
			return pgconn.CommandTag{}, nil
		},
	}
	svc := newTestService(&mockDB{
		beginFn: func(ctx context.Context) (pgx.Tx, error) { return tx, nil },
	})

	_, err := svc.CreateDriverFromAdmin(context.Background(), AdminCreateDriverInput{
		Phone: "+250700000004", FullName: "Plate Collision",
		TransportType: "MOTO_BIKE", VehiclePlate: "RAA000E", LicenseNumber: "9999888877776666",
		NationalIDCountry: "RW", NationalIDNumber: "5555555555555555",
	})
	require.Error(t, err)
	appErr, ok := err.(*apperrors.AppError)
	require.True(t, ok, "expected *apperrors.AppError, got %T: %v", err, err)
	assert.Equal(t, "PLATE_ALREADY_EXISTS", appErr.Code)
	assert.False(t, tx.committed, "the transaction must never commit when the profile insert fails")
}
