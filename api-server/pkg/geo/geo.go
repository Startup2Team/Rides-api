package geo

import (
	"fmt"
	"math"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

const earthRadiusKM = 6371.0

// Point represents a geographic coordinate.
type Point struct {
	Lat float64
	Lng float64
}

// Validate checks that coordinates are within valid WGS84 ranges.
func (p Point) Validate() error {
	if p.Lat < -90 || p.Lat > 90 {
		return apperrors.ErrGPSCoords
	}
	if p.Lng < -180 || p.Lng > 180 {
		return apperrors.ErrGPSCoords
	}
	return nil
}

// WKT returns the Well-Known Text representation for PostGIS.
// e.g. "SRID=4326;POINT(30.1234 -1.9441)"
func (p Point) WKT() string {
	return fmt.Sprintf("SRID=4326;POINT(%f %f)", p.Lng, p.Lat)
}

// DistanceKM computes the Haversine distance between two points in kilometres.
func DistanceKM(a, b Point) float64 {
	dLat := toRad(b.Lat - a.Lat)
	dLng := toRad(b.Lng - a.Lng)

	sin2Lat := math.Sin(dLat/2) * math.Sin(dLat/2)
	sin2Lng := math.Sin(dLng/2) * math.Sin(dLng/2)

	h := sin2Lat + math.Cos(toRad(a.Lat))*math.Cos(toRad(b.Lat))*sin2Lng
	c := 2 * math.Atan2(math.Sqrt(h), math.Sqrt(1-h))

	return earthRadiusKM * c
}

// SpeedKMH computes the speed in km/h between two points over a duration in seconds.
func SpeedKMH(from, to Point, durationSeconds float64) float64 {
	if durationSeconds <= 0 {
		return 0
	}
	distKM := DistanceKM(from, to)
	return distKM / (durationSeconds / 3600)
}

func toRad(deg float64) float64 {
	return deg * math.Pi / 180
}
