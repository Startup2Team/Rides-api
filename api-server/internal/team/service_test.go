package team

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/pquerna/otp/totp"
	goredis "github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/workspace/ride-platform/config"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// ── Mock repo ─────────────────────────────────────────────────────────────

type mockRepo struct {
	findByEmailFn       func(ctx context.Context, email string) (*AdminAccount, *string, error)
	findByIDFn          func(ctx context.Context, id string) (*AdminAccount, *string, error)
	touchLastActiveFn   func(ctx context.Context, id string)
	listAdminsFn        func(ctx context.Context, status, roleID, search string) ([]*AdminAccount, error)
	inviteFn            func(ctx context.Context, name, email, roleID string) (*AdminAccount, error)
	updateRoleFn        func(ctx context.Context, id, roleID string) error
	updateStatusFn      func(ctx context.Context, id, status string) error
	deleteFn            func(ctx context.Context, id string) error
	updateNameFn        func(ctx context.Context, id, name string) error
	setPasswordFn       func(ctx context.Context, id, hash string) error
	getTOTPSecretFn     func(ctx context.Context, id string) (*string, error)
	saveTOTPFn          func(ctx context.Context, id, secret string) error
	clearTOTPFn         func(ctx context.Context, id string) error
	getBackupCodesFn    func(ctx context.Context, id string) ([]BackupCode, error)
	saveBackupCodesFn   func(ctx context.Context, id string, codes []BackupCode) error
	listRolesFn         func(ctx context.Context) ([]*Role, error)
	createRoleFn        func(ctx context.Context, name, description string, permissions interface{}) (*Role, error)
	updateRoleByIDFn    func(ctx context.Context, roleID, name, description string, permissions interface{}) (*Role, error)
	deleteRoleByIDFn    func(ctx context.Context, roleID string) error
	logActionFn         func(ctx context.Context, adminID, action, targetType, targetID, detail, ip string) error
	getMemberActivityFn func(ctx context.Context, adminID string, limit int) ([]AuditEntry, error)
	listAuditLogFn      func(ctx context.Context, actor, action, targetType, from, to string, limit, offset int) ([]AuditEntry, int, error)
}

