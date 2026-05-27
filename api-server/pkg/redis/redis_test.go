package redis_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	rkeys "github.com/workspace/ride-platform/pkg/redis"
)

func TestRedisKeyBuilders(t *testing.T) {
	k := rkeys.K

	assert.Equal(t, "driver:location:driver-1", k.DriverLocation("driver-1"))
	assert.Equal(t, "driver:location:driver-1:history", k.DriverLocationHistory("driver-1"))
	assert.Equal(t, "driver:driver-1:state", k.DriverState("driver-1"))
	assert.Equal(t, "driver:driver-1:active_ride", k.DriverActiveRide("driver-1"))
	assert.Equal(t, "drivers:geo:MOTO_BIKE", k.DriverGeoIndex("MOTO_BIKE"))
	assert.Equal(t, "matching:lock:driver-1", k.MatchingLock("driver-1"))
	assert.Equal(t, "ride:ride-1:pending_driver", k.RidePendingDriver("ride-1"))
	assert.Equal(t, "ride:ride-1:excluded_drivers", k.RideExcludedDrivers("ride-1"))
	assert.Equal(t, "ride:ride-1:state", k.RideState("ride-1"))
	assert.Equal(t, "customer:customer-1:active_ride", k.CustomerActiveRide("customer-1"))
	assert.Equal(t, "ride:ride-1:negotiation", k.RideNegotiation("ride-1"))
	assert.Equal(t, "ride:ride-1:offers:CUSTOMER", k.RideOfferCount("ride-1", "CUSTOMER"))
	assert.Equal(t, "driver:penalties:driver-1:daily_declines", k.DriverDailyDeclines("driver-1"))
	assert.Equal(t, "driver:penalties:driver-1:offline_at", k.DriverOfflineAt("driver-1"))
	assert.Equal(t, "customer:cancels:customer-1:daily", k.CustomerDailyCancel("customer-1"))
	assert.Equal(t, "session:user-1:jti-1", k.Session("user-1", "jti-1"))
	assert.Equal(t, "ratelimit:otp:+250780000000", k.OTPRateLimit("+250780000000"))
	assert.Equal(t, "route:origin:dest:MOTO_BIKE", k.RouteCache("origin:dest:MOTO_BIKE"))
	assert.Equal(t, "suggestions:user-1", k.UserSuggestions("user-1"))
	assert.Equal(t, "landmark:suggestions", k.LandmarkSuggestions())
	assert.Equal(t, "driver:gps_anomalies:driver-1:session_count", k.GPSAnomalyCount("driver-1"))
	assert.Equal(t, "analytics:events", k.AnalyticsStream())
}
