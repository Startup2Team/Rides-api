package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	goredis "github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/workspace/ride-platform/config"
	"github.com/workspace/ride-platform/internal/telephony"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// ── pgx mock primitives (mirrors internal/admin/service_test.go) ──────────

// closureRow implements pgx.Row using a caller-supplied Scan function.
type closureRow struct {
	scanFn func(dest ...any) error
}

func (r *closureRow) Scan(dest ...any) error { return r.scanFn(dest...) }

func errRow(err error) pgx.Row {
	return &closureRow{scanFn: func(...any) error { return err }}
}

// scanRow fills dest positionally from values, skipping any nil value (the
// destination is left at its zero value, exactly like a NULL column would).
// Supports the destination types Repository's Scan calls actually use.
func scanRow(values ...any) pgx.Row {
	return &closureRow{scanFn: func(dest ...any) error {
		for i, d := range dest {
			if i >= len(values) || values[i] == nil {
				continue
			}
			switch p := d.(type) {
			case *string:
				if v, ok := values[i].(string); ok {
					*p = v
				}
			case **string:
				if v, ok := values[i].(string); ok {
					*p = &v
				}
			case *bool:
				if v, ok := values[i].(bool); ok {
					*p = v
				}
			case *int:
				if v, ok := values[i].(int); ok {
					*p = v
				}
			case *time.Time:
				if v, ok := values[i].(time.Time); ok {
					*p = v
				}
			case **time.Time:
				if v, ok := values[i].(time.Time); ok {
					*p = &v
				}
			}
		}
		return nil
	}}
}

// mockDB implements DBTX with per-call function fields for fine-grained
// control — mirrors internal/admin/service_test.go's mockDB.
type mockDB struct {
	queryRowFn func(ctx context.Context, sql string, args ...any) pgx.Row
	execFn     func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	beginFn    func(ctx context.Context) (pgx.Tx, error)
}

func (m *mockDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if m.queryRowFn != nil {
		return m.queryRowFn(ctx, sql, args...)
	}
	return errRow(pgx.ErrNoRows)
}

func (m *mockDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if m.execFn != nil {
		return m.execFn(ctx, sql, args...)
	}
	return pgconn.CommandTag{}, nil
}

func (m *mockDB) Begin(ctx context.Context) (pgx.Tx, error) {
	if m.beginFn != nil {
		return m.beginFn(ctx)
	}
	return nil, nil // unused by most paths under test
}

// userRow builds the pgx.Row FindUserByPhone/FindUserByID scan expects:
// id, phone_number, full_name, email, role_state, device_id, fcm_token,
// is_suspended, suspension_until, created_at, updated_at.
func userRow(id, phone string) pgx.Row {
	now := time.Now()
	return scanRow(id, phone, nil, nil, "CUSTOMER_ONLY", nil, nil, false, nil, now, now)
}

// otpRow builds the pgx.Row FindLatestOTP scan expects, hashing code so
// bcrypt.CompareHashAndPassword in VerifyOTP succeeds against it.
func otpRow(t *testing.T, code string) pgx.Row {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(code), bcrypt.DefaultCost)
	require.NoError(t, err)
	return scanRow("otp-1", "+250780000000", string(hash), PurposeRegistration, false,
		time.Now().Add(10*time.Minute), time.Now())
}

// newMiniredis starts an in-memory Redis the Service can use directly — no
// real Redis server needed for these tests.
func newMiniredis(t *testing.T) goredis.UniversalClient {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

// ── InitiateOTP: duplicate-phone guard (register-duplicate-phone-and-gender) ─
//
// A phone that already has an account must never reach the OTP send / store
// path on /auth/register — it costs real Pindo money and (before this guard)
// left VerifyOTP silently discarding the whole registration payload on its
// existing-user branch.

// Table-driven across both OTP modes: the guard sits ABOVE the
// `s.cfg.OTPMode == "pindo_verify"` branch in InitiateOTP specifically so it
// can't be bypassed in pindo mode (staging runs self_sms today, but that's
// config, not code — this pins the ordering against a future refactor or a
// staging/prod config flip).
func TestInitiateOTP_ExistingPhone_Register_ReturnsConflictWithoutSideEffects(t *testing.T) {
	tests := []struct {
		name    string
		otpMode string
	}{
		{"self_sms mode", ""},
		{"pindo_verify mode", "pindo_verify"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			execCalled := false
			repo := &Repository{db: &mockDB{
				queryRowFn: func(ctx context.Context, sql string, args ...any) pgx.Row {
					if strings.Contains(sql, "FROM users WHERE phone_number") {
						return userRow("existing-user-1", "+250780000001")
					}
					t.Fatalf("unexpected QueryRow: %s", sql)
					return nil
				},
				execFn: func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
					execCalled = true
					return pgconn.CommandTag{}, nil
				},
			}}

			svc := &Service{
				repo:  repo,
				redis: newMiniredis(t),
				// telephony left nil deliberately: if the guard didn't stop the
				// call before it reaches SendOTP/StartVerify, dereferencing a nil
				// *Service panics — a hard, unambiguous signal that no SMS/Verify
				// attempt was made, in EITHER mode.
				telephony: nil,
				cfg:       &config.Config{OTPMode: tc.otpMode},
				log:       zerolog.Nop(),
			}

			devOTP, err := svc.InitiateOTP(context.Background(), "+250780000001", PurposeRegistration, "device-1", "android", "Existing User", nil, nil)

			require.Error(t, err)
			appErr, ok := err.(*apperrors.AppError)
			require.True(t, ok, "expected *apperrors.AppError, got %T", err)
			assert.Equal(t, 409, appErr.StatusCode)
			assert.Equal(t, "PHONE_ALREADY_REGISTERED", appErr.Code)
			assert.Empty(t, devOTP)
			assert.False(t, execCalled, "CreateOTP must never be called for an already-registered phone")
		})
	}
}