func (m *mockRepo) FindByEmail(ctx context.Context, email string) (*AdminAccount, *string, error) {
	if m.findByEmailFn != nil {
		return m.findByEmailFn(ctx, email)
	}
	return nil, nil, apperrors.ErrNotFound
}
func (m *mockRepo) FindByID(ctx context.Context, id string) (*AdminAccount, *string, error) {
	if m.findByIDFn != nil {
		return m.findByIDFn(ctx, id)
	}
	return nil, nil, apperrors.ErrNotFound
}
func (m *mockRepo) TouchLastActive(ctx context.Context, id string) {
	if m.touchLastActiveFn != nil {
		m.touchLastActiveFn(ctx, id)
	}
}
func (m *mockRepo) ListAdmins(ctx context.Context, status, roleID, search string) ([]*AdminAccount, error) {
	if m.listAdminsFn != nil {
		return m.listAdminsFn(ctx, status, roleID, search)
	}
	return nil, nil
}
func (m *mockRepo) Invite(ctx context.Context, name, email, roleID string) (*AdminAccount, error) {
	if m.inviteFn != nil {
		return m.inviteFn(ctx, name, email, roleID)
	}
	return nil, nil
}
func (m *mockRepo) UpdateRole(ctx context.Context, id, roleID string) error {
	if m.updateRoleFn != nil {
		return m.updateRoleFn(ctx, id, roleID)
	}
	return nil
}
func (m *mockRepo) UpdateStatus(ctx context.Context, id, status string) error {
	if m.updateStatusFn != nil {
		return m.updateStatusFn(ctx, id, status)
	}
	return nil
}
func (m *mockRepo) Delete(ctx context.Context, id string) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, id)
	}
	return nil
}
func (m *mockRepo) TouchInvitedAt(ctx context.Context, id string) error         { return nil }
func (m *mockRepo) ReissueInvite(ctx context.Context, id string) (int64, error) { return 1, nil }
func (m *mockRepo) UpdateRolePermissions(ctx context.Context, roleID string, permissions interface{}) (*Role, error) {
	return &Role{ID: roleID, Permissions: permissions}, nil
}
func (m *mockRepo) UpdateName(ctx context.Context, id, name string) error {
	if m.updateNameFn != nil {
		return m.updateNameFn(ctx, id, name)
	}
	return nil
}
func (m *mockRepo) UpdateProfile(ctx context.Context, id, name, phone, photoURL string) error {
	return nil
}
func (m *mockRepo) SetPassword(ctx context.Context, id, hash string) error {
	if m.setPasswordFn != nil {
		return m.setPasswordFn(ctx, id, hash)
	}
	return nil
}
func (m *mockRepo) GetTOTPSecret(ctx context.Context, id string) (*string, error) {
	if m.getTOTPSecretFn != nil {
		return m.getTOTPSecretFn(ctx, id)
	}
	return nil, nil
}
func (m *mockRepo) SaveTOTP(ctx context.Context, id, secret string) error {
	if m.saveTOTPFn != nil {
		return m.saveTOTPFn(ctx, id, secret)
	}
	return nil
}
func (m *mockRepo) ClearTOTP(ctx context.Context, id string) error {
	if m.clearTOTPFn != nil {
		return m.clearTOTPFn(ctx, id)
	}
	return nil
}
func (m *mockRepo) GetBackupCodes(ctx context.Context, id string) ([]BackupCode, error) {
	if m.getBackupCodesFn != nil {
		return m.getBackupCodesFn(ctx, id)
	}
	return nil, nil
}
func (m *mockRepo) SaveBackupCodes(ctx context.Context, id string, codes []BackupCode) error {
	if m.saveBackupCodesFn != nil {
		return m.saveBackupCodesFn(ctx, id, codes)
	}
	return nil
}
func (m *mockRepo) ListRoles(ctx context.Context) ([]*Role, error) {
	if m.listRolesFn != nil {
		return m.listRolesFn(ctx)
	}
	return nil, nil
}
func (m *mockRepo) CreateRole(ctx context.Context, name, description string, permissions interface{}) (*Role, error) {
	if m.createRoleFn != nil {
		return m.createRoleFn(ctx, name, description, permissions)
	}
	return nil, nil
}
func (m *mockRepo) UpdateRoleByID(ctx context.Context, roleID, name, description string, permissions interface{}) (*Role, error) {
	if m.updateRoleByIDFn != nil {
		return m.updateRoleByIDFn(ctx, roleID, name, description, permissions)
	}
	return nil, nil
}
func (m *mockRepo) DeleteRoleByID(ctx context.Context, roleID string) error {
	if m.deleteRoleByIDFn != nil {
		return m.deleteRoleByIDFn(ctx, roleID)
	}
	return nil
}
func (m *mockRepo) LogAction(ctx context.Context, adminID, action, targetType, targetID, detail, ip string) error {
	if m.logActionFn != nil {
		return m.logActionFn(ctx, adminID, action, targetType, targetID, detail, ip)
	}
	return nil
}
func (m *mockRepo) GetMemberActivity(ctx context.Context, adminID string, limit int) ([]AuditEntry, error) {
	if m.getMemberActivityFn != nil {
		return m.getMemberActivityFn(ctx, adminID, limit)
	}
	return nil, nil
}
func (m *mockRepo) ListAuditLog(ctx context.Context, actor, action, targetType, from, to string, limit, offset int) ([]AuditEntry, int, error) {
	if m.listAuditLogFn != nil {
		return m.listAuditLogFn(ctx, actor, action, targetType, from, to, limit, offset)
	}
	return nil, 0, nil
}

// ── Test helpers ──────────────────────────────────────────────────────────

func newTestRedis(t *testing.T) goredis.UniversalClient {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	return rdb
}

func testCfg() *config.Config {
	return &config.Config{
		JWT: config.JWTConfig{
			AccessSecret:        "test-access-secret-64-chars-long-enough-for-hmac-signing-ok",
			RefreshSecret:       "test-refresh-secret-64-chars-long-enough-for-hmac-signing-ok",
			AccessExpiryMinutes: 15,
			RefreshExpiryDays:   30,
			AccessExpiry:        15 * time.Minute,
			RefreshExpiry:       30 * 24 * time.Hour,
			AdminPreAuthMinutes: 30,
			AdminPreAuthExpiry:  30 * time.Minute,
		},
	}
}

func newTestService(repo TeamRepo, rdb goredis.UniversalClient) *Service {
	return &Service{repo: repo, cfg: testCfg(), rdb: rdb}
}

func newTestServiceProduction(repo TeamRepo, rdb goredis.UniversalClient) *Service {
	cfg := testCfg()
	cfg.Env = "production"
	return &Service{repo: repo, cfg: cfg, rdb: rdb}
}

// ── Simple delegation methods ─────────────────────────────────────────────

