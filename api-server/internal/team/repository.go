package team

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// BackupCode is one entry stored in admin_accounts.backup_codes JSONB array.
type BackupCode struct {
	Hash string `json:"hash"`
	Used bool   `json:"used"`
}

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

// ── Admin account CRUD ────────────────────────────────────────────────────

func (r *Repository) ListAdmins(ctx context.Context, status, roleID, search string) ([]*AdminAccount, error) {
	var clauses []string
	var args []interface{}
	n := 1

	if status != "" {
		clauses = append(clauses, fmt.Sprintf("a.status = $%d", n))
		args = append(args, status)
		n++
	}
	if roleID != "" {
		clauses = append(clauses, fmt.Sprintf("a.role_id = $%d", n))
		args = append(args, roleID)
		n++
	}
	if search != "" {
		clauses = append(clauses, fmt.Sprintf("(a.name ILIKE $%d OR a.email ILIKE $%d)", n, n))
		args = append(args, "%"+search+"%")
		n++
	}

	where := ""
	if len(clauses) > 0 {
		where = "WHERE " + strings.Join(clauses, " AND ")
	}

	q := fmt.Sprintf(`
		SELECT a.id, a.name, a.email, a.role_id, ar.name,
		       a.status, a.two_factor, a.last_active_at, a.invited_at, a.created_at
		FROM admin_accounts a
		JOIN admin_roles ar ON ar.id = a.role_id
		%s ORDER BY a.created_at DESC
	`, where)

	rows, err := r.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*AdminAccount
	for rows.Next() {
		a := &AdminAccount{}
		if err := rows.Scan(&a.ID, &a.Name, &a.Email, &a.RoleID, &a.RoleName,
			&a.Status, &a.TwoFactor, &a.LastActiveAt, &a.InvitedAt, &a.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, nil
}

func (r *Repository) Invite(ctx context.Context, name, email, roleID string) (*AdminAccount, error) {
	a := &AdminAccount{}
	err := r.db.QueryRow(ctx, `
		INSERT INTO admin_accounts (name, email, role_id)
		VALUES ($1,$2,$3)
		RETURNING id, name, email, role_id,
		          (SELECT name FROM admin_roles WHERE id = $3),
		          status, two_factor, last_active_at, invited_at, created_at
	`, name, email, roleID).Scan(
		&a.ID, &a.Name, &a.Email, &a.RoleID, &a.RoleName,
		&a.Status, &a.TwoFactor, &a.LastActiveAt, &a.InvitedAt, &a.CreatedAt,
	)
	return a, err
}

func (r *Repository) UpdateRole(ctx context.Context, id, roleID string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE admin_accounts SET role_id=$1, updated_at=NOW() WHERE id=$2`, roleID, id)
	return err
}

func (r *Repository) UpdateStatus(ctx context.Context, id, status string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE admin_accounts SET status=$1, updated_at=NOW() WHERE id=$2`, status, id)
	return err
}

func (r *Repository) Delete(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM admin_accounts WHERE id=$1`, id)
	return err
}

func (r *Repository) UpdateName(ctx context.Context, id, name string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE admin_accounts SET name=$1, updated_at=NOW() WHERE id=$2`, name, id)
	return err
}

// ── Auth ──────────────────────────────────────────────────────────────────

// FindByEmail returns the account + password hash for login.
func (r *Repository) FindByEmail(ctx context.Context, email string) (*AdminAccount, *string, error) {
	a := &AdminAccount{}
	var passwordHash *string
	err := r.db.QueryRow(ctx, `
		SELECT a.id, a.name, a.email, a.role_id, ar.name,
		       a.status, a.two_factor, a.last_active_at, a.invited_at, a.created_at,
		       a.password_hash
		FROM admin_accounts a
		JOIN admin_roles ar ON ar.id = a.role_id
		WHERE a.email = $1
	`, email).Scan(
		&a.ID, &a.Name, &a.Email, &a.RoleID, &a.RoleName,
		&a.Status, &a.TwoFactor, &a.LastActiveAt, &a.InvitedAt, &a.CreatedAt,
		&passwordHash,
	)
	return a, passwordHash, err
}

