//go:build integration

package dbit

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/config"
	"github.com/workspace/ride-platform/internal/auth"
	"github.com/workspace/ride-platform/internal/ride"
	"github.com/workspace/ride-platform/internal/tracking"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/geo"
	rkeys "github.com/workspace/ride-platform/pkg/redis"
)

// End-to-end coverage for Service.UpdateCustomerLocation (customer
// live-location publish contract) against a real Postgres-backed
// ride.Repository: ownership enforcement, the active-state gate, and the
// success path's Redis cache + driver fan-out.
//
// Uses a real repo (real SQL, real status guards) but a miniredis-backed hub
// for the WS side, mirroring internal/tracking/hub_test.go.

// createTestRide drives a ride through CreateRide -> AssignDriver -> ...
// -> targetStatus using the real repository's own transition guards, so the
// fixture is exactly as valid as a ride reaching that status in production.
func createTestRide(t *testing.T, ctx context.Context, repo *ride.Repository, customerID, driverProfileID string, targetStatus ride.Status) string {
	t.Helper()

	pickup := geo.Point{Lat: -1.9441, Lng: 30.0619}
	dest := geo.Point{Lat: -1.9355, Lng: 30.1127}
	r, err := repo.CreateRide(ctx, customerID, "MOTO_BIKE", "Kigali CBD", "Kimironko", pickup, dest, nil, nil, nil)
	require.NoError(t, err)

	if targetStatus == ride.StatusSearching {
		return r.ID
	}

	require.NoError(t, repo.AssignDriver(ctx, r.ID, driverProfileID))
	require.NoError(t, repo.Transition(ctx, r.ID, ride.StatusSearching, ride.StatusMatched))
	if targetStatus == ride.StatusMatched {
		return r.ID
	}

	require.NoError(t, repo.Transition(ctx, r.ID, ride.StatusMatched, ride.StatusNegotiating))
	if targetStatus == ride.StatusNegotiating {
		return r.ID
	}

	require.NoError(t, repo.Transition(ctx, r.ID, ride.StatusNegotiating, ride.StatusConfirmed))
	if targetStatus == ride.StatusConfirmed {
		return r.ID
	}

	require.NoError(t, repo.Transition(ctx, r.ID, ride.StatusConfirmed, ride.StatusDriverEnRoute))
	if targetStatus == ride.StatusDriverEnRoute {
		return r.ID
	}

	require.NoError(t, repo.Transition(ctx, r.ID, ride.StatusDriverEnRoute, ride.StatusDriverArrived))
	if targetStatus == ride.StatusDriverArrived {
		return r.ID
	}

	require.NoError(t, repo.Transition(ctx, r.ID, ride.StatusDriverArrived, ride.StatusInProgress))
	if targetStatus == ride.StatusInProgress {
		return r.ID
	}

	require.NoError(t, repo.Transition(ctx, r.ID, ride.StatusInProgress, ride.StatusCompleted))
	return r.ID
}

func newTestRideService(t *testing.T) (*ride.Service, *ride.Repository, *goredis.Client, *tracking.Hub) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })

	hub := tracking.NewHub(rdb, zerolog.Nop())
	t.Cleanup(func() { hub.Close() })

	repo := ride.NewRepository(pool)
	svc := ride.NewService(repo, rdb, nil, nil, hub, &config.Config{}, zerolog.Nop())
	return svc, repo, rdb, hub
}

func TestUpdateCustomerLocation_NonOwnerRejected(t *testing.T) {
	ctx := context.Background()
	svc, repo, _, _ := newTestRideService(t)
	authRepo := auth.NewRepository(pool)

	customer, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-cust", "android", nil, nil)
	require.NoError(t, err)
	stranger, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-stranger", "android", nil, nil)
	require.NoError(t, err)
	driverUser, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-driver", "android", nil, nil)
	require.NoError(t, err)
	driverProfileID := insertDriverProfile(t, ctx, driverUser.ID, "MOTO_BIKE")

	rideID := createTestRide(t, ctx, repo, customer.ID, driverProfileID, ride.StatusConfirmed)

	err = svc.UpdateCustomerLocation(ctx, rideID, stranger.ID, ride.CustomerLocationUpdate{Lat: -1.94, Lng: 30.06})
	require.Error(t, err)
	require.ErrorIs(t, err, apperrors.ErrRideNotFound, "a ride owned by someone else must resolve as not found, not forbidden")
}

func TestUpdateCustomerLocation_CompletedRideRejected(t *testing.T) {
	ctx := context.Background()
	svc, repo, _, _ := newTestRideService(t)
	authRepo := auth.NewRepository(pool)

	customer, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-cust", "android", nil, nil)
	require.NoError(t, err)
	driverUser, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-driver", "android", nil, nil)
	require.NoError(t, err)
	driverProfileID := insertDriverProfile(t, ctx, driverUser.ID, "MOTO_BIKE")

	rideID := createTestRide(t, ctx, repo, customer.ID, driverProfileID, ride.StatusCompleted)

	err = svc.UpdateCustomerLocation(ctx, rideID, customer.ID, ride.CustomerLocationUpdate{Lat: -1.94, Lng: 30.06})
	require.Error(t, err)
	require.ErrorIs(t, err, apperrors.ErrRideNotActive)
}

func TestUpdateCustomerLocation_ActiveRide_CachesAndFansOutToDriver(t *testing.T) {
	ctx := context.Background()
	svc, repo, rdb, hub := newTestRideService(t)
	authRepo := auth.NewRepository(pool)

	customer, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-cust", "android", nil, nil)
	require.NoError(t, err)
	driverUser, err := authRepo.CreateUser(ctx, uniquePhone(), "dev-driver", "android", nil, nil)
	require.NoError(t, err)
	driverProfileID := insertDriverProfile(t, ctx, driverUser.ID, "MOTO_BIKE")

	rideID := createTestRide(t, ctx, repo, customer.ID, driverProfileID, ride.StatusInProgress)

	// Subscribe the driver's WS client to the hub before publishing, mirroring
	// hub_test.go's pattern.
	driverSend := make(chan tracking.Message, 4)
	hub.RegisterDriver(driverProfileID, &tracking.Client{
		UserID: driverProfileID,
		Role:   "DRIVER",
		Send:   driverSend,
	})
	time.Sleep(100 * time.Millisecond) // let PSubscribe activate

	err = svc.UpdateCustomerLocation(ctx, rideID, customer.ID, ride.CustomerLocationUpdate{Lat: -1.9441, Lng: 30.0619})
	require.NoError(t, err)

	select {
	case msg := <-driverSend:
		require.Equal(t, "customer_location", msg.Type)
		require.Equal(t, rideID, msg.RideID)
		require.InDelta(t, -1.9441, msg.Payload["lat"], 1e-9)
		require.InDelta(t, 30.0619, msg.Payload["lng"], 1e-9)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for customer_location fan-out to the driver")
	}

	cached, err := rdb.Get(ctx, rkeys.K.RideCustomerLocation(rideID)).Result()
	require.NoError(t, err)
	require.Contains(t, cached, "-1.9441")
}