func TestListAdmins_Delegates(t *testing.T) {
	repo := &mockRepo{
		listAdminsFn: func(_ context.Context, _, _, _ string) ([]*AdminAccount, error) {
			return []*AdminAccount{{ID: "a1", Email: "admin@test.com"}}, nil
		},
	}
	svc := newTestService(repo, newTestRedis(t))
	admins, err := svc.ListAdmins(context.Background(), "", "", "")
	require.NoError(t, err)
	assert.Len(t, admins, 1)
}

func TestInvite_Delegates(t *testing.T) {
	repo := &mockRepo{
		inviteFn: func(_ context.Context, name, email, roleID string) (*AdminAccount, error) {
			_ = name
			_ = email
			_ = roleID
			return &AdminAccount{ID: "new", Email: email}, nil
		},
	}
	svc := newTestService(repo, newTestRedis(t))
	a, err := svc.Invite(context.Background(), "Test", "t@test.com", "role-id", "")
	require.NoError(t, err)
	assert.Equal(t, "t@test.com", a.Email)
}

func TestSuspend_SetsStatusSuspended(t *testing.T) {
	var gotStatus string
	repo := &mockRepo{
		updateStatusFn: func(_ context.Context, _, status string) error {
			gotStatus = status
			return nil
		},
	}
	svc := newTestService(repo, newTestRedis(t))
	require.NoError(t, svc.Suspend(context.Background(), "member-id"))
	assert.Equal(t, "SUSPENDED", gotStatus)
}

func TestReinstate_SetsStatusActive(t *testing.T) {
	var gotStatus string
	repo := &mockRepo{
		updateStatusFn: func(_ context.Context, _, status string) error {
			gotStatus = status
			return nil
		},
	}
	svc := newTestService(repo, newTestRedis(t))
	require.NoError(t, svc.Reinstate(context.Background(), "member-id"))
	assert.Equal(t, "ACTIVE", gotStatus)
}

func TestRemove_Delegates(t *testing.T) {
	called := false
	repo := &mockRepo{
		deleteFn: func(_ context.Context, _ string) error {
			called = true
			return nil
		},
	}
	svc := newTestService(repo, newTestRedis(t))
	require.NoError(t, svc.Remove(context.Background(), "member-id"))
	assert.True(t, called)
}

func TestUpdateRole_Delegates(t *testing.T) {
	repo := &mockRepo{
		updateRoleFn: func(_ context.Context, _, _ string) error { return nil },
	}
	svc := newTestService(repo, newTestRedis(t))
	assert.NoError(t, svc.UpdateRole(context.Background(), "member-id", "new-role"))
}

func TestUpdateName_Delegates(t *testing.T) {
	repo := &mockRepo{
		updateNameFn: func(_ context.Context, _, _ string) error { return nil },
	}
	svc := newTestService(repo, newTestRedis(t))
	assert.NoError(t, svc.UpdateName(context.Background(), "member-id", "New Name"))
}

func TestListRoles_Delegates(t *testing.T) {
	desc := "test"
	repo := &mockRepo{
		listRolesFn: func(_ context.Context) ([]*Role, error) {
			return []*Role{{ID: "r1", Name: "Admin", Description: &desc}}, nil
		},
	}
	svc := newTestService(repo, newTestRedis(t))
	roles, err := svc.ListRoles(context.Background())
	require.NoError(t, err)
	assert.Len(t, roles, 1)
}

func TestCreateRole_Delegates(t *testing.T) {
	repo := &mockRepo{
		createRoleFn: func(_ context.Context, name, _ string, _ interface{}) (*Role, error) {
			return &Role{ID: "new", Name: name}, nil
		},
	}
	svc := newTestService(repo, newTestRedis(t))
	role, err := svc.CreateRole(context.Background(), "Finance", "", nil)
	require.NoError(t, err)
	assert.Equal(t, "Finance", role.Name)
}

func TestDeleteRoleByID_Delegates(t *testing.T) {
	repo := &mockRepo{
		deleteRoleByIDFn: func(_ context.Context, _ string) error { return nil },
	}
	svc := newTestService(repo, newTestRedis(t))
	assert.NoError(t, svc.DeleteRoleByID(context.Background(), "role-id"))
}

func TestDeleteRoleByID_SystemRole(t *testing.T) {
	repo := &mockRepo{
		deleteRoleByIDFn: func(_ context.Context, _ string) error {
			return errors.New("cannot_delete_system_role")
		},
	}
	svc := newTestService(repo, newTestRedis(t))
	err := svc.DeleteRoleByID(context.Background(), "system-role")
	require.Error(t, err)
}

// ── SetPassword ───────────────────────────────────────────────────────────

