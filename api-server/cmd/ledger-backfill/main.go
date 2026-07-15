// Command ledger-backfill posts a double-entry journal entry for every
// historical PAID package purchase, so the general ledger represents sales that
// predate the ledger's introduction. It is idempotent (keyed by purchase id via
// ledger.Post's ON CONFLICT), so it is safe to run repeatedly and safe to run
// alongside live posting — already-posted sales are skipped.
//
//	go run ./cmd/ledger-backfill
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/workspace/ride-platform/config"
	"github.com/workspace/ride-platform/internal/ledger"
	pgpkg "github.com/workspace/ride-platform/pkg/postgres"
)

func main() {
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

	led := ledger.NewService(db)

	rows, err := db.Query(ctx, `
		SELECT id::text, price_paid_rwf, COALESCE(payment_provider, ''),
		       COALESCE(paid_at, created_at), payment_ref
		FROM package_purchases
		WHERE status = 'PAID' AND price_paid_rwf > 0
		ORDER BY created_at ASC`)
	if err != nil {
		fmt.Fprintln(os.Stderr, "query purchases:", err)
		os.Exit(1)
	}
	defer rows.Close()

	type sale struct {
		id       string
		price    int64
		provider string
		paidAt   time.Time
		ref      string
	}
	var sales []sale
	for rows.Next() {
		var s sale
		if err := rows.Scan(&s.id, &s.price, &s.provider, &s.paidAt, &s.ref); err != nil {
			fmt.Fprintln(os.Stderr, "scan:", err)
			os.Exit(1)
		}
		sales = append(sales, s)
	}
	if err := rows.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "rows:", err)
		os.Exit(1)
	}

	var posted int
	for _, s := range sales {
		cashAcct := ledger.AcctCashManual
		switch strings.ToLower(s.provider) {
		case "mtn", "airtel", "momo":
			cashAcct = ledger.AcctCashMoMo
		}
		sid := s.id
		if err := led.Post(ctx, db, ledger.Entry{
			Date:           s.paidAt,
			Description:    "Package sale " + s.ref + " (backfill)",
			SourceType:     "package_purchase",
			SourceID:       &sid,
			IdempotencyKey: "purchase_paid:" + s.id,
			CreatedBy:      "backfill",
			Lines: []ledger.Line{
				{Account: cashAcct, Debit: s.price, Memo: "package sale"},
				{Account: ledger.AcctPackageRevenue, Credit: s.price, Memo: "package sale"},
			},
		}); err != nil {
			fmt.Fprintf(os.Stderr, "post %s: %v\n", s.id, err)
			os.Exit(1)
		}
		posted++
	}

	fmt.Printf("ledger-backfill: processed %d PAID purchases (idempotent; already-posted skipped)\n", posted)
}
