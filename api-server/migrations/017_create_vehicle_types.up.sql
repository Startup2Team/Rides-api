CREATE TABLE vehicle_types (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code            VARCHAR(20) UNIQUE NOT NULL,
    display_name    VARCHAR(50) NOT NULL,
    base_fare_rwf   INTEGER NOT NULL,
    per_km_fare_rwf INTEGER NOT NULL,
    min_fare_rwf    INTEGER NOT NULL,
    max_passengers  INTEGER NOT NULL DEFAULT 1,
    credit_cost_rwf INTEGER NOT NULL,
    is_active       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO vehicle_types (code, display_name, base_fare_rwf, per_km_fare_rwf, min_fare_rwf, max_passengers, credit_cost_rwf) VALUES
    ('MOTO_BIKE',   'Moto',        500,  200, 500,  1,  30),
    ('CAB_TAXI',    'Cab',         1000, 400, 1500, 4,  200),
    ('HEAVY_FUSO',  'Heavy Fuso',  1500, 500, 2000, 1,  300),
    ('LIGHT_HILUX', 'Light Hilux', 800,  300, 1000, 6,  100),
    ('TUK_TUK',     'Tuk Tuk',     600,  250, 700,  3,  100);
