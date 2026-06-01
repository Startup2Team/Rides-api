package team

import "context"

// TeamService is the interface Handler depends on.
// *Service satisfies it automatically via Go structural typing.
type TeamService interface {
	Login(ctx context.Context, email, password string) (*LoginResult, error)
	Verify2FA(ctx context.Context, preAuthToken, code string) (*LoginResult, error)
	VerifyBackupCode(ctx context.Context, preAuthToken, backupCode string) (*LoginResult, error)
	Logout(ctx context.Context, adminID, jti string) error
	Generate2FASetup(ctx context.Context, adminID string) (secret, otpauthURL string, err error)
	Enable2FA(ctx context.Context, adminID, secret, code string) ([]string, error)
	Disable2FA(ctx context.Context, adminID, password string) error
	ResetTOTP(ctx context.Context, adminID, currentCode string) (secret, qr string, backupCodes []string, err error)
	ListAdmins(ctx context.Context, status, roleID, search string) ([]*AdminAccount, error)
	Invite(ctx context.Context, name, email, roleID string) (*AdminAccount, error)
	ListRoles(ctx context.Context) ([]*Role, error)
	CreateRole(ctx context.Context, name, description string, permissions interface{}) (*Role, error)
	UpdateRoleByID(ctx context.Context, roleID, name, description string, permissions interface{}) (*Role, error)
	DeleteRoleByID(ctx context.Context, roleID string) error
	UpdateRole(ctx context.Context, id, roleID string) error
	Suspend(ctx context.Context, id string) error
	Reinstate(ctx context.Context, id string) error
	Remove(ctx context.Context, id string) error
	UpdateName(ctx context.Context, id, name string) error
	ChangePassword(ctx context.Context, id, currentPassword, newPassword string) error
	SetPassword(ctx context.Context, id, password string) error
}
