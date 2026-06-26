-- =====================================================
-- Migration 030: Complete pricing configs + Kigali hot zones + extend landmarks
-- =====================================================

-- ─── Extend landmarks table to match v3 schema ───────────────────────────────

ALTER TABLE landmarks
    ADD COLUMN IF NOT EXISTS city      VARCHAR(50)  NOT NULL DEFAULT 'Kigali',
    ADD COLUMN IF NOT EXISTS is_active BOOLEAN      NOT NULL DEFAULT TRUE;

-- Fix landmark category to use varchar instead of text (cosmetic — already text)
-- and backfill geohash6 accuracy (no-op, just ensure)

-- ─── Pricing configs for the 3 remaining vehicle types ───────────────────────
-- MOTO_BIKE and CAB_TAXI already seeded in migration 027.

INSERT INTO vehicle_pricing_configs (
    vehicle_type_code, base_fare_rwf, base_distance_km,
    tier1_per_km_rwf, tier1_max_km, tier2_per_km_rwf,
    night_surcharge_pct, night_start_hour, night_end_hour,
    waiting_rwf_per_min, waiting_free_minutes,
    min_fare_rwf, cancellation_fee_rwf, is_active
)
SELECT 'LIGHT_HILUX', 1200, 1.0,
       400, 30.0, 320,
       0.20, 22, 5,
       100.0, 5,
       1200, 400, TRUE
WHERE NOT EXISTS (
    SELECT 1 FROM vehicle_pricing_configs
    WHERE vehicle_type_code = 'LIGHT_HILUX' AND is_active = TRUE
);

INSERT INTO vehicle_pricing_configs (
    vehicle_type_code, base_fare_rwf, base_distance_km,
    tier1_per_km_rwf, tier1_max_km, tier2_per_km_rwf,
    night_surcharge_pct, night_start_hour, night_end_hour,
    waiting_rwf_per_min, waiting_free_minutes,
    min_fare_rwf, cancellation_fee_rwf, is_active
)
SELECT 'HEAVY_FUSO', 2000, 1.0,
       600, 50.0, 500,
       0.20, 22, 5,
       200.0, 10,
       2000, 800, TRUE
WHERE NOT EXISTS (
    SELECT 1 FROM vehicle_pricing_configs
    WHERE vehicle_type_code = 'HEAVY_FUSO' AND is_active = TRUE
);

INSERT INTO vehicle_pricing_configs (
    vehicle_type_code, base_fare_rwf, base_distance_km,
    tier1_per_km_rwf, tier1_max_km, tier2_per_km_rwf,
    night_surcharge_pct, night_start_hour, night_end_hour,
    waiting_rwf_per_min, waiting_free_minutes,
    min_fare_rwf, cancellation_fee_rwf, is_active
)
SELECT 'TUK_TUK', 700, 1.0,
       250, 20.0, 200,
       0.25, 22, 5,
       50.0, 3,
       700, 200, TRUE
WHERE NOT EXISTS (
    SELECT 1 FROM vehicle_pricing_configs
    WHERE vehicle_type_code = 'TUK_TUK' AND is_active = TRUE
);

-- ─── Update vehicle_types display names ──────────────────────────────────────

UPDATE vehicle_types SET display_name = 'Moto'         WHERE code = 'MOTO_BIKE';
UPDATE vehicle_types SET display_name = 'Cab'          WHERE code = 'CAB_TAXI';
UPDATE vehicle_types SET display_name = 'Light Hilux'  WHERE code = 'LIGHT_HILUX';
UPDATE vehicle_types SET display_name = 'Heavy Fuso'   WHERE code = 'HEAVY_FUSO';
UPDATE vehicle_types SET display_name = 'Tuk Tuk'      WHERE code = 'TUK_TUK';