func TestInitiateOTP_NewPhone_Register_SendsNormally(t *testing.T) {
	otpInserted := false
	repo := &Repository{db: &mockDB{
		queryRowFn: func(ctx context.Context, sql string, args ...any) pgx.Row {
			if strings.Contains(sql, "FROM users WHERE phone_number") {
				return errRow(pgx.ErrNoRows) // phone is free
			}
			t.Fatalf("unexpected QueryRow: %s", sql)
			return nil
		},
		execFn: func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
			if strings.Contains(sql, "INSERT INTO otp_verifications") {
				otpInserted = true
			}
			return pgconn.CommandTag{}, nil
		},
	}}

	rdb := newMiniredis(t)
	cfg := &config.Config{Env: "test"}
	svc := &Service{
		repo:  repo,
		redis: rdb,
		// Empty Pindo token + non-production Env => sendPindoSMS logs and
		// returns nil without making any network call (see telephony.Service).
		telephony: telephony.New(cfg, zerolog.Nop()),
		cfg:       cfg,
		log:       zerolog.Nop(),
	}

	gender := "female"
	devOTP, err := svc.InitiateOTP(context.Background(), "+250780000002", PurposeRegistration, "device-2", "android", "New User", nil, &gender)

	require.NoError(t, err)
	assert.NotEmpty(t, devOTP, "non-production must echo the OTP back")
	assert.True(t, otpInserted, "a new registration must still create an otp_verifications row")

	// gender was stashed alongside full_name for VerifyOTP to consume.
	v, err := rdb.HGet(context.Background(), "otp:meta:+250780000002", "gender").Result()
	require.NoError(t, err)
	assert.Equal(t, "female", v)
}

// A resend that intentionally omits a field previously supplied (e.g. the
// user picked "female", went back, deselected it, then hit Continue again)
// must NOT leave the stale value behind in the otp:meta stash — InitiateOTP
// Del's the whole hash before re-writing it per attempt.
func TestInitiateOTP_Register_ResendDoesNotLeakStaleGender(t *testing.T) {
	repo := &Repository{db: &mockDB{
		queryRowFn: func(ctx context.Context, sql string, args ...any) pgx.Row {
			if strings.Contains(sql, "FROM users WHERE phone_number") {
				return errRow(pgx.ErrNoRows)
			}
			t.Fatalf("unexpected QueryRow: %s", sql)
			return nil
		},
	}}

	rdb := newMiniredis(t)
	cfg := &config.Config{Env: "test"}
	svc := &Service{
		repo:      repo,
		redis:     rdb,
		telephony: telephony.New(cfg, zerolog.Nop()),
		cfg:       cfg,
		log:       zerolog.Nop(),
	}
	ctx := context.Background()
	phone := "+250780000006"

	// Attempt 1: gender supplied.
	gender := "female"
	_, err := svc.InitiateOTP(ctx, phone, PurposeRegistration, "device-6", "android", "Resend Tester", nil, &gender)
	require.NoError(t, err)
	v, err := rdb.HGet(ctx, "otp:meta:"+phone, "gender").Result()
	require.NoError(t, err)
	assert.Equal(t, "female", v)

	// Attempt 2 (resend after deselecting): gender omitted.
	_, err = svc.InitiateOTP(ctx, phone, PurposeRegistration, "device-6", "android", "Resend Tester", nil, nil)
	require.NoError(t, err)

	_, err = rdb.HGet(ctx, "otp:meta:"+phone, "gender").Result()
	assert.ErrorIs(t, err, goredis.Nil, "a field omitted on this attempt must not survive from a previous attempt")

	// full_name from the second attempt must still be present — only the
	// omitted field should be gone, not the whole stash.
	name, err := rdb.HGet(ctx, "otp:meta:"+phone, "full_name").Result()
	require.NoError(t, err)
	assert.Equal(t, "Resend Tester", name)
}

