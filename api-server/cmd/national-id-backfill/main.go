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
// The actual logic lives in internal/nationalidbackfill (package main can't
// be imported by tests, so it moved there to be reachable by test/dbit — see
// that package's doc comment). This file is just the CLI wrapper, mirroring
// cmd/ledger-backfill's thin-main/real-logic-in-internal pattern.
//
//	go run ./cmd/national-id-backfill            # dry run (default)
//	go run ./cmd/national-id-backfill -apply      # actually write
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/workspace/ride-platform/config"
	"github.com/workspace/ride-platform/internal/nationalidbackfill"
	pgpkg "github.com/workspace/ride-platform/pkg/postgres"
)

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

	result, err := nationalidbackfill.Run(ctx, db, *apply, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if result.Candidates == 0 {
		fmt.Println("national-id-backfill: no drivers need a placeholder national ID — nothing to do")
		return
	}

	if !*apply {
		fmt.Printf("national-id-backfill: DRY RUN — %d driver(s) would get a placeholder RW national ID. Re-run with -apply to write.\n", result.Candidates)
		return
	}

	fmt.Printf("national-id-backfill: %d backfilled, %d already had one on file, %d skipped (see stderr) — %d candidate(s) total\n",
		result.Backfilled, result.AlreadyDone, result.Skipped, result.Candidates)
}
