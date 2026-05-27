package geo_test

import (
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/geo"
)

func TestPointValidate(t *testing.T) {
	assert.NoError(t, (geo.Point{Lat: -1.9441, Lng: 30.0619}).Validate())
	assert.ErrorIs(t, (geo.Point{Lat: -91, Lng: 30.0619}).Validate(), apperrors.ErrGPSCoords)
	assert.ErrorIs(t, (geo.Point{Lat: -1.9441, Lng: 181}).Validate(), apperrors.ErrGPSCoords)
}

func TestPointWKT(t *testing.T) {
	assert.Equal(t, "SRID=4326;POINT(30.061900 -1.944100)", (geo.Point{Lat: -1.9441, Lng: 30.0619}).WKT())
}

func TestDistanceAndSpeed(t *testing.T) {
	cbd := geo.Point{Lat: -1.9441, Lng: 30.0619}
	airport := geo.Point{Lat: -1.9686, Lng: 30.1395}

	distance := geo.DistanceKM(cbd, airport)
	assert.True(t, distance > 8 && distance < 10, "expected Kigali CBD to airport distance around 9km, got %f", distance)
	assert.Equal(t, 0.0, geo.SpeedKMH(cbd, airport, 0))
	assert.True(t, math.Abs(geo.SpeedKMH(cbd, airport, 1800)-(distance*2)) < 0.001)
}

func TestAppErrorComparisonFromGeo(t *testing.T) {
	err := (geo.Point{Lat: 100, Lng: 30}).Validate()
	assert.True(t, errors.Is(err, apperrors.ErrGPSCoords))
}