func TestSetPassword_HashesAndStores(t *testing.T) {
	var storedHash string
	repo := &mockRepo{
		setPasswordFn: func(_ context.Context, _, hash string) error {
			storedHash = hash
			return nil
		},
	}
	svc := newTestService(repo, newTestRedis(t))
	require.NoError(t, svc.SetPassword(context.Background(), "member-id", "newpassword123"))
	// Verify it's a bcrypt hash, not plaintext
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(storedHash), []byte("newpassword123")))
}

// ── ChangePassword ────────────────────────────────────────────────────────

func TestChangePassword_WrongCurrent(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct"), bcrypt.MinCost)
	hashStr := string(hash)
	repo := &mockRepo{
		findByIDFn: func(_ context.Context, _ string) (*AdminAccount, *string, error) {
			return &AdminAccount{ID: "member-id"}, &hashStr, nil
		},
	}
	svc := newTestService(repo, newTestRedis(t))
	err := svc.ChangePassword(context.Background(), "member-id", "wrong", "newpassword123")
	require.Error(t, err)
	var appErr *apperrors.AppError
	assert.True(t, errors.As(err, &appErr))
	assert.Equal(t, "INVALID_CREDENTIALS", appErr.Code)
}

func TestChangePassword_Success(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct"), bcrypt.MinCost)
	hashStr := string(hash)
	repo := &mockRepo{
		findByIDFn: func(_ context.Context, _ string) (*AdminAccount, *string, error) {
			return &AdminAccount{ID: "member-id"}, &hashStr, nil
		},
		setPasswordFn: func(_ context.Context, _, _ string) error { return nil },
	}
	svc := newTestService(repo, newTestRedis(t))
	assert.NoError(t, svc.ChangePassword(context.Background(), "member-id", "correct", "newpassword123"))
}

// ── Login ─────────────────────────────────────────────────────────────────

func TestLogin_UnknownEmail(t *testing.T) {
	repo := &mockRepo{
		findByEmailFn: func(_ context.Context, _ string) (*AdminAccount, *string, error) {
			return nil, nil, apperrors.ErrNotFound
		},
	}
	svc := newTestService(repo, newTestRedis(t))
	_, err := svc.Login(context.Background(), "nobody@test.com", "pass")
	require.Error(t, err)
}

func TestLogin_WrongPassword(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct"), bcrypt.MinCost)
	hashStr := string(hash)
	repo := &mockRepo{
		findByEmailFn: func(_ context.Context, _ string) (*AdminAccount, *string, error) {
			return &AdminAccount{ID: "a1", Status: "ACTIVE"}, &hashStr, nil
		},
	}
	svc := newTestService(repo, newTestRedis(t))
	_, err := svc.Login(context.Background(), "admin@test.com", "wrong")
	require.Error(t, err)
}

// A 2FA-off admin must NOT receive an access token. This test previously
// asserted the opposite, which is what made 2FA a UI convention: the login
// response carried a fully-privileged token and only the console's own wall
// stood between it and the dashboard. Login now hands back a setup token, whose
// single power is to complete enrolment.
func TestLogin_No2FA_ReturnsSetupTokenNotAccess(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	hashStr := string(hash)
	repo := &mockRepo{
		findByEmailFn: func(_ context.Context, _ string) (*AdminAccount, *string, error) {
			return &AdminAccount{ID: "a1", Status: "ACTIVE", TwoFactor: false}, &hashStr, nil
		},
		getTOTPSecretFn: func(_ context.Context, _ string) (*string, error) {
			return nil, nil
		},
	}
	svc := newTestService(repo, newTestRedis(t))

	result, err := svc.Login(context.Background(), "admin@test.com", "secret")

	require.NoError(t, err)
	assert.Empty(t, result.AccessToken, "login must never issue an access token before 2FA is enrolled")
	assert.NotEmpty(t, result.SetupToken)
	assert.True(t, result.TwoFactorSetupRequired)
	assert.False(t, result.TwoFactorRequired)
}

// The setup token must be useless as a session credential. The admin middleware
// admits only token_type "access"; this asserts the claim it inspects, so a
// future change to issueSetupToken cannot quietly widen its reach.
func TestSetupToken_IsNotAnAccessToken(t *testing.T) {
	svc := newTestService(&mockRepo{}, newTestRedis(t))

	tok, err := svc.issueSetupToken("a1")
	require.NoError(t, err)

	parsed, _, err := jwt.NewParser().ParseUnverified(tok, jwt.MapClaims{})
	require.NoError(t, err)
	claims, ok := parsed.Claims.(jwt.MapClaims)
	require.True(t, ok)

	assert.Equal(t, "totp_setup", claims["token_type"])
	assert.NotEqual(t, "access", claims["token_type"])
	assert.Nil(t, claims["jti"], "a setup token is not a session and must carry no jti")

	// It must also be rejected where a pre-auth token is expected.
	_, preAuthErr := svc.validatePreAuthToken(tok)
	require.Error(t, preAuthErr)
}

