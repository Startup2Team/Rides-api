// Package nationalidbackfill holds the actual logic behind
// cmd/national-id-backfill — pulled out of package main (which cannot be
// imported by tests, see https://pkg.go.dev/cmd/go#hdr-Description_of_package_lists)
// so test/dbit can exercise it directly against a real Postgres, the same
// pattern cmd/ledger-backfill uses (thin main, real logic in an internal
// package).
//
// Assigns a random, format-valid RW national ID PLACEHOLDER to every existing
// driver that predates DB-1 (migration 080) and so has no national_id_number
// captured — a driver with driver_profiles but national_id_number IS NULL.
//
// This does not invent an identity for a real person: every row it writes is
// flagged national_id_placeholder = TRUE (migration 084) so a placeholder is
// always distinguishable from a real, driver-supplied ID and can be found and
// reconciled later against the ID images already on file (driver_documents
// NATIONAL_ID_FRONT/BACK).
//
// Dry-run by default (reports what WOULD change, writes nothing); Run's apply
// parameter must be true to actually write. Idempotent: a user who already
// has a national_id_number (real or a placeholder from a prior run) is
// excluded by the candidate SELECT and, defensively, by the UPDATE's own
// WHERE clause — re-running is always safe.
package nationalidbackfill

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math/big"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// MaxAttemptsPerUser bounds retries on a collision against
// uq_users_national_id (RW, 16 random digits — a collision is astronomically
// unlikely, but the constraint exists precisely to catch it if it ever
// happens, so a retry loop is the correct response, not a crash).
const MaxAttemptsPerUser = 10

// DBConn is the minimal subset of *pgxpool.Pool Run needs. A real pool
// satisfies it directly (no adapter needed in main); test/dbit passes its
// real pool through the same way, so the test exercises the exact same SQL
// against a real Postgres rather than a mock.
type DBConn interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// IsNationalIDConflict reports whether err is specifically a 23505 violation
// of uq_users_national_id — mirrors the identically-purposed, independently
// kept copies in internal/driver and internal/admin (see their comments for
// why this isn't shared: each bounded context, including this one-off
// command, keeps its own small copy rather than introducing a cross-package
// dependency for a two-line check).
func IsNationalIDConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "uq_users_national_id"
}

// GenerateRWNationalID produces a random, uniformly-distributed 16-digit
// string (including leading zeros) — the RW format pkg/nationalid validates
// (`^\d{16}$`). crypto/rand, not math/rand: mirrors internal/auth's OTP
// generator (generateOTP) — this value ends up in a unique, queryable column,
// so it should be as unpredictable as anything else keyed the same way.
func GenerateRWNationalID() (string, error) {
	max := new(big.Int).Exp(big.NewInt(10), big.NewInt(16), nil) // 10^16
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%016d", n), nil
}

// Result summarizes one Run call — main()'s stdout summary line and
// test/dbit's assertions both read off of this.
type Result struct {
	Candidates  int
	Backfilled  int
	AlreadyDone int
	Skipped     int
}

// Run is the command's actual logic. Behavior is unchanged from the original
// inline version in main(): same candidate query, same dry-run short-circuit,
// same per-user retry-on-collision loop, same WHERE-clause idempotency guard
// on the UPDATE. stderr receives the same per-user warnings main() used to
// print directly.
func Run(ctx context.Context, db DBConn, apply bool, stderr io.Writer) (Result, error) {
	rows, err := db.Query(ctx, `
		SELECT u.id
		FROM users u
		JOIN driver_profiles dp ON dp.user_id = u.id
		WHERE u.national_id_number IS NULL
		ORDER BY u.created_at ASC`)
	if err != nil {
		return Result{}, fmt.Errorf("query candidates: %w", err)
	}
	var userIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return Result{}, fmt.Errorf("scan: %w", err)
		}
		userIDs = append(userIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Result{}, fmt.Errorf("rows: %w", err)
	}

	result := Result{Candidates: len(userIDs)}
	if len(userIDs) == 0 || !apply {
		return result, nil
	}

	for _, userID := range userIDs {
		ok := false
		for attempt := 1; attempt <= MaxAttemptsPerUser; attempt++ {
			number, genErr := GenerateRWNationalID()
			if genErr != nil {
				fmt.Fprintf(stderr, "backfill %s: generate id: %v\n", userID, genErr)
				break
			}
			tag, execErr := db.Exec(ctx, `
				UPDATE users
				SET national_id_number = $1,
				    national_id_country = 'RW',
				    national_id_placeholder = TRUE,
				    updated_at = NOW()
				WHERE id = $2 AND national_id_number IS NULL
			`, number, userID)
			if execErr != nil {
				if IsNationalIDConflict(execErr) {
					if attempt == MaxAttemptsPerUser {
						fmt.Fprintf(stderr, "backfill %s: exhausted %d attempts on national-ID collisions\n", userID, MaxAttemptsPerUser)
					}
					continue // collision on a randomly generated number — retry with a new one
				}
				fmt.Fprintf(stderr, "backfill %s: %v\n", userID, execErr)
				break
			}
			ok = true
			if tag.RowsAffected() > 0 {
				result.Backfilled++
			} else {
				// Already has a national ID — set concurrently, by a prior
				// run, or by the driver themselves between the candidate
				// SELECT and this UPDATE. Not a failure; this WHERE clause is
				// exactly what makes re-running this command safe.
				result.AlreadyDone++
			}
			break
		}
		if !ok {
			result.Skipped++
		}
	}

	return result, nil
}
