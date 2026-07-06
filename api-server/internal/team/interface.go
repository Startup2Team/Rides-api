package team

import "context"

// TeamService is the interface Handler depends on.
// *Service satisfies it automatically via Go structural typing.
type TeamService interface {
	Login(ctx context.Context, email, password string) (*LoginResult, error)
	Verify2FA(ctx context.Context, preAuthToken, code string) (*LoginResult, error)
	VerifyBackupCode(ctx context.Context, preAuthToken, backupCode string) (*LoginResult, error)
	Reissue2FAChallenge(ctx context.Context, adminID string) (string, error)
	Logout(ctx context.Context, adminID, jti string) error
	Generate2FASetup(ctx context.Context, adminID string) (secret, otpauthURL string, err error)
	Enable2FA(ctx context.Context, adminID, secret, code string) ([]string, error)
	Disable2FA(ctx context.Context, adminID, password string) error
	ResetTOTP(ctx context.Context, adminID, currentCode string) (secret, qr string, backupCodes []string, err error)
	ResetTOTPFromPreAuth(ctx context.Context, preAuthToken, currentCode string) (secret, qr string, backupCodes []string, err error)
	ListAdmins(ctx context.Context, status, roleID, search string) ([]*AdminAccount, error)
	ListAuditLog(ctx context.Context, actor, action, targetType, from, to string, limit, offset int) ([]AuditEntry, int, error)
	Invite(ctx context.Context, name, email, roleID, password string) (*AdminAccount, error)
	ListRoles(ctx context.Context) ([]*Role, error)
	CreateRole(ctx context.Context, name, description string, permissions interface{}) (*Role, error)
	UpdateRoleByID(ctx context.Context, roleID, name, description string, permissions interface{}) (*Role, error)
	UpdateRolePermissions(ctx context.Context, roleID string, permissions interface{}) error
	DeleteRoleByID(ctx context.Context, roleID string) error
	UpdateRole(ctx context.Context, id, roleID string) error
	Suspend(ctx context.Context, id string) error
	Reinstate(ctx context.Context, id string) error
	Remove(ctx context.Context, id string) error
	ResendInvite(ctx context.Context, id string) error
	ResetMember2FA(ctx context.Context, actorID, memberID string) error
	GetMemberActivity(ctx context.Context, adminID string, limit int) ([]AuditEntry, error)
	UpdateName(ctx context.Context, id, name string) error
	ChangePassword(ctx context.Context, id, currentPassword, newPassword string) error
	SetPassword(ctx context.Context, id, password string) error
}