// The whole point of the exchange: a valid code turns a setup token into a real
// session, and an invalid one turns it into nothing.
func TestEnrollTOTPComplete_ValidCodeIssuesAccessToken(t *testing.T) {
	const secret = "JBSWY3DPEHPK3PXP"
	repo := &mockRepo{
		findByIDFn: func(_ context.Context, _ string) (*AdminAccount, *string, error) {
			return &AdminAccount{ID: "a1", Email: "a@test.com", RoleName: "Super Admin", TwoFactor: false}, nil, nil
		},
		saveTOTPFn:        func(_ context.Context, _, _ string) error { return nil },
		saveBackupCodesFn: func(_ context.Context, _ string, _ []BackupCode) error { return nil },
	}
	svc := newTestService(repo, newTestRedis(t))
	setupTok, err := svc.issueSetupToken("a1")
	require.NoError(t, err)

	code, err := totp.GenerateCode(secret, time.Now().UTC())
	require.NoError(t, err)

	result, backupCodes, err := svc.EnrollTOTPComplete(context.Background(), setupTok, secret, code)

	require.NoError(t, err)
	assert.NotEmpty(t, result.AccessToken, "a verified enrolment is where the session begins")
	assert.Len(t, backupCodes, backupCodeCount)
}

func TestEnrollTOTPComplete_InvalidCodeIssuesNothing(t *testing.T) {
	stored := false
	repo := &mockRepo{
		findByIDFn: func(_ context.Context, _ string) (*AdminAccount, *string, error) {
			return &AdminAccount{ID: "a1", Email: "a@test.com", RoleName: "Super Admin"}, nil, nil
		},
		saveTOTPFn: func(_ context.Context, _, _ string) error {
			stored = true
			return nil
		},
	}
	svc := newTestService(repo, newTestRedis(t))
	setupTok, err := svc.issueSetupToken("a1")
	require.NoError(t, err)

	result, backupCodes, err := svc.EnrollTOTPComplete(context.Background(), setupTok, "JBSWY3DPEHPK3PXP", "000000")

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Nil(t, backupCodes)
	assert.False(t, stored, "a failed enrolment must not store a secret")
}

// An already-enrolled admin must not be able to re-enrol through the login path,
// which would replace a working authenticator without proving they hold it.
func TestEnrollTOTPBegin_RefusesWhenAlreadyEnrolled(t *testing.T) {
	repo := &mockRepo{
		findByIDFn: func(_ context.Context, _ string) (*AdminAccount, *string, error) {
			return &AdminAccount{ID: "a1", Email: "a@test.com", TwoFactor: true}, nil, nil
		},
	}
	svc := newTestService(repo, newTestRedis(t))
	setupTok, err := svc.issueSetupToken("a1")
	require.NoError(t, err)

	_, err = svc.EnrollTOTPBegin(context.Background(), setupTok)
	require.Error(t, err)
}

// A setup token may not be replayed as a pre-auth token, and vice versa.
func TestSetupToken_AndPreAuthToken_AreNotInterchangeable(t *testing.T) {
	svc := newTestService(&mockRepo{}, newTestRedis(t))

	setupTok, err := svc.issueSetupToken("a1")
	require.NoError(t, err)
	preAuthTok, err := svc.issuePreAuthToken("a1")
	require.NoError(t, err)

	_, err = svc.validateSetupToken(preAuthTok)
	assert.Error(t, err, "a pre-auth token must not pass as a setup token")

	_, err = svc.validatePreAuthToken(setupTok)
	assert.Error(t, err, "a setup token must not pass as a pre-auth token")

	// Each still validates as itself.
	id, err := svc.validateSetupToken(setupTok)
	require.NoError(t, err)
	assert.Equal(t, "a1", id)
}

func TestLogin_With2FA_ReturnsPreAuthToken(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	hashStr := string(hash)
	totpSecret := "JBSWY3DPEHPK3PXP"
	repo := &mockRepo{
		findByEmailFn: func(_ context.Context, _ string) (*AdminAccount, *string, error) {
			return &AdminAccount{ID: "a1", Status: "ACTIVE", TwoFactor: true}, &hashStr, nil
		},
		getTOTPSecretFn: func(_ context.Context, _ string) (*string, error) {
			return &totpSecret, nil
		},
	}

	svc := newTestServiceProduction(repo, newTestRedis(t))

	result, err := svc.Login(context.Background(), "admin@test.com", "secret")
	require.NoError(t, err)
	assert.True(t, result.TwoFactorRequired)
	assert.NotEmpty(t, result.PreAuthToken)
	assert.Empty(t, result.AccessToken)
}

