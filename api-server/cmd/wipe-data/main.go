// wipe-data clears demo/seed operational data from PostgreSQL and Redis.
//
// Usage:
//
//	go run ./cmd/wipe-data
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"

	"github.com/workspace/ride-platform/pkg/redis"
)

const wipeSQL = `
TRUNCATE TABLE
    ticket_messages,
    support_tickets,
    inbox_messages,
    incident_events,
    safety_incidents,
    reports,
    scheduled_reports,
    negotiation_rounds,
    ride_events,
    ride_disputes,
    payments,
    wallet_transactions,
    wallets,
    ratings,
    notifications,
    analytics_events,
    driver_documents,
    driver_locations,
    driver_ride_credits,
    driver_sessions,
    driver_vehicles,
    gps_anomalies,
    driver_profiles,
    customer_profiles,
    saved_locations,
    route_cache,
    otp_verifications,
    device_sessions,
    zone_demand_stats,
    rides,
    hot_zones,
    landmarks,
    ride_packages,
    users
RESTART IDENTITY CASCADE;
`

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL is required")
		os.Exit(1)
	}

	db, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db connect: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	if _, err := db.Exec(ctx, wipeSQL); err != nil {
		fmt.Fprintf(os.Stderr, "wipe postgres: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("PostgreSQL: operational and seed demo data cleared")

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		fmt.Println("REDIS_URL not set — skipped Redis cleanup")
		return
	}

	rdb, err := redis.New(ctx, redisURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "redis connect: %v\n", err)
		os.Exit(1)
	}
	defer rdb.Close()

	patterns := []string{
		"driver:*",
		"drivers:geo:*",
		"ride:*",
		"matching:*",
		"landmarks:suggestions",
	}
	for _, pattern := range patterns {
		if err := deleteByPattern(ctx, rdb, pattern); err != nil {
			fmt.Fprintf(os.Stderr, "redis %s: %v\n", pattern, err)
			os.Exit(1)
		}
	}
	fmt.Println("Redis: driver/ride cache keys cleared")
}

func deleteByPattern(ctx context.Context, rdb *goredis.Client, pattern string) error {
	var cursor uint64
	for {
		keys, next, err := rdb.Scan(ctx, cursor, pattern, 200).Result()
		if err != nil {
			return err
		}
		if len(keys) > 0 {
			if err := rdb.Del(ctx, keys...).Err(); err != nil {
				return err
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return nil
}
