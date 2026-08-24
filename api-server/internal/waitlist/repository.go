package waitlist

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

// referralCodeAlphabet excludes visually ambiguous characters (0/O, 1/I/L)
// since the code is meant to be read aloud / typed by a person sharing it.
const referralCodeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
const referralCodeLength = 8

// maxReferralCodeAttempts bounds retries on the astronomically rare event of
// a referral_code collision (32^8 keyspace) so a pathological run of bad luck
// still fails fast instead of looping forever.
const maxReferralCodeAttempts = 5

func generateReferralCode() (string, error) {
	alphabetLen := big.NewInt(int64(len(referralCodeAlphabet)))
	b := make([]byte, referralCodeLength)
	for i := range b {
		n, err := rand.Int(rand.Reader, alphabetLen)
		if err != nil {
			return "", fmt.Errorf("rand: %w", err)
		}
		b[i] = referralCodeAlphabet[n.Int64()]
	}
	return string(b), nil
}

// phone is nullable since migration 087 (phone-optional signups) — COALESCE
// it to "" so the Go-side Signup.Phone can stay a plain (non-pointer) string,
// matching the JSON contract every existing consumer (admin console) already
// expects: "phone" is always present, just sometimes empty now.
const signupColumns = `
	id, role, name, COALESCE(phone, '') AS phone, email, area, vehicle_type, referral_code,
	referred_by, consent_launch, consent_marketing, source, opted_out_at, created_at
`

