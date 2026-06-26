package fare

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

const pricingSelect = `
	id, vehicle_type_code, base_fare_rwf, base_distance_km,
	tier1_per_km_rwf, tier1_max_km, tier2_per_km_rwf,
	night_surcharge_pct, night_start_hour, night_end_hour,
	waiting_rwf_per_min, waiting_free_minutes, min_fare_rwf, cancellation_fee_rwf,
	is_active, effective_from
`

func scanConfig(scan func(dest ...any) error) (*Config, error) {
	cfg := &Config{}
	var effectiveFrom time.Time
	if err := scan(
		&cfg.ID, &cfg.VehicleTypeCode, &cfg.BaseFareRWF, &cfg.BaseDistanceKM,
		&cfg.Tier1PerKmRWF, &cfg.Tier1MaxKM, &cfg.Tier2PerKmRWF,
		&cfg.NightSurchargePct, &cfg.NightStartHour, &cfg.NightEndHour,
		&cfg.WaitingRWFPerMin, &cfg.WaitingFreeMinutes, &cfg.MinFareRWF, &cfg.CancellationFeeRWF,
		&cfg.IsActive, &effectiveFrom,
	); err != nil {
		return nil, err
	}
	cfg.EffectiveFrom = effectiveFrom.Format(time.RFC3339)
	return cfg, nil
}

func (r *Repository) GetActiveConfig(ctx context.Context, vehicleTypeCode string) (*Config, error) {
	row := r.db.QueryRow(ctx, `
		SELECT `+pricingSelect+`
		FROM vehicle_pricing_configs
		WHERE vehicle_type_code = $1 AND is_active = TRUE
		ORDER BY effective_from DESC
		LIMIT 1
	`, vehicleTypeCode)
	return scanConfig(row.Scan)
}

func (r *Repository) GetConfigByVehicleType(ctx context.Context, vehicleTypeCode string) (*Config, error) {
	return r.GetActiveConfig(ctx, vehicleTypeCode)
}

func (r *Repository) GetConfigByID(ctx context.Context, id string) (*Config, error) {
	row := r.db.QueryRow(ctx, `SELECT `+pricingSelect+` FROM vehicle_pricing_configs WHERE id = $1`, id)
	return scanConfig(row.Scan)
}

func (r *Repository) ListActiveConfigs(ctx context.Context) ([]*Config, error) {
	rows, err := r.db.Query(ctx, `
		SELECT DISTINCT ON (vehicle_type_code) `+pricingSelect+`
		FROM vehicle_pricing_configs
		WHERE is_active = TRUE
		ORDER BY vehicle_type_code, effective_from DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Config
	for rows.Next() {
		cfg, err := scanConfig(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, cfg)
	}
	return out, rows.Err()
}

func (r *Repository) CreateConfig(ctx context.Context, cfg *Config, createdByUserID string) (*Config, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `UPDATE vehicle_pricing_configs SET is_active = FALSE WHERE vehicle_type_code = $1 AND is_active = TRUE`, cfg.VehicleTypeCode); err != nil {
		return nil, err
	}

	row := tx.QueryRow(ctx, `
		INSERT INTO vehicle_pricing_configs (
			vehicle_type_code, base_fare_rwf, base_distance_km,
			tier1_per_km_rwf, tier1_max_km, tier2_per_km_rwf,
			night_surcharge_pct, night_start_hour, night_end_hour,
			waiting_rwf_per_min, waiting_free_minutes,
			min_fare_rwf, cancellation_fee_rwf, is_active, created_by
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,TRUE,$14)
		RETURNING `+pricingSelect,
		cfg.VehicleTypeCode, cfg.BaseFareRWF, cfg.BaseDistanceKM,
		cfg.Tier1PerKmRWF, cfg.Tier1MaxKM, cfg.Tier2PerKmRWF,
		cfg.NightSurchargePct, cfg.NightStartHour, cfg.NightEndHour,
		cfg.WaitingRWFPerMin, cfg.WaitingFreeMinutes, cfg.MinFareRWF, cfg.CancellationFeeRWF, createdByUserID,
	)
	created, err := scanConfig(row.Scan)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return created, nil
}

func (r *Repository) GetConfigHistory(ctx context.Context, vehicleTypeCode string) ([]*Config, error) {
	rows, err := r.db.Query(ctx, `
		SELECT `+pricingSelect+`
		FROM vehicle_pricing_configs
		WHERE vehicle_type_code = $1
		ORDER BY effective_from DESC, created_at DESC
	`, vehicleTypeCode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Config
	for rows.Next() {
		cfg, err := scanConfig(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, cfg)
	}
	return out, rows.Err()
}

func (r *Repository) ListPublicPricing(ctx context.Context) ([]map[string]any, error) {
	rows, err := r.db.Query(ctx, `
		SELECT vt.code, vt.display_name, vt.max_passengers,
		       vpc.base_fare_rwf, vpc.base_distance_km,
		       vpc.tier1_per_km_rwf, vpc.tier1_max_km, vpc.tier2_per_km_rwf,
		       vpc.night_surcharge_pct, vpc.waiting_rwf_per_min, vpc.waiting_free_minutes,
		       vpc.min_fare_rwf, vpc.cancellation_fee_rwf
		FROM vehicle_types vt
		LEFT JOIN LATERAL (
			SELECT *
			FROM vehicle_pricing_configs
			WHERE vehicle_type_code = vt.code AND is_active = TRUE
			ORDER BY effective_from DESC
			LIMIT 1
		) vpc ON TRUE
		WHERE vt.is_active = TRUE
		ORDER BY vt.code
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []map[string]any
	for rows.Next() {
		var code, displayName string
		var maxPassengers int
		var baseFare, tier1PerKm, tier2PerKm, waitingFreeMinutes, minFare, cancellationFee *int
		var baseDistance, tier1Max, nightSurchargePct, waitingPerMin *float64
		if err := rows.Scan(
			&code, &displayName, &maxPassengers,
			&baseFare, &baseDistance,
			&tier1PerKm, &tier1Max, &tier2PerKm,
			&nightSurchargePct, &waitingPerMin, &waitingFreeMinutes,
			&minFare, &cancellationFee,
		); err != nil {
			return nil, err
		}
		entry := map[string]any{
			"code":                 code,
			"display_name":         displayName,
			"max_passengers":       maxPassengers,
			"base_fare_rwf":        baseFare,
			"base_distance_km":     baseDistance,
			"tier1_per_km_rwf":     tier1PerKm,
			"tier1_max_km":         tier1Max,
			"tier2_per_km_rwf":     tier2PerKm,
			"night_surcharge_pct":  nightSurchargePct,
			"waiting_rwf_per_min":  waitingPerMin,
			"waiting_free_minutes": waitingFreeMinutes,
			"min_fare_rwf":         minFare,
			"cancellation_fee_rwf": cancellationFee,
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}
