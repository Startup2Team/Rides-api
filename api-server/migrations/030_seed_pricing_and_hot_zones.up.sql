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

-- ─── Kigali hot zones ─────────────────────────────────────────────────────────

INSERT INTO hot_zones (name, category, lat, lng, geohash6, radius_meters, city) VALUES
('Nyabugogo Bus Park',             'transport_hub', -1.9388, 30.0431, 'ks7gfz', 600, 'Kigali'),
('Remera Bus Terminal',            'transport_hub', -1.9513, 30.1103, 'ks7gxg', 400, 'Kigali'),
('Kacyiru Bus Stop',               'transport_hub', -1.9284, 30.0619, 'ks7gvy', 350, 'Kigali'),
('UTC Nyarugenge Bus Stop',        'transport_hub', -1.9559, 30.0617, 'ks7gtp', 300, 'Kigali'),
('Kimironko Market',               'market',        -1.9355, 30.1127, 'ks7gwx', 500, 'Kigali'),
('Nyabugogo Market',               'market',        -1.9388, 30.0431, 'ks7gfz', 400, 'Kigali'),
('Kicukiro Market',                'market',        -2.0015, 30.0751, 'ks7g6n', 400, 'Kigali'),
('Gikondo Market',                 'market',        -1.9755, 30.0756, 'ks7gkp', 400, 'Kigali'),
('University of Rwanda – Gikondo', 'university',    -1.9755, 30.0756, 'ks7gkp', 500, 'Kigali'),
('AUCA – Gisozi',                  'university',    -1.9170, 30.0619, 'ks7gvy', 400, 'Kigali'),
('CHUK (University Hospital)',     'hospital',      -1.9560, 30.0590, 'ks7gtp', 400, 'Kigali'),
('King Faisal Hospital',           'hospital',      -1.9295, 30.0619, 'ks7gvy', 400, 'Kigali'),
('Rwanda Military Hospital',       'hospital',      -1.9738, 30.0820, 'ks7gks', 400, 'Kigali'),
('Kibagabaga Hospital',            'hospital',      -1.9167, 30.1127, 'ks7gwx', 350, 'Kigali'),
('Kigali CBD (KBC)',                'office',        -1.9441, 30.0619, 'ks7gqy', 500, 'Kigali'),
('Kacyiru Government Hill',        'office',        -1.9284, 30.0818, 'ks7gvy', 400, 'Kigali'),
('Norrsken House Kigali',          'office',        -1.9513, 30.0619, 'ks7gqy', 200, 'Kigali'),
('Kigali Special Economic Zone',   'office',        -1.9755, 30.1027, 'ks7gks', 500, 'Kigali'),
('Kigali Serena Hotel',            'hotel',         -1.9441, 30.0619, 'ks7gqy', 200, 'Kigali'),
('Hotel des Mille Collines',       'hotel',         -1.9513, 30.0607, 'ks7gtp', 200, 'Kigali'),
('Kigali City Tower Mall',         'market',        -1.9441, 30.0619, 'ks7gqy', 300, 'Kigali'),
('Simba Supercentre',              'market',        -1.9730, 30.0609, 'ks7gkj', 300, 'Kigali'),
('Nakumatt Remera',                'market',        -1.9513, 30.1103, 'ks7gxg', 300, 'Kigali'),
('Kicukiro – Sonatubes',           'residential',   -2.0015, 30.0751, 'ks7g6n', 500, 'Kigali'),
('Gisozi Residential',             'residential',   -1.9170, 30.0619, 'ks7gvy', 500, 'Kigali'),
('Kimihurura',                     'residential',   -1.9513, 30.0909, 'ks7gvx', 500, 'Kigali'),
('Nyamirambo',                     'residential',   -1.9731, 30.0380, 'ks7gk9', 500, 'Kigali'),
('Gasabo District Offices',        'office',        -1.9167, 30.0619, 'ks7gvy', 300, 'Kigali'),
('Gikondo Industrial',             'office',        -1.9755, 30.0756, 'ks7gkp', 500, 'Kigali'),
('Kigali Airport (RwandAir)',       'transport_hub', -1.9687, 30.1395, 'ks7gx5', 800, 'Kigali')
ON CONFLICT DO NOTHING;

-- ─── Additional landmarks ─────────────────────────────────────────────────────
-- Extend the 20 existing landmarks with more Kigali coverage.

INSERT INTO landmarks (name, category, lat, lng, geohash6, city) VALUES
('Nyabugogo Bus Park',       'transport',   -1.9388, 30.0431, 'ks7gfz', 'Kigali'),
('Gikondo',                  'market',      -1.9755, 30.0756, 'ks7gkp', 'Kigali'),
('Kicukiro',                 'market',      -2.0015, 30.0751, 'ks7g6n', 'Kigali'),
('CHUK Hospital',            'hospital',    -1.9560, 30.0590, 'ks7gtp', 'Kigali'),
('King Faisal Hospital',     'hospital',    -1.9295, 30.0619, 'ks7gvy', 'Kigali'),
('Rwanda Military Hospital', 'hospital',    -1.9738, 30.0820, 'ks7gks', 'Kigali'),
('Norrsken House',           'other',       -1.9513, 30.0619, 'ks7gqy', 'Kigali'),
('Kigali SEZ',               'other',       -1.9755, 30.1027, 'ks7gks', 'Kigali'),
('Kimihurura',               'other',       -1.9513, 30.0909, 'ks7gvx', 'Kigali'),
('Gisozi',                   'other',       -1.9170, 30.0619, 'ks7gvy', 'Kigali'),
('Kigali Airport',           'transport',   -1.9687, 30.1395, 'ks7gx5', 'Kigali'),
('Kibagabaga Hospital',      'hospital',    -1.9167, 30.1127, 'ks7gwx', 'Kigali'),
('Kicukiro Market',          'market',      -2.0015, 30.0751, 'ks7g6n', 'Kigali'),
('Gasabo District',          'government',  -1.9167, 30.0619, 'ks7gvy', 'Kigali'),
('Nyamirambo',               'market',      -1.9731, 30.0380, 'ks7gk9', 'Kigali')
ON CONFLICT DO NOTHING;
