package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"time"

	goredis "github.com/redis/go-redis/v9"

	rkeys "github.com/workspace/ride-platform/pkg/redis"
	"github.com/workspace/ride-platform/pkg/respond"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
)

// SeedHandler is a standalone handler registered only in non-production environments.
// It populates 100 realistic Kigali drivers in both PostgreSQL and Redis.
type SeedHandler struct {
	db  *pgxpool.Pool
	rdb *goredis.Client
	log zerolog.Logger
}

func NewSeedHandler(db *pgxpool.Pool, rdb *goredis.Client, log zerolog.Logger) *SeedHandler {
	return &SeedHandler{db: db, rdb: rdb, log: log}
}

// POST /api/v1/admin/dev/seed-drivers
// Idempotent: skips phones that already exist.
func (h *SeedHandler) SeedDrivers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	created, skipped, err := seedDrivers(ctx, h.db, h.rdb, h.log)
	if err != nil {
		h.log.Error().Err(err).Msg("seed: failed")
		respond.ErrorMsg(w, http.StatusInternalServerError, "SEED_FAILED", err.Error())
		return
	}
	respond.OK(w, map[string]interface{}{
		"created": created,
		"skipped": skipped,
		"message": fmt.Sprintf("seed complete: %d created, %d skipped (already existed)", created, skipped),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Seed data
// ─────────────────────────────────────────────────────────────────────────────

// kigaliZone describes a Kigali neighbourhood with a center point and spread radius.
type kigaliZone struct {
	name   string
	lat    float64
	lng    float64
	spread float64 // degrees (~111 km per degree)
}

var kigaliZones = []kigaliZone{
	{name: "CBD / Nyarugenge", lat: -1.9441, lng: 30.0619, spread: 0.012},
	{name: "Kicukiro / Gikondo", lat: -1.9735, lng: 30.0680, spread: 0.015},
	{name: "Kimironko", lat: -1.9305, lng: 30.1000, spread: 0.012},
	{name: "Remera", lat: -1.9464, lng: 30.0923, spread: 0.010},
	{name: "Kacyiru", lat: -1.9296, lng: 30.0722, spread: 0.010},
	{name: "Nyabugogo", lat: -1.9280, lng: 30.0575, spread: 0.008},
	{name: "Kanombe", lat: -1.9714, lng: 30.1154, spread: 0.012},
	{name: "Gisozi", lat: -1.9292, lng: 30.0710, spread: 0.010},
	{name: "Biryogo", lat: -1.9500, lng: 30.0600, spread: 0.008},
	{name: "Gasabo", lat: -1.9103, lng: 30.0939, spread: 0.015},
}

// vehicleDist defines how many drivers per vehicle type (total = 100).
type vehicleAlloc struct {
	code  string
	count int
}

var vehicleAllocs = []vehicleAlloc{
	{code: "MOTO_BIKE", count: 58},
	{code: "CAB_TAXI", count: 28},
	{code: "LIGHT_HILUX", count: 10},
	{code: "HEAVY_FUSO", count: 4},
}

var rwandanNames = []string{
	"Kalisa Jean", "Uwimana Marie", "Nkurunziza Peter", "Mukamana Grace", "Habimana Eric",
	"Ingabire Alice", "Ndayishimiye Paul", "Uwase Claudine", "Bizimana John", "Iradukunda Rose",
	"Gashumba Ivan", "Mukagatare Diana", "Nzeyimana Jules", "Umwali Joselyne", "Hakizimana Bruno",
	"Mukandayisenga Lea", "Nsengimana Alain", "Uwingabire Cecile", "Tuyisenge Robert", "Murerwa Olive",
	"Ntirenganya Claude", "Mukabagwiza Sandra", "Habimana Felix", "Uwimbabazi Patricia", "Bizimungu Daniel",
	"Mukandoli Christine", "Niyonsaba Emmanuel", "Mukabutera Sylvie", "Hakizimana Michael", "Ineza Beatrice",
	"Gatete Samuel", "Mukabera Joyce", "Nshimiyimana Vincent", "Uwera Esperance", "Habimana Prosper",
	"Mukagasana Therese", "Nduwayezu Innocent", "Uwimana Solange", "Gatera Innocent", "Mukamuziga Florence",
	"Nzabonimpa Martin", "Uwase Fortunee", "Habimana Celestin", "Mukangango Venantie", "Ntirugirimbabazi Leon",
	"Uwimbabazi Josephine", "Nkurunziza Desire", "Mukarurinda Gaudence", "Bizimana Alphonse", "Iragena Chantal",
	"Gasana Theophile", "Mukandekezi Immaculee", "Niyomugabo Justin", "Umubyeyi Annonciate", "Hakizimana Edouard",
	"Mukamana Chantale", "Nshimiyimana Augustin", "Uwimana Laetitia", "Rwema Gervais", "Mukabashema Antoinette",
	"Nzabonimana Fabrice", "Uwumugambi Gloriose", "Habimana Cleophas", "Mukamurenzi Emerthe", "Ntirabampa Rene",
	"Uwera Valerie", "Gasinzigwa Methode", "Mukabutera Odette", "Nkurunziza Callixte", "Uwimbabazi Gorette",
	"Bizimana Evaste", "Mukamugema Speciose", "Ndayambaje Juvenal", "Umwizabihe Gertrude", "Hakizimana Norbert",
	"Mukamuyange Theotime", "Niyomugabo Sosthene", "Uwamariya Devota", "Ntakirutimana Innocent", "Mukamana Vestine",
	"Ngabonziza Telesphore", "Uwera Mediatrice", "Habimana Zacharie", "Mukantwali Beatrice", "Nzabonimpa Ildephonse",
	"Uwimbabazi Modeste", "Gatera Sylvestre", "Mukarurinda Scholastique", "Nizeyimana Leonard", "Uwitonze Adolphine",
	"Hakizimana Anastase", "Mukamana Benedicte", "Nduwimana Celestin", "Uwimpuhwe Assumpta", "Bizimana Melchior",
	"Mukabera Alphonsine", "Ntirabampa Juvite", "Uwimana Theotime", "Gasana Donatien", "Mukagatare Gerardine",
}

// seedDrivers inserts 100 driver records and populates Redis GEO.
func seedDrivers(ctx context.Context, db *pgxpool.Pool, rdb *goredis.Client, log zerolog.Logger) (created, skipped int, err error) {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	// Build the flat list: one entry per driver (vehicle type × count, spread across zones)
	type driverSpec struct {
		phone       string
		name        string
		vehicleType string
		plate       string
		license     string
		lat         float64
		lng         float64
		zone        string
	}

	specs := make([]driverSpec, 0, 100)
	idx := 0
	for _, va := range vehicleAllocs {
		for i := 0; i < va.count; i++ {
			zone := kigaliZones[idx%len(kigaliZones)]
			// jitter within the zone spread
			lat := zone.lat + (rng.Float64()*2-1)*zone.spread
			lng := zone.lng + (rng.Float64()*2-1)*zone.spread
			// round to 6 decimal places
			lat = math.Round(lat*1e6) / 1e6
			lng = math.Round(lng*1e6) / 1e6

			phone := fmt.Sprintf("+25078%07d", 1000000+idx)
			name := rwandanNames[idx%len(rwandanNames)]

			plate := fmt.Sprintf("R%s %03d %c",
				platePrefix(va.code),
				(idx%900)+100,
				rune('A'+idx%26),
			)
			license := fmt.Sprintf("DL-%07d", 1000000+idx)

			specs = append(specs, driverSpec{
				phone:       phone,
				name:        name,
				vehicleType: va.code,
				plate:       plate,
				license:     license,
				lat:         lat,
				lng:         lng,
				zone:        zone.name,
			})
			idx++
		}
	}

	for i, spec := range specs {
		// --- Insert user ---
		var userID string
		err := db.QueryRow(ctx, `
			INSERT INTO users (phone_number, full_name, role_state, device_id)
			VALUES ($1, $2, 'DRIVER_ACTIVE', $3)
			ON CONFLICT (phone_number) DO NOTHING
			RETURNING id
		`, spec.phone, spec.name, fmt.Sprintf("seed-device-%d", i)).Scan(&userID)

		if err != nil {
			// ON CONFLICT returned no row
			skipped++
			continue
		}

		// --- Insert driver profile ---
		var profileID string
		dob := time.Date(1985+rng.Intn(20), time.Month(1+rng.Intn(12)), 1+rng.Intn(28), 0, 0, 0, 0, time.UTC)
		momoProvider := "mtn"
		if i%3 == 0 {
			momoProvider = "airtel"
		}
		momoCode := fmt.Sprintf("25078%07d", 1000000+i)

		err = db.QueryRow(ctx, `
			INSERT INTO driver_profiles (
				user_id, transport_type, vehicle_plate, license_number, date_of_birth,
				city, province, district, sector, cell, village,
				momo_pay_code, momo_provider,
				approval_status, policy_accepted,
				is_online, total_rides, acceptance_rate, priority_tier
			) VALUES (
				$1, $2, $3, $4, $5,
				'Kigali', 'Kigali City', 'Nyarugenge', 'Nyarugenge', 'Biryogo', 'Biryogo I',
				$6, $7,
				'APPROVED', TRUE,
				TRUE, $8, 0.92, 1
			)
			RETURNING id
		`, userID, spec.vehicleType, spec.plate, spec.license, dob,
			momoCode, momoProvider,
			10+rng.Intn(200),
		).Scan(&profileID)

		if err != nil {
			log.Warn().Err(err).Str("phone", spec.phone).Msg("seed: driver profile insert failed")
			skipped++
			continue
		}

		// --- Insert driver location ---
		_, err = db.Exec(ctx, `
			INSERT INTO driver_locations (driver_id, location, speed_kmh, heading)
			VALUES ($1, ST_SetSRID(ST_MakePoint($2, $3), 4326)::geography, 0, 0)
			ON CONFLICT (driver_id) DO UPDATE
				SET location = EXCLUDED.location, updated_at = NOW()
		`, profileID, spec.lng, spec.lat)
		if err != nil {
			log.Warn().Err(err).Str("profile_id", profileID).Msg("seed: driver_location insert failed")
		}

		// --- Seed Redis GEO index ---
		geoKey := rkeys.K.DriverGeoIndex(spec.vehicleType)
		rdb.GeoAdd(ctx, geoKey, &goredis.GeoLocation{
			Name:      profileID,
			Longitude: spec.lng,
			Latitude:  spec.lat,
		})

		// Mark driver as AVAILABLE in Redis
		rdb.Set(ctx, rkeys.K.DriverState(profileID), "AVAILABLE", 0)

		// Store last known location in Redis (used by ride completion to re-add to GEO)
		locJSON, _ := json.Marshal(map[string]float64{"lat": spec.lat, "lng": spec.lng})
		rdb.Set(ctx, rkeys.K.DriverLocation(profileID), string(locJSON), 0)

		log.Info().
			Str("phone", spec.phone).
			Str("vehicle", spec.vehicleType).
			Str("zone", spec.zone).
			Msgf("seed: driver %d/%d created", i+1, len(specs))

		created++
	}

	return created, skipped, nil
}

func platePrefix(vehicleType string) string {
	switch vehicleType {
	case "MOTO_BIKE":
		return "AD"
	case "CAB_TAXI":
		return "AF"
	case "LIGHT_HILUX":
		return "AH"
	default:
		return "AJ"
	}
}