// FindByID returns the account + password hash by primary key.
func (r *Repository) FindByID(ctx context.Context, id string) (*AdminAccount, *string, error) {
	a := &AdminAccount{}
	var passwordHash *string
	err := r.db.QueryRow(ctx, `
		SELECT a.id, a.name, a.email, a.role_id, ar.name,
		       a.status, a.two_factor, a.last_active_at, a.invited_at, a.created_at,
		       a.password_hash
		FROM admin_accounts a
		JOIN admin_roles ar ON ar.id = a.role_id
		WHERE a.id = $1
	`, id).Scan(
		&a.ID, &a.Name, &a.Email, &a.RoleID, &a.RoleName,
		&a.Status, &a.TwoFactor, &a.LastActiveAt, &a.InvitedAt, &a.CreatedAt,
		&passwordHash,
	)
	return a, passwordHash, err
}

func (r *Repository) SetPassword(ctx context.Context, id, hash string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE admin_accounts SET password_hash=$1, status='ACTIVE', updated_at=NOW() WHERE id=$2`, hash, id)
	return err
}

func (r *Repository) TouchLastActive(ctx context.Context, id string) {
	_, _ = r.db.Exec(ctx, `UPDATE admin_accounts SET last_active_at=NOW() WHERE id=$1`, id)
}

// ── Roles ─────────────────────────────────────────────────────────────────

func (r *Repository) ListRoles(ctx context.Context) ([]*Role, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, name, description, permissions, is_system, created_at
		FROM admin_roles ORDER BY is_system DESC, name ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*Role
	for rows.Next() {
		role := &Role{}
		var raw []byte
		if err := rows.Scan(&role.ID, &role.Name, &role.Description, &raw, &role.IsSystem, &role.CreatedAt); err != nil {
			return nil, err
		}
		var perms interface{}
		if err := json.Unmarshal(raw, &perms); err == nil {
			role.Permissions = perms
		}
		result = append(result, role)
	}
	return result, nil
}

// ── 2FA ───────────────────────────────────────────────────────────────────

// GetTOTPSecret returns the stored TOTP secret for an admin (nil if not set).
func (r *Repository) GetTOTPSecret(ctx context.Context, id string) (*string, error) {
	var secret *string
	err := r.db.QueryRow(ctx,
		`SELECT totp_secret FROM admin_accounts WHERE id=$1`, id).Scan(&secret)
	return secret, err
}

// SaveTOTP stores the verified TOTP secret and marks two_factor = true.
func (r *Repository) SaveTOTP(ctx context.Context, id, secret string) error {
	_, err := r.db.Exec(ctx, `
		UPDATE admin_accounts
		SET totp_secret=$1, two_factor=TRUE, updated_at=NOW()
		WHERE id=$2
	`, secret, id)
	return err
}

// ClearTOTP removes TOTP and backup codes and marks two_factor = false.
func (r *Repository) ClearTOTP(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `
		UPDATE admin_accounts
		SET totp_secret=NULL, backup_codes='[]', two_factor=FALSE, updated_at=NOW()
		WHERE id=$2
	`, id)
	return err
}

// GetBackupCodes returns the stored backup codes for an admin.
func (r *Repository) GetBackupCodes(ctx context.Context, id string) ([]BackupCode, error) {
	var raw []byte
	if err := r.db.QueryRow(ctx,
		`SELECT backup_codes FROM admin_accounts WHERE id=$1`, id).Scan(&raw); err != nil {
		return nil, err
	}
	var codes []BackupCode
	if err := json.Unmarshal(raw, &codes); err != nil {
		return nil, err
	}
	return codes, nil
}

// SaveBackupCodes stores hashed backup codes for an admin.
func (r *Repository) SaveBackupCodes(ctx context.Context, id string, codes []BackupCode) error {
	raw, err := json.Marshal(codes)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(ctx,
		`UPDATE admin_accounts SET backup_codes=$1, updated_at=NOW() WHERE id=$2`, raw, id)
	return err
}
