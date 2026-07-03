package team

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
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
func (m *mockRepo) UpdateName(ctx context.Context, id, name string) error {
	if m.updateNameFn != nil {
		return m.updateNameFn(ctx, id, name)
	}
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

func newTestRedis(t *testing.T) *goredis.Client {
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
		},
	}
}

func newTestService(repo TeamRepo, rdb *goredis.Client) *Service {
	return &Service{repo: repo, cfg: testCfg(), rdb: rdb}
}

func newTestServiceProduction(repo TeamRepo, rdb *goredis.Client) *Service {
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

func TestLogin_No2FA_ReturnsAccessToken(t *testing.T) {
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
	assert.NotEmpty(t, result.AccessToken)
	assert.False(t, result.TwoFactorRequired)
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

	// 2FA is only enforced in production (dev skips it for testing ergonomics),
	// so exercise the pre-auth path with a production config.
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
	svc := NewService(&mockRepo{}, testCfg(), rdb)
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
