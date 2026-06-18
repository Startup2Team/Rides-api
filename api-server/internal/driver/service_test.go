package driver_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/workspace/ride-platform/internal/driver"
)

func TestCalculateDriverPayoutNoCommission(t *testing.T) {
	// Package-based model: the driver keeps 100% of the agreed fare.
	assert.Equal(t, 1.0, driver.DriverPayoutRate)
	assert.Equal(t, 10000.0, driver.CalculateDriverPayout(10000))
	assert.Equal(t, 0.0, driver.CalculateDriverPayout(0))
}