// ── Logout ────────────────────────────────────────────────────────────────

func TestLogout_RevokesSession(t *testing.T) {
	rdb := newTestRedis(t)
	svc := newTestService(&mockRepo{}, rdb)
	err := svc.Logout(context.Background(), "admin-id", "test-jti")
	require.NoError(t, err)
}

// ── Generate2FASetup ──────────────────────────────────────────────────────

func TestGenerate2FASetup_ReturnsSecretAndURL(t *testing.T) {
	repo := &mockRepo{
		findByIDFn: func(_ context.Context, _ string) (*AdminAccount, *string, error) {
			return &AdminAccount{ID: "a1", Email: "admin@test.com"}, nil, nil
		},
	}
	svc := newTestService(repo, newTestRedis(t))
	secret, url, err := svc.Generate2FASetup(context.Background(), "a1")
	require.NoError(t, err)
	assert.NotEmpty(t, secret)
	assert.Contains(t, url, "otpauth://totp/")
}

// ── Enable2FA ─────────────────────────────────────────────────────────────

func TestEnable2FA_InvalidTOTPCode(t *testing.T) {
	svc := newTestServiceProduction(&mockRepo{}, newTestRedis(t))
	// "000000" will not match any valid TOTP for a fresh secret
	_, err := svc.Enable2FA(context.Background(), "a1", "JBSWY3DPEHPK3PXP", "000000")
	require.Error(t, err)
}

// Enrolment must verify the code OUTSIDE production too. This was gated on
// cfg.Env == "production", so on staging any six digits enrolled successfully —
// storing a secret for a QR that was never scanned, which locks the admin out
// permanently because no code they can generate will ever match it.
func TestEnable2FA_RejectsInvalidCode_OutsideProduction(t *testing.T) {
	saved := false
	repo := &mockRepo{
		saveTOTPFn: func(_ context.Context, _, _ string) error {
			saved = true
			return nil
		},
	}
	svc := newTestService(repo, newTestRedis(t)) // cfg.Env is "" — not production

	_, err := svc.Enable2FA(context.Background(), "a1", "JBSWY3DPEHPK3PXP", "000000")

	require.Error(t, err)
	assert.False(t, saved, "a secret must never be stored for an unverified code")
}

// A valid code still enrols outside production — the check is strict, not broken.
func TestEnable2FA_AcceptsValidCode_OutsideProduction(t *testing.T) {
	const secret = "JBSWY3DPEHPK3PXP"
	repo := &mockRepo{
		saveTOTPFn:        func(_ context.Context, _, _ string) error { return nil },
		saveBackupCodesFn: func(_ context.Context, _ string, _ []BackupCode) error { return nil },
	}
	svc := newTestService(repo, newTestRedis(t))

	code, err := totp.GenerateCode(secret, time.Now().UTC())
	require.NoError(t, err)

	codes, err := svc.Enable2FA(context.Background(), "a1", secret, code)
	require.NoError(t, err)
	assert.Len(t, codes, backupCodeCount)
}

// ── Disable2FA ────────────────────────────────────────────────────────────

func TestDisable2FA_WrongPassword(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct"), bcrypt.MinCost)
	hashStr := string(hash)
	repo := &mockRepo{
		findByIDFn: func(_ context.Context, _ string) (*AdminAccount, *string, error) {
			return &AdminAccount{ID: "a1"}, &hashStr, nil
		},
	}
	svc := newTestService(repo, newTestRedis(t))
	err := svc.Disable2FA(context.Background(), "a1", "wrong")
	require.Error(t, err)
}

func TestDisable2FA_Success(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct"), bcrypt.MinCost)
	hashStr := string(hash)
	repo := &mockRepo{
		findByIDFn: func(_ context.Context, _ string) (*AdminAccount, *string, error) {
			return &AdminAccount{ID: "a1"}, &hashStr, nil
		},
		clearTOTPFn: func(_ context.Context, _ string) error { return nil },
	}
	svc := newTestService(repo, newTestRedis(t))
	assert.NoError(t, svc.Disable2FA(context.Background(), "a1", "correct"))
}

// ── PreAuth token round-trip ──────────────────────────────────────────────