// Gender must stash even when full_name is empty — the previous gate
// (`purpose == PurposeRegistration && fullName != ""`) silently dropped
// gender (and email) whenever a caller sent no name. Not reachable from the
// shipped mobile client (register.tsx requires a name) but a live trap for
// any other caller (e.g. the admin driver-verify path evolving to pass one).
func TestInitiateOTP_Register_GenderStashedEvenWithoutFullName(t *testing.T) {
	repo := &Repository{db: &mockDB{
		queryRowFn: func(ctx context.Context, sql string, args ...any) pgx.Row {
			if strings.Contains(sql, "FROM users WHERE phone_number") {
				return errRow(pgx.ErrNoRows)
			}
			t.Fatalf("unexpected QueryRow: %s", sql)
			return nil
		},
	}}

	rdb := newMiniredis(t)
	cfg := &config.Config{Env: "test"}
	svc := &Service{
		repo:      repo,
		redis:     rdb,
		telephony: telephony.New(cfg, zerolog.Nop()),
		cfg:       cfg,
		log:       zerolog.Nop(),
	}

	gender := "other"
	_, err := svc.InitiateOTP(context.Background(), "+250780000007", PurposeRegistration, "device-7", "android", "" /* no name */, nil, &gender)
	require.NoError(t, err)

	v, err := rdb.HGet(context.Background(), "otp:meta:+250780000007", "gender").Result()
	require.NoError(t, err)
	assert.Equal(t, "other", v)
}

// ── CreateUser: gender normalization ───────────────────────────────────────

func TestCreateUser_GenderRoundTrip(t *testing.T) {
	var insertArgs []any
	repo := &Repository{db: &mockDB{
		queryRowFn: func(ctx context.Context, sql string, args ...any) pgx.Row {
			if strings.Contains(sql, "INSERT INTO users") {
				insertArgs = args
				return scanRow("new-user-1", "+250780000003", nil, nil, "CUSTOMER_ONLY", nil, nil, false, nil, time.Now(), time.Now())
			}
			t.Fatalf("unexpected QueryRow: %s", sql)
			return nil
		},
	}}

	gender := "male"
	_, err := repo.CreateUser(context.Background(), "+250780000003", "device-3", "android", nil, nil, &gender)
	require.NoError(t, err)
	require.NotEmpty(t, insertArgs)

	// gender is the 5th INSERT arg (phone, device_id, full_name, email, gender).
	genderArg, ok := insertArgs[4].(*string)
	require.True(t, ok, "gender must reach the INSERT as *string, got %T", insertArgs[4])
	require.NotNil(t, genderArg)
	assert.Equal(t, "male", *genderArg)
}

func TestCreateUser_EmptyOrUnrecognisedGender_NormalizedToNil(t *testing.T) {
	tests := []struct {
		name   string
		gender *string
	}{
		{"nil (omitted)", nil},
		{"empty string", strPtr("")},
		{"unrecognised value", strPtr("not-a-real-gender")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var insertArgs []any
			repo := &Repository{db: &mockDB{
				queryRowFn: func(ctx context.Context, sql string, args ...any) pgx.Row {
					if strings.Contains(sql, "INSERT INTO users") {
						insertArgs = args
						return scanRow("new-user-x", "+250780000004", nil, nil, "CUSTOMER_ONLY", nil, nil, false, nil, time.Now(), time.Now())
					}
					t.Fatalf("unexpected QueryRow: %s", sql)
					return nil
				},
			}}

			_, err := repo.CreateUser(context.Background(), "+250780000004", "device-4", "android", nil, nil, tc.gender)
			require.NoError(t, err)
			require.NotEmpty(t, insertArgs)
			assert.Nil(t, insertArgs[4], "gender must be written as NULL (nil), never \"\" or an unrecognised value")
		})
	}
}

func strPtr(s string) *string { return &s }

// ── VerifyOTP: existing-user branch clears the registration stash ─────────

func TestVerifyOTP_ExistingUser_DeletesOTPMetaKey(t *testing.T) {
	phone := "+250780000005"
	repo := &Repository{db: &mockDB{
		queryRowFn: func(ctx context.Context, sql string, args ...any) pgx.Row {
			switch {
			case strings.Contains(sql, "FROM otp_verifications"):
				return otpRow(t, "123456")
			case strings.Contains(sql, "FROM users WHERE phone_number"):
				return userRow("existing-user-5", phone)
			case strings.Contains(sql, "SELECT u.role_state, dp.approval_status"):
				return scanRow("CUSTOMER_ONLY", nil)
			case strings.Contains(sql, "COUNT(DISTINCT user_id)"):
				return scanRow(0)
			}
			t.Fatalf("unexpected QueryRow: %s", sql)
			return nil
		},
		execFn: func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
			return pgconn.CommandTag{}, nil
		},
	}}

	rdb := newMiniredis(t)
	ctx := context.Background()
	metaKey := "otp:meta:" + phone
	require.NoError(t, rdb.HSet(ctx, metaKey, "full_name", "Stale Stash").Err())

	svc := &Service{
		repo:  repo,
		redis: rdb,
		cfg:   &config.Config{},
		log:   zerolog.Nop(),
	}

	_, user, err := svc.VerifyOTP(ctx, phone, "123456", PurposeRegistration, "device-5", "android", "1.0", "127.0.0.1")
	require.NoError(t, err)
	require.NotNil(t, user)
	assert.Equal(t, "existing-user-5", user.ID)

	exists, err := rdb.Exists(ctx, metaKey).Result()
	require.NoError(t, err)
	assert.Zero(t, exists, "otp:meta stash must be deleted on the existing-user branch, not leaked until TTL")
}