func scanSignup(row pgx.Row) (*Signup, error) {
	s := &Signup{}
	err := row.Scan(
		&s.ID, &s.Role, &s.Name, &s.Phone, &s.Email, &s.Area, &s.VehicleType, &s.ReferralCode,
		&s.ReferredBy, &s.ConsentLaunch, &s.ConsentMarketing, &s.Source, &s.OptedOutAt, &s.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// Create inserts a new waitlist signup, generating a unique referral code.
//
// Dedupe target depends on what the submitter gave us (phone and email are
// both optional as of migration 087):
//   - phone supplied: dedupe on (role, phone) — the original UNIQUE constraint.
//   - phone absent, email supplied: dedupe on (role, email) via the partial
//     unique index added in 087 (it only covers phone IS NULL rows).
//   - neither supplied: nothing to dedupe against — every submission is a new
//     row. Accepted gap: a name-and-area-only signup can't be deduped.
//
// Either way, a conflict is treated as an idempotent success: the existing
// row is returned with created=false so the caller does not re-send the
// confirmation SMS/email for a repeat submission.
func (r *Repository) Create(ctx context.Context, in CreateInput) (signup *Signup, created bool, err error) {
	hasPhone := strings.TrimSpace(in.Phone) != ""
	hasEmail := in.Email != nil && strings.TrimSpace(*in.Email) != ""

	// nil (not "") so a blank phone is persisted as SQL NULL — required both
	// for the phone-optional relaxation itself and so multiple no-phone rows
	// don't collide against the original UNIQUE(role, phone) constraint
	// (Postgres never treats NULL <> NULL as a match).
	var phoneArg interface{}
	if hasPhone {
		phoneArg = in.Phone
	}

	var conflictClause string
	switch {
	case hasPhone:
		conflictClause = "ON CONFLICT (role, phone) DO NOTHING"
	case hasEmail:
		// Must match the partial index's predicate exactly (Postgres requires
		// the ON CONFLICT inference to match the arbiter index's WHERE clause).
		conflictClause = "ON CONFLICT (role, email) WHERE phone IS NULL AND email IS NOT NULL DO NOTHING"
	}

	for attempt := 0; attempt < maxReferralCodeAttempts; attempt++ {
		code, err := generateReferralCode()
		if err != nil {
			return nil, false, fmt.Errorf("generate referral code: %w", err)
		}

		row := r.db.QueryRow(ctx, `
			INSERT INTO waitlist_signups
				(role, name, phone, email, area, vehicle_type, referral_code, referred_by, consent_launch, consent_marketing, source)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
			`+conflictClause+`
			RETURNING `+signupColumns,
			in.Role, in.Name, phoneArg, in.Email, in.Area, in.VehicleType, code, in.ReferredBy,
			in.ConsentLaunch, in.ConsentMarketing, in.Source,
		)
		s, scanErr := scanSignup(row)
		if scanErr == nil {
			return s, true, nil
		}
		if errors.Is(scanErr, pgx.ErrNoRows) {
			// The conflict target's ON CONFLICT DO NOTHING skipped the insert,
			// so RETURNING produced no row. Idempotent success: fetch it.
			var existing *Signup
			var findErr error
			switch {
			case hasPhone:
				existing, findErr = r.findByRolePhone(ctx, in.Role, in.Phone)
			case hasEmail:
				existing, findErr = r.findByRoleEmailNoPhone(ctx, in.Role, *in.Email)
			}
			if findErr != nil {
				return nil, false, fmt.Errorf("find existing signup: %w", findErr)
			}
			if existing == nil {
				// Defensive: a contact-less insert uses no ON CONFLICT target, so
				// it can't reach ErrNoRows today — but guard so a future change
				// can't return a nil signup that the handler would deref.
				return nil, false, fmt.Errorf("waitlist: dedupe hit ErrNoRows with no lookup key")
			}
			return existing, false, nil
		}
		if isUniqueViolation(scanErr) {
			// referral_code collision — retry with a freshly generated code.
			continue
		}
		return nil, false, fmt.Errorf("insert signup: %w", scanErr)
	}
	return nil, false, fmt.Errorf("waitlist: could not generate a unique referral code after %d attempts", maxReferralCodeAttempts)
}

func (r *Repository) findByRolePhone(ctx context.Context, role, phone string) (*Signup, error) {
	row := r.db.QueryRow(ctx, `SELECT `+signupColumns+` FROM waitlist_signups WHERE role = $1 AND phone = $2`, role, phone)
	return scanSignup(row)
}

// findByRoleEmailNoPhone looks up the phone-less signup an email-only
// submission collided with — mirrors findByRolePhone for the (role, email)
// WHERE phone IS NULL partial-unique-index dedupe path (migration 087).
func (r *Repository) findByRoleEmailNoPhone(ctx context.Context, role, email string) (*Signup, error) {
	row := r.db.QueryRow(ctx, `SELECT `+signupColumns+` FROM waitlist_signups WHERE role = $1 AND email = $2 AND phone IS NULL`, role, email)
	return scanSignup(row)
}

// List returns signups newest-first, optionally filtered by role/area.
func (r *Repository) List(ctx context.Context, f ListFilter) ([]*Signup, int, error) {
	where, args := buildWhere(f)

	var total int
	if err := r.db.QueryRow(ctx, `SELECT COUNT(*) FROM waitlist_signups `+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count signups: %w", err)
	}

	n := len(args) + 1
	q := fmt.Sprintf(`SELECT %s FROM waitlist_signups %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d`, signupColumns, where, n, n+1)
	args = append(args, f.Limit, f.Offset)

	rows, err := r.db.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list signups: %w", err)
	}
	defer rows.Close()

	var result []*Signup
	for rows.Next() {
		s, err := scanSignup(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan signup: %w", err)
		}
		result = append(result, s)
	}
	return result, total, rows.Err()
}

func buildWhere(f ListFilter) (string, []interface{}) {
	var clauses []string
	var args []interface{}
	n := 1

	if f.Role != "" {
		clauses = append(clauses, fmt.Sprintf("role = $%d", n))
		args = append(args, f.Role)
		n++
	}
	if f.Area != "" {
		clauses = append(clauses, fmt.Sprintf("area ILIKE $%d", n))
		args = append(args, "%"+f.Area+"%")
		n++
	}

	if len(clauses) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

// isUniqueViolation reports whether err is a Postgres unique-constraint
// (23505) failure — matches the auth package's convention for turning a
// collision into a retry/friendly-error rather than a raw DB error. Checks
// the structured pgconn.PgError code rather than substring-matching the
// error string, which is brittle against wrapped errors and message-text
// changes across pgx/Postgres versions.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