func TestPreAuthToken_RoundTrip(t *testing.T) {
	svc := newTestService(&mockRepo{}, newTestRedis(t))
	tok, err := svc.issuePreAuthToken("admin-uuid")
	require.NoError(t, err)
	assert.NotEmpty(t, tok)

	adminID, err := svc.validatePreAuthToken(tok)
	require.NoError(t, err)
	assert.Equal(t, "admin-uuid", adminID)
}

func TestPreAuthToken_Invalid(t *testing.T) {
	svc := newTestService(&mockRepo{}, newTestRedis(t))
	_, err := svc.validatePreAuthToken("not.a.token")
	require.Error(t, err)
}

// ── NewService constructor ────────────────────────────────────────────────

func TestNewService_Constructor(t *testing.T) {
	rdb := newTestRedis(t)
	svc := NewService(&mockRepo{}, testCfg(), rdb, zerolog.Nop())
	require.NotNil(t, svc)
}

// ── UpdateRoleByID delegation ─────────────────────────────────────────────

func TestUpdateRoleByID_Delegates(t *testing.T) {
	desc := "updated"
	repo := &mockRepo{
		updateRoleByIDFn: func(_ context.Context, roleID, _, _ string, _ interface{}) (*Role, error) {
			return &Role{ID: roleID, Name: "Updated", Description: &desc}, nil
		},
	}
	svc := newTestService(repo, newTestRedis(t))
	role, err := svc.UpdateRoleByID(context.Background(), "role-id", "Updated", "updated", nil)
	require.NoError(t, err)
	assert.Equal(t, "Updated", role.Name)
}

// ── generateBackupCodes ───────────────────────────────────────────────────

func TestGenerateBackupCodes_ProducesHashedCodes(t *testing.T) {
	plain, hashed, err := generateBackupCodes()
	require.NoError(t, err)
	assert.Len(t, plain, backupCodeCount)
	assert.Len(t, hashed, backupCodeCount)
	for _, h := range hashed {
		assert.False(t, h.Used)
		assert.NotEmpty(t, h.Hash)
	}
}

// ── Verify2FA ─────────────────────────────────────────────────────────────

func TestVerify2FA_InvalidPreAuthToken(t *testing.T) {
	svc := newTestService(&mockRepo{}, newTestRedis(t))
	_, err := svc.Verify2FA(context.Background(), "bad-token", "123456")
	require.Error(t, err)
}

func TestVerify2FA_NoTOTPSecret(t *testing.T) {
	svc := newTestService(&mockRepo{}, newTestRedis(t))
	tok, _ := svc.issuePreAuthToken("admin-id")
	// repo returns nil secret → no 2FA configured
	_, err := svc.Verify2FA(context.Background(), tok, "123456")
	require.Error(t, err)
}

// ── VerifyBackupCode ──────────────────────────────────────────────────────

func TestVerifyBackupCode_InvalidPreAuthToken(t *testing.T) {
	svc := newTestService(&mockRepo{}, newTestRedis(t))
	_, err := svc.VerifyBackupCode(context.Background(), "bad-token", "any-code")
	require.Error(t, err)
}

func TestVerifyBackupCode_NoBackupCodes(t *testing.T) {
	repo := &mockRepo{
		getBackupCodesFn: func(_ context.Context, _ string) ([]BackupCode, error) {
			return nil, nil // no backup codes stored
		},
	}
	svc := newTestService(repo, newTestRedis(t))
	tok, _ := svc.issuePreAuthToken("admin-id")
	_, err := svc.VerifyBackupCode(context.Background(), tok, "wrong-code")
	require.Error(t, err)
}

func TestVerifyBackupCode_InvalidCode(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("ab1cd-ef2gh"), bcrypt.MinCost)
	repo := &mockRepo{
		getBackupCodesFn: func(_ context.Context, _ string) ([]BackupCode, error) {
			return []BackupCode{{Hash: string(hash), Used: false}}, nil
		},
	}
	svc := newTestService(repo, newTestRedis(t))
	tok, _ := svc.issuePreAuthToken("admin-id")
	_, err := svc.VerifyBackupCode(context.Background(), tok, "wrong-code")
	require.Error(t, err)
}

// ── ResetTOTP ─────────────────────────────────────────────────────────────

func TestResetTOTP_InvalidPreAuthSource(t *testing.T) {
	// Use wrong TOTP secret — code "000000" won't match
	totpSecret := "JBSWY3DPEHPK3PXP"
	repo := &mockRepo{
		getTOTPSecretFn: func(_ context.Context, _ string) (*string, error) {
			return &totpSecret, nil
		},
		findByIDFn: func(_ context.Context, _ string) (*AdminAccount, *string, error) {
			return &AdminAccount{ID: "a1", Email: "admin@test.com"}, nil, nil
		},
	}
	svc := newTestService(repo, newTestRedis(t))
	_, _, _, err := svc.ResetTOTP(context.Background(), "a1", "000000")
	require.Error(t, err)
}

