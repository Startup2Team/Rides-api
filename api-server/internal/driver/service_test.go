package driver_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/workspace/ride-platform/internal/driver"
)

func TestCalculateDriverPayoutUsesPlatformCommission(t *testing.T) {
	assert.Equal(t, 0.85, driver.DriverPayoutRate)
	assert.Equal(t, 8500.0, driver.CalculateDriverPayout(10000))
	assert.Equal(t, 0.0, driver.CalculateDriverPayout(0))
}
