//go:build integration

// Package dbit holds DATABASE-BACKED integration tests: they run the real
// migrations against a real Postgres and exercise the repository/service layer
// with a real *pgxpool.Pool (real SQL, real constraints, real transactions).
//
// These are the tests the unit suite can't be: `ledger` unit tests use a fake
// Querier and `wallet` mutations use serialisable transactions that only a real
// database validates. They compile ONLY under `-tags=integration` and run in
// CI's "Integration" job (Postgres + Redis service containers). Without
// TEST_DATABASE_URL set they skip cleanly, so a tagged local run never fails
// just because no database is around.
package dbit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pool is the shared connection to the test database, ready after TestMain.
var pool *pgxpool.Pool

func TestMain(m *testing.M) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		fmt.Fprintln(os.Stderr, "TEST_DATABASE_URL not set — skipping db integration tests")
		os.Exit(0)
	}

	if err := runMigrations(dbURL); err != nil {
		fmt.Fprintln(os.Stderr, "migrations failed:", err)
		os.Exit(1)
	}

	p, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect failed:", err)
		os.Exit(1)
	}
	pool = p

	code := m.Run()
	pool.Close()
	os.Exit(code)
}

// runMigrations applies the real migration set (same source golang-migrate uses
// on boot in cmd/server) so tests hit the exact schema production runs.
func runMigrations(dbURL string) error {
	migrateURL := strings.NewReplacer(
		"postgresql://", "pgx5://",
		"postgres://", "pgx5://",
	).Replace(dbURL)

	// Locate api-server/migrations relative to this source file, so the path is
	// correct regardless of the working directory the test binary runs from.
	_, thisFile, _, _ := runtime.Caller(0)
	migDir := filepath.Join(filepath.Dir(thisFile), "..", "..", "migrations")

	m, err := migrate.New("file://"+migDir, migrateURL)
	if err != nil {
		return fmt.Errorf("migrate.New: %w", err)
	}
	defer m.Close()
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("migrate.Up: %w", err)
	}
	return nil
}

// ── unique-value helpers: keep tests independent + rerunnable on a dirty DB ──

var phoneCounter = time.Now().UnixNano() % 9_000_000

func uniquePhone() string {
	n := atomic.AddInt64(&phoneCounter, 1) % 9_000_000
	return fmt.Sprintf("+25078%07d", n)
}

var keyCounter int64

func uniqueKey(prefix string) string {
	n := atomic.AddInt64(&keyCounter, 1)
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), n)
}
