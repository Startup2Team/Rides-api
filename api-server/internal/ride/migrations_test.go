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
