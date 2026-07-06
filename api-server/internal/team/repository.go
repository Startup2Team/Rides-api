package team

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
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

// TouchInvitedAt bumps invited_at for a pending admin invite (resend flow).
func (r *Repository) TouchInvitedAt(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE admin_accounts SET invited_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND status IN ('INVITED', 'ACTIVE')
	`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("admin not found or not eligible for invite resend")
	}
	return nil
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
	if err != nil {
		return nil, err
	}
	if secret == nil {
		return nil, nil
	}
	decrypted, err := decryptTOTP(*secret)
	if err != nil {
		return nil, fmt.Errorf("decrypt totp: %w", err)
	}
	return &decrypted, nil
}

// SaveTOTP stores the verified TOTP secret and marks two_factor = true.
func (r *Repository) SaveTOTP(ctx context.Context, id, secret string) error {
	encrypted, err := encryptTOTP(secret)
	if err != nil {
		return fmt.Errorf("encrypt totp: %w", err)
	}
	_, err = r.db.Exec(ctx, `
		UPDATE admin_accounts
		SET totp_secret=$1, two_factor=TRUE, updated_at=NOW()
		WHERE id=$2
	`, encrypted, id)
	return err
}

func getEncryptionKey() []byte {
	k := os.Getenv("TOTP_ENCRYPTION_KEY")
	if k == "" {
		k = "default-dev-totp-encryption-key-do-not-use-in-prod"
	}
	hash := sha256.Sum256([]byte(k))
	return hash[:]
}

func encryptTOTP(plaintext string) (string, error) {
	key := getEncryptionKey()
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return "enc:" + base64.StdEncoding.EncodeToString(ciphertext), nil
}

func decryptTOTP(ciphertextStr string) (string, error) {
	if !strings.HasPrefix(ciphertextStr, "enc:") {
		return ciphertextStr, nil
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(ciphertextStr, "enc:"))
	if err != nil {
		return "", err
	}
	key := getEncryptionKey()
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// ClearTOTP removes TOTP and backup codes and marks two_factor = false.
func (r *Repository) ClearTOTP(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `
		UPDATE admin_accounts
		SET totp_secret=NULL, backup_codes='[]', two_factor=FALSE, updated_at=NOW()
		WHERE id=$1
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

// ── Role CRUD ─────────────────────────────────────────────────────────────

func (r *Repository) CreateRole(ctx context.Context, name, description string, permissions interface{}) (*Role, error) {
	raw, err := json.Marshal(permissions)
	if err != nil {
		return nil, err
	}
	role := &Role{}
	var rawPerms []byte
	err = r.db.QueryRow(ctx, `
		INSERT INTO admin_roles (name, description, permissions)
		VALUES ($1, $2, $3)
		RETURNING id, name, description, permissions, is_system, created_at
	`, name, description, raw).Scan(
		&role.ID, &role.Name, &role.Description, &rawPerms, &role.IsSystem, &role.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	var perms interface{}
	if err := json.Unmarshal(rawPerms, &perms); err == nil {
		role.Permissions = perms
	}
	return role, nil
}

func (r *Repository) UpdateRoleByID(ctx context.Context, roleID, name, description string, permissions interface{}) (*Role, error) {
	raw, err := json.Marshal(permissions)
	if err != nil {
		return nil, err
	}
	role := &Role{}
	var rawPerms []byte
	err = r.db.QueryRow(ctx, `
		UPDATE admin_roles SET name=$1, description=$2, permissions=$3
		WHERE id=$4 AND is_system=FALSE
		RETURNING id, name, description, permissions, is_system, created_at
	`, name, description, raw, roleID).Scan(
		&role.ID, &role.Name, &role.Description, &rawPerms, &role.IsSystem, &role.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	var perms interface{}
	if err := json.Unmarshal(rawPerms, &perms); err == nil {
		role.Permissions = perms
	}
	return role, nil
}

// UpdateRolePermissions replaces only the permissions of a non-system role.
// Returns cannot_modify_system_role if the role is a system role or missing.
func (r *Repository) UpdateRolePermissions(ctx context.Context, roleID string, permissions interface{}) error {
	raw, err := json.Marshal(permissions)
	if err != nil {
		return err
	}
	tag, err := r.db.Exec(ctx, `
		UPDATE admin_roles SET permissions=$1
		WHERE id=$2 AND is_system=FALSE
	`, raw, roleID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("cannot_modify_system_role")
	}
	return nil
}

// ReissueInvite re-stamps invited_at for a still-pending (not yet ACTIVE) admin.
// Returns the number of rows updated (0 = already active or not found).
func (r *Repository) ReissueInvite(ctx context.Context, id string) (int64, error) {
	tag, err := r.db.Exec(ctx, `
		UPDATE admin_accounts SET invited_at=NOW(), updated_at=NOW()
		WHERE id=$1 AND status <> 'ACTIVE'
	`, id)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (r *Repository) DeleteRoleByID(ctx context.Context, roleID string) error {
	var isSystem bool
	if err := r.db.QueryRow(ctx,
		`SELECT is_system FROM admin_roles WHERE id=$1`, roleID).Scan(&isSystem); err != nil {
		return err
	}
	if isSystem {
		return fmt.Errorf("cannot_delete_system_role")
	}
	_, err := r.db.Exec(ctx, `DELETE FROM admin_roles WHERE id=$1`, roleID)
	return err
}

// LogAction inserts one audit entry. Errors are non-fatal — callers ignore them.
func (r *Repository) LogAction(ctx context.Context, adminID, action, targetType, targetID, detail, ip string) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO admin_audit_log (admin_id, action, target_type, target_id, detail, ip)
		VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''), NULLIF($6, ''))
	`, adminID, action, targetType, targetID, detail, ip)
	return err
}

// GetMemberActivity returns the most recent audit entries for a given admin.
func (r *Repository) GetMemberActivity(ctx context.Context, adminID string, limit int) ([]AuditEntry, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, action, COALESCE(target_type, ''), COALESCE(target_id, ''), COALESCE(detail, ''), COALESCE(ip, ''), occurred_at
		FROM admin_audit_log
		WHERE admin_id = $1
		ORDER BY occurred_at DESC
		LIMIT $2
	`, adminID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []AuditEntry
	for rows.Next() {
		var e AuditEntry
		err := rows.Scan(&e.ID, &e.Action, &e.TargetType, &e.TargetID, &e.Detail, &e.IP, &e.OccurredAt)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

// ListAuditLog returns the platform-wide admin audit trail with optional filters,
// newest first. Super Admin only — enforced at the route level.
func (r *Repository) ListAuditLog(ctx context.Context, actor, action, targetType, from, to string, limit, offset int) ([]AuditEntry, int, error) {
	var clauses []string
	var args []interface{}
	n := 1

	if actor != "" {
		clauses = append(clauses, fmt.Sprintf("l.admin_id = $%d", n))
		args = append(args, actor)
		n++
	}
	if action != "" {
		clauses = append(clauses, fmt.Sprintf("l.action ILIKE $%d", n))
		args = append(args, "%"+action+"%")
		n++
	}
	if targetType != "" {
		clauses = append(clauses, fmt.Sprintf("l.target_type = $%d", n))
		args = append(args, targetType)
		n++
	}
	if from != "" {
		clauses = append(clauses, fmt.Sprintf("l.occurred_at >= $%d", n))
		args = append(args, from)
		n++
	}
	if to != "" {
		clauses = append(clauses, fmt.Sprintf("l.occurred_at <= $%d", n))
		args = append(args, to)
		n++
	}

	where := ""
	if len(clauses) > 0 {
		where = "WHERE " + strings.Join(clauses, " AND ")
	}

	countQ := fmt.Sprintf(`SELECT COUNT(*) FROM admin_audit_log l %s`, where)
	var total int
	if err := r.db.QueryRow(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, limit, offset)
	q := fmt.Sprintf(`
		SELECT l.id, l.admin_id, a.name, COALESCE(l.admin_role, ''), l.action,
		       COALESCE(l.target_type, ''), COALESCE(l.target_id, ''), COALESCE(l.detail, ''),
		       COALESCE(l.ip, ''), l.metadata, l.occurred_at
		FROM admin_audit_log l
		JOIN admin_accounts a ON a.id = l.admin_id
		%s
		ORDER BY l.occurred_at DESC
		LIMIT $%d OFFSET $%d
	`, where, n, n+1)

	rows, err := r.db.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var entries []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var rawMeta []byte
		if err := rows.Scan(&e.ID, &e.AdminID, &e.AdminName, &e.AdminRole, &e.Action,
			&e.TargetType, &e.TargetID, &e.Detail, &e.IP, &rawMeta, &e.OccurredAt); err != nil {
			return nil, 0, err
		}
		if len(rawMeta) > 0 {
			_ = json.Unmarshal(rawMeta, &e.Metadata)
		}
		entries = append(entries, e)
	}
	if err = rows.Err(); err != nil {
		return nil, 0, err
	}
	return entries, total, nil
}
