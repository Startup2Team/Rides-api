// Command registration-bonus-backfill makes whole every driver who was granted
// the registration bonus (DB-4) before the fix landed: ApproveDriver used to
// write the "30 free rides" bonus only into the legacy bonus_grants table
// (keyed on users.id), which the go-online/accept credit gates and
// GET /credits + /entitlements never read (those only read the v4 ledger,
// driver_entitlements, keyed on driver_profiles.id) — so the bonus was shown
// but could never be spent.
//
// This is a ONE-OFF, IDEMPOTENT backfill: it finds every REGISTRATION
// bonus_grants row that has no matching ride_credit_ledger entry (keyed
// "registration:<driver_profiles.id>", the same idempotency key the now-fixed
// ApproveDriver path uses) and grants it via the exact same
// packages.LedgerService.GrantRegistrationBonus call the live approval flow
// now uses — so re-running this command, or a driver being re-approved later,
// can never double-grant.
//
// DEFAULT MODE IS DRY-RUN. It only prints what it would do. Nothing is written
// unless -apply is passed. This command is DELIBERATELY NOT run by this task —
// applying it against any real database (staging or prod) needs its own
// explicit approval, per Rides' infrastructure-safety rules, after review of:
//
//  1. Whether to search for a real staging/prod schema question first: does the
//     driver's approved-but-never-spendable bonus grant still make sense to
//     honor for drivers who have since been suspended/deleted, or whose vehicle
//     type has since changed? This script grants against the vehicle_type_id
//     recorded on the ORIGINAL bonus_grants row and skips profiles that no
//     longer exist (driver_profiles was deleted) — see the JOIN below.
//  2. Expiry: the original grant's expires_at is typically already in the past
//     (bonus_grants.expires_at was ~30 days after the (unspendable) grant), so
//     honoring it would make this backfill a no-op once SweepExpired runs.
//     GrantRegistrationBonus always sets a FRESH 30-day window from the moment
//     it runs — i.e. this backfill deliberately gives drivers a new,
//     actually-usable 30-day window as the "make whole" remedy, not the
//     original (already-lapsed) one. That's a product/trust call, not a
//     unilateral engineering one — confirm before -apply.
//
// Usage:
//
//	go run ./cmd/registration-bonus-backfill            # dry-run (default): lists what would change
//	go run ./cmd/registration-bonus-backfill -apply      # actually grants (idempotent, safe to re-run)
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/rs/zerolog"

	"github.com/workspace/ride-platform/config"
	"github.com/workspace/ride-platform/internal/packages"
	pgpkg "github.com/workspace/ride-platform/pkg/postgres"
)

type unspendableGrant struct {
	profileID     string // driver_profiles.id
	userID        string // users.id (driver_profiles.user_id)
	vehicleTypeID string
	vehicleCode   string
	bonusRides    int
	grantedAt     time.Time
}

func main() {
	apply := flag.Bool("apply", false, "actually write the backfill grants (default: dry-run, prints only)")
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

	// Every REGISTRATION bonus_grants row whose driver still has a
	// driver_profiles row, and for whom no ledger entry exists under the
	// idempotency key the fixed ApproveDriver path now uses. This is the exact
	// unspendable-promise set described in DB-4.
	rows, err := db.Query(ctx, `
		SELECT dp.id, dp.user_id, bg.vehicle_type_id, vt.code, bg.bonus_rides, bg.granted_at
		FROM bonus_grants bg
		JOIN bonus_tiers bt      ON bt.id = bg.tier_id AND bt.trigger_type = 'REGISTRATION'
		JOIN driver_profiles dp  ON dp.user_id = bg.driver_id
		JOIN vehicle_types vt    ON vt.id = bg.vehicle_type_id
		LEFT JOIN ride_credit_ledger rcl
		       ON rcl.driver_id = dp.id
		      AND rcl.idempotency_key = 'registration:' || dp.id::text
		WHERE rcl.id IS NULL
		ORDER BY bg.granted_at ASC`)
	if err != nil {
		fmt.Fprintln(os.Stderr, "query unspendable grants:", err)
		os.Exit(1)
	}
	defer rows.Close()

	var grants []unspendableGrant
	for rows.Next() {
		var g unspendableGrant
		if err := rows.Scan(&g.profileID, &g.userID, &g.vehicleTypeID, &g.vehicleCode, &g.bonusRides, &g.grantedAt); err != nil {
			fmt.Fprintln(os.Stderr, "scan:", err)
			os.Exit(1)
		}
		grants = append(grants, g)
	}
	if err := rows.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "rows:", err)
		os.Exit(1)
	}

	fmt.Printf("registration-bonus-backfill: found %d driver(s) with an unspendable registration bonus\n", len(grants))
	if len(grants) == 0 {
		return
	}
	for _, g := range grants {
		fmt.Printf("  profile=%s user=%s vehicle=%s bonus_rides=%d originally_granted=%s\n",
			g.profileID, g.userID, g.vehicleCode, g.bonusRides, g.grantedAt.Format(time.RFC3339))
	}

	if !*apply {
		fmt.Println("\nDRY RUN — nothing written. Re-run with -apply to grant these onto the v4 ledger.")
		return
	}

	pkgRepo := packages.NewRepository(db)
	ledgerSvc := packages.NewLedgerService(pkgRepo, zerolog.Nop())

	var granted, failed int
	for _, g := range grants {
		if err := ledgerSvc.GrantRegistrationBonus(ctx, g.userID, g.vehicleTypeID, g.bonusRides); err != nil {
			fmt.Fprintf(os.Stderr, "grant failed for profile=%s: %v\n", g.profileID, err)
			failed++
			continue
		}
		granted++
	}
	fmt.Printf("\nregistration-bonus-backfill: granted %d, failed %d (idempotent — safe to re-run for the failures)\n", granted, failed)
}