func TestResetTOTP_NoExistingSecret(t *testing.T) {
	// No TOTP set up yet — any code should fail
	repo := &mockRepo{
		getTOTPSecretFn: func(_ context.Context, _ string) (*string, error) {
			return nil, nil
		},
	}
	svc := newTestService(repo, newTestRedis(t))
	_, _, _, err := svc.ResetTOTP(context.Background(), "a1", "123456")
	require.Error(t, err)
}

// Rotating your own second factor requires proving you hold the current one,
// outside production too. Both guards were gated on cfg.Env == "production", so
// on staging an empty code rotated the secret — meaning a stolen session could
// take over the second factor and lock the real owner out. An admin who truly
// lost their device goes through ResetMember2FA, which needs a second person.
func TestResetTOTP_RequiresCurrentCode_OutsideProduction(t *testing.T) {
	const existing = "JBSWY3DPEHPK3PXP"
	rotated := false
	repo := &mockRepo{
		getTOTPSecretFn: func(_ context.Context, _ string) (*string, error) {
			s := existing
			return &s, nil
		},
		saveTOTPFn: func(_ context.Context, _, _ string) error {
			rotated = true
			return nil
		},
	}
	svc := newTestService(repo, newTestRedis(t)) // cfg.Env is "" — not production

	_, _, _, err := svc.ResetTOTP(context.Background(), "a1", "")

	require.Error(t, err)
	assert.False(t, rotated, "an empty code must never rotate an existing secret")
}

func TestTOTPEncryption_RoundTripAndFallback(t *testing.T) {
	originalSecret := "JBSWY3DPEHPK3PXP"
	encrypted, err := encryptTOTP(originalSecret)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(encrypted, "enc:"))

	decrypted, err := decryptTOTP(encrypted)
	require.NoError(t, err)
	assert.Equal(t, originalSecret, decrypted)

	plainSecret := "MYPLAINSECRET"
	fallbackDecrypted, err := decryptTOTP(plainSecret)
	require.NoError(t, err)
	assert.Equal(t, plainSecret, fallbackDecrypted)
}

// 2FA must be enforced in every environment, not just production.
//
// It used to be gated on cfg.Env == "production", which locked staging admins
// out completely: the admin web decides whether to show the 2FA wall from
// NODE_ENV, and a built Next.js image reports "production" even on staging, so
// the UI demanded a code the API would never issue a pre-auth token for.
func TestLogin_With2FA_EnforcedOutsideProduction(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	hashStr := string(hash)
	totpSecret := "JBSWY3DPEHPK3PXP"
	repo := &mockRepo{
		findByEmailFn: func(_ context.Context, _ string) (*AdminAccount, *string, error) {
			return &AdminAccount{ID: "a1", Status: "ACTIVE", TwoFactor: true}, &hashStr, nil
		},
		getTOTPSecretFn: func(_ context.Context, _ string) (*string, error) {
			return &totpSecret, nil
		},
	}

	// Default test config is NOT production — previously this returned a full
	// access token and skipped 2FA entirely.
	svc := newTestService(repo, newTestRedis(t))

	result, err := svc.Login(context.Background(), "admin@test.com", "secret")
	require.NoError(t, err)
	assert.True(t, result.TwoFactorRequired, "2FA must be required outside production too")
	assert.NotEmpty(t, result.PreAuthToken, "a pre-auth token is what /2fa/verify needs")
	assert.Empty(t, result.AccessToken, "no full access token before the second factor")
}

// Staging and production hold separate TOTP secrets for the same email, so one
// person legitimately has two enrolments. A fixed issuer made both show up in
// the authenticator as an identical "Rides Admin: you@example.com", and using
// the wrong one returns only "authenticator code is invalid or expired" — which
// points nowhere near the real cause.
func TestTOTPIssuerLabelNamesNonProductionEnvironments(t *testing.T) {
	prod := newTestServiceProduction(&mockRepo{}, newTestRedis(t))
	assert.Equal(t, "Rides Admin", prod.totpIssuerLabel(),
		"production stays unadorned — it is the one people use daily")

	other := newTestService(&mockRepo{}, newTestRedis(t))
	assert.NotEqual(t, "Rides Admin", other.totpIssuerLabel(),
		"a non-production enrolment must be distinguishable in the authenticator")
	assert.Contains(t, other.totpIssuerLabel(), "Rides Admin")
}
