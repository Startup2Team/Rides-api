package ride_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrations_AddDriverArrivedAt(t *testing.T) {
	upPath := filepath.Join("..", "..", "migrations", "016_add_driver_arrived_at_to_rides.up.sql")
	downPath := filepath.Join("..", "..", "migrations", "016_add_driver_arrived_at_to_rides.down.sql")

	up, err := os.ReadFile(upPath)
	require.NoError(t, err)
	down, err := os.ReadFile(downPath)
	require.NoError(t, err)

	assert.Contains(t, strings.ToLower(string(up)), "driver_arrived_at")
	assert.Contains(t, strings.ToLower(string(down)), "driver_arrived_at")
}

// TestMigrations_AddNegotiationDeadlineToRides locks in the durable
// negotiation-timeout backstop's schema change (migration 090): the up
// migration must add the column, and the down migration must drop it — both
// proven reversible against a real Postgres in
// test/dbit (integration-tagged; requires TEST_DATABASE_URL).
func TestMigrations_AddNegotiationDeadlineToRides(t *testing.T) {
	upPath := filepath.Join("..", "..", "migrations", "090_add_negotiation_deadline_to_rides.up.sql")
	downPath := filepath.Join("..", "..", "migrations", "090_add_negotiation_deadline_to_rides.down.sql")

	up, err := os.ReadFile(upPath)
	require.NoError(t, err)
	down, err := os.ReadFile(downPath)
	require.NoError(t, err)

	assert.Contains(t, strings.ToLower(string(up)), "negotiation_deadline_at")
	assert.Contains(t, strings.ToLower(string(up)), "add column")
	assert.Contains(t, strings.ToLower(string(down)), "negotiation_deadline_at")
	assert.Contains(t, strings.ToLower(string(down)), "drop column")
}
