// seed-admin creates the first Super Admin account in the database.
// Run once after starting the server for the first time.
//
// Usage:
//   go run ./cmd/seed-admin --email admin@rides.com --password Admin1234!
//
// The password must be at least 8 characters.
// If the email already exists, the command exits cleanly without error.

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"golang.org/x/crypto/bcrypt"
)

func main() {
	email := flag.String("email", "admin@rides.com", "Admin email address")
	name := flag.String("name", "Super Admin", "Admin display name")
	password := flag.String("password", "", "Admin password (min 8 chars)")
	flag.Parse()

	if len(*password) < 8 {
		fmt.Fprintln(os.Stderr, "error: --password must be at least 8 characters")
		os.Exit(1)
	}

	_ = godotenv.Load()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		fmt.Fprintln(os.Stderr, "error: DATABASE_URL environment variable is not set")
		os.Exit(1)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot connect to database: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Get the Super Admin role ID
	var roleID string
	err = pool.QueryRow(ctx,
		`SELECT id FROM admin_roles WHERE name = 'Super Admin' LIMIT 1`,
	).Scan(&roleID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: Super Admin role not found — have migrations run? (%v)\n", err)
		os.Exit(1)
	}

	// Check if the email already exists
	var existing int
	_ = pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM admin_accounts WHERE email = $1`, strings.ToLower(*email),
	).Scan(&existing)
	if existing > 0 {
		fmt.Printf("Admin account %s already exists — nothing to do.\n", *email)
		os.Exit(0)
	}

	// Hash the password
	hash, err := bcrypt.GenerateFromPassword([]byte(*password), bcrypt.DefaultCost)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: bcrypt failed: %v\n", err)
		os.Exit(1)
	}

	// Insert the account
	var id string
	err = pool.QueryRow(ctx, `
		INSERT INTO admin_accounts (name, email, password_hash, role_id, status)
		VALUES ($1, $2, $3, $4, 'ACTIVE')
		RETURNING id
	`, *name, strings.ToLower(*email), string(hash), roleID).Scan(&id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: insert failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✓ Admin account created\n")
	fmt.Printf("  ID:    %s\n", id)
	fmt.Printf("  Email: %s\n", strings.ToLower(*email))
	fmt.Printf("  Role:  Super Admin\n")
	fmt.Printf("  2FA:   disabled (enable it after first login)\n")
}
