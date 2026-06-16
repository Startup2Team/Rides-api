package team

import "context"

// TeamRepo is the interface Service depends on for persistence.
// *Repository satisfies it automatically.
type TeamRepo interface {
	FindByEmail(ctx context.Context, email string) (*AdminAccount, *string, error)
	FindByID(ctx context.Context, id string) (*AdminAccount, *string, error)
	TouchLastActive(ctx context.Context, id string)
	ListAdmins(ctx context.Context, status, roleID, search string) ([]*AdminAccount, error)
	Invite(ctx context.Context, name, email, roleID string) (*AdminAccount, error)
	UpdateRole(ctx context.Context, id, roleID string) error
	UpdateStatus(ctx context.Context, id, status string) error
	Delete(ctx context.Context, id string) error
	UpdateName(ctx context.Context, id, name string) error
	SetPassword(ctx context.Context, id, hash string) error
	GetTOTPSecret(ctx context.Context, id string) (*string, error)
	SaveTOTP(ctx context.Context, id, secret string) error
	ClearTOTP(ctx context.Context, id string) error
	GetBackupCodes(ctx context.Context, id string) ([]BackupCode, error)
	SaveBackupCodes(ctx context.Context, id string, codes []BackupCode) error
	ListRoles(ctx context.Context) ([]*Role, error)
	CreateRole(ctx context.Context, name, description string, permissions interface{}) (*Role, error)
	UpdateRoleByID(ctx context.Context, roleID, name, description string, permissions interface{}) (*Role, error)
	DeleteRoleByID(ctx context.Context, roleID string) error
	LogAction(ctx context.Context, adminID, action, targetType, targetID, detail, ip string) error
	GetMemberActivity(ctx context.Context, adminID string, limit int) ([]AuditEntry, error)
	ListAuditLog(ctx context.Context, actor, action, targetType, from, to string, limit, offset int) ([]AuditEntry, int, error)
}
