CREATE TABLE vehicle_pricing_configs (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    vehicle_type_code       VARCHAR(20) NOT NULL REFERENCES vehicle_types(code),
    base_fare_rwf           INTEGER NOT NULL CHECK (base_fare_rwf > 0),
    base_distance_km        DECIMAL(6,2) NOT NULL DEFAULT 1.0,
    tier1_per_km_rwf        INTEGER NOT NULL CHECK (tier1_per_km_rwf > 0),
    tier1_max_km            DECIMAL(6,2) NOT NULL,
    tier2_per_km_rwf        INTEGER NOT NULL CHECK (tier2_per_km_rwf > 0),
    night_surcharge_pct     DECIMAL(5,4) NOT NULL DEFAULT 0.0,
    night_start_hour        SMALLINT NOT NULL DEFAULT 22 CHECK (night_start_hour BETWEEN 0 AND 23),
    night_end_hour          SMALLINT NOT NULL DEFAULT 5 CHECK (night_end_hour BETWEEN 0 AND 23),
    waiting_rwf_per_min     DECIMAL(8,2) NOT NULL DEFAULT 0.0,
    waiting_free_minutes    SMALLINT NOT NULL DEFAULT 0,
    min_fare_rwf            INTEGER NOT NULL DEFAULT 0,
    cancellation_fee_rwf    INTEGER NOT NULL DEFAULT 0,
    is_active               BOOLEAN NOT NULL DEFAULT TRUE,
    effective_from          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by              UUID REFERENCES users(id),
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_vpc_vehicle_active ON vehicle_pricing_configs(vehicle_type_code, is_active, effective_from DESC);

INSERT INTO vehicle_pricing_configs
    (vehicle_type_code, base_fare_rwf, base_distance_km,
     tier1_per_km_rwf, tier1_max_km, tier2_per_km_rwf,
     night_surcharge_pct, night_start_hour, night_end_hour,
     waiting_rwf_per_min, waiting_free_minutes,
     min_fare_rwf, cancellation_fee_rwf)
VALUES
    ('MOTO_BIKE', 500, 1.0, 150, 40.0, 250, 0.30, 22, 5, 30, 5, 500, 200),
    ('CAB_TAXI', 1800, 1.0, 1080, 30.0, 900, 0.0, 22, 5, 100, 15, 1800, 500);
