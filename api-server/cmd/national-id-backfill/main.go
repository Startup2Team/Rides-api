// Command national-id-backfill assigns a random, format-valid RW national ID
// PLACEHOLDER to every existing driver that predates DB-1 (migration 080) and
// so has no national_id_number captured — a driver with driver_profiles but
// national_id_number IS NULL.
//
// This does not invent an identity for a real person: every row it writes is
// flagged national_id_placeholder = TRUE (migration 084) so a placeholder is
// always distinguishable from a real, driver-supplied ID and can be found and
// reconciled later against the ID images already on file (driver_documents
// NATIONAL_ID_FRONT/BACK).
//
// Dry-run by default (reports what WOULD change, writes nothing); pass -apply
// to actually write. Idempotent: a user who already has a national_id_number
// (real or a placeholder from a prior run) is excluded by the candidate
// SELECT and, defensively, by the UPDATE's own WHERE clause — re-running this
// command is always safe.
//
//	go run ./cmd/national-id-backfill            # dry run (default)
//	go run ./cmd/national-id-backfill -apply      # actually write
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/workspace/ride-platform/config"
	pgpkg "github.com/workspace/ride-platform/pkg/postgres"
)

// maxAttemptsPerUser bounds retries on a collision against
// uq_users_national_id (RW, 16 random digits — a collision is astronomically
// unlikely, but the constraint exists precisely to catch it if it ever
// happens, so a retry loop is the correct response, not a crash).
const maxAttemptsPerUser = 10

// isNationalIDConflict reports whether err is specifically a 23505 violation
// of uq_users_national_id — mirrors the identically-purposed, independently
// kept copies in internal/driver and internal/admin (see their comments for
// why this isn't shared: each bounded context, including this one-off
// command, keeps its own small copy rather than introducing a cross-package
// dependency for a two-line check).
func isNationalIDConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "uq_users_national_id"
}

// generateRWNationalID produces a random, uniformly-distributed 16-digit
// string (including leading zeros) — the RW format pkg/nationalid validates
// (`^\d{16}$`). crypto/rand, not math/rand: mirrors internal/auth's OTP
// generator (generateOTP) — this value ends up in a unique, queryable column,
// so it should be as unpredictable as anything else keyed the same way.
func generateRWNationalID() (string, error) {
	max := new(big.Int).Exp(big.NewInt(10), big.NewInt(16), nil) // 10^16
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%016d", n), nil
}

func main() {
	apply := flag.Bool("apply", false, "actually write the backfill (default: dry run, writes nothing)")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config load:", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	db, err := pgpkg.New(ctx, cfg.Database.URL, cfg.Database.MaxConns, cfg.Database.MinConns)
	if err != nil {
		fmt.Fprintln(os.Stderr, "postgres connect:", err)
		os.Exit(1)
	}
	defer db.Close()

	rows, err := db.Query(ctx, `
		SELECT u.id
		FROM users u
		JOIN driver_profiles dp ON dp.user_id = u.id
		WHERE u.national_id_number IS NULL
		ORDER BY u.created_at ASC`)
	if err != nil {
		fmt.Fprintln(os.Stderr, "query candidates:", err)
		os.Exit(1)
	}
	var userIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			fmt.Fprintln(os.Stderr, "scan:", err)
			os.Exit(1)
		}
		userIDs = append(userIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "rows:", err)
		os.Exit(1)
	}

	if len(userIDs) == 0 {
		fmt.Println("national-id-backfill: no drivers need a placeholder national ID — nothing to do")
		return
	}

	if !*apply {
		fmt.Printf("national-id-backfill: DRY RUN — %d driver(s) would get a placeholder RW national ID. Re-run with -apply to write.\n", len(userIDs))
		return
	}

	var backfilled, alreadyDone, skipped int
	for _, userID := range userIDs {
		ok := false
		for attempt := 1; attempt <= maxAttemptsPerUser; attempt++ {
			number, genErr := generateRWNationalID()
			if genErr != nil {
				fmt.Fprintf(os.Stderr, "backfill %s: generate id: %v\n", userID, genErr)
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
				if isNationalIDConflict(execErr) {
					if attempt == maxAttemptsPerUser {
						fmt.Fprintf(os.Stderr, "backfill %s: exhausted %d attempts on national-ID collisions\n", userID, maxAttemptsPerUser)
					}
					continue // collision on a randomly generated number — retry with a new one
				}
				fmt.Fprintf(os.Stderr, "backfill %s: %v\n", userID, execErr)
				break
			}
			ok = true
			if tag.RowsAffected() > 0 {
				backfilled++
			} else {
				// Already has a national ID — set concurrently, by a prior
				// run, or by the driver themselves between the candidate
				// SELECT and this UPDATE. Not a failure; this WHERE clause is
				// exactly what makes re-running this command safe.
				alreadyDone++
			}
			break
		}
		if !ok {
			skipped++
		}
	}

	fmt.Printf("national-id-backfill: %d backfilled, %d already had one on file, %d skipped (see stderr) — %d candidate(s) total\n",
		backfilled, alreadyDone, skipped, len(userIDs))
}
