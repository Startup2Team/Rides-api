CREATE TABLE ride_packages (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            VARCHAR(50) NOT NULL,
    vehicle_type_id UUID NOT NULL REFERENCES vehicle_types(id),
    ride_count      INTEGER NOT NULL,
    validity_days   INTEGER NOT NULL DEFAULT 30,
    price_rwf       INTEGER NOT NULL,
    is_promotional  BOOLEAN NOT NULL DEFAULT FALSE,
    is_active       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Moto packages (30 RWF/ride credit cost)
INSERT INTO ride_packages (name, vehicle_type_id, ride_count, validity_days, price_rwf, is_promotional)
SELECT 'Moto Free Trial', id, 20, 30, 0, TRUE FROM vehicle_types WHERE code = 'MOTO_BIKE';

INSERT INTO ride_packages (name, vehicle_type_id, ride_count, validity_days, price_rwf)
SELECT 'Moto Starter', id, 20, 30, 600 FROM vehicle_types WHERE code = 'MOTO_BIKE';

INSERT INTO ride_packages (name, vehicle_type_id, ride_count, validity_days, price_rwf)
SELECT 'Moto Standard', id, 50, 30, 1400 FROM vehicle_types WHERE code = 'MOTO_BIKE';

INSERT INTO ride_packages (name, vehicle_type_id, ride_count, validity_days, price_rwf)
SELECT 'Moto Pro', id, 100, 30, 2500 FROM vehicle_types WHERE code = 'MOTO_BIKE';

-- Cab packages (200 RWF/ride credit cost)
INSERT INTO ride_packages (name, vehicle_type_id, ride_count, validity_days, price_rwf, is_promotional)
SELECT 'Cab Free Trial', id, 20, 30, 0, TRUE FROM vehicle_types WHERE code = 'CAB_TAXI';

INSERT INTO ride_packages (name, vehicle_type_id, ride_count, validity_days, price_rwf)
SELECT 'Cab Starter', id, 20, 30, 3800 FROM vehicle_types WHERE code = 'CAB_TAXI';

INSERT INTO ride_packages (name, vehicle_type_id, ride_count, validity_days, price_rwf)
SELECT 'Cab Standard', id, 50, 30, 9000 FROM vehicle_types WHERE code = 'CAB_TAXI';

INSERT INTO ride_packages (name, vehicle_type_id, ride_count, validity_days, price_rwf)
SELECT 'Cab Pro', id, 100, 30, 17000 FROM vehicle_types WHERE code = 'CAB_TAXI';

-- Heavy Fuso packages (300 RWF/ride credit cost)
INSERT INTO ride_packages (name, vehicle_type_id, ride_count, validity_days, price_rwf, is_promotional)
SELECT 'Fuso Free Trial', id, 10, 30, 0, TRUE FROM vehicle_types WHERE code = 'HEAVY_FUSO';

INSERT INTO ride_packages (name, vehicle_type_id, ride_count, validity_days, price_rwf)
SELECT 'Fuso Starter', id, 20, 30, 5700 FROM vehicle_types WHERE code = 'HEAVY_FUSO';

INSERT INTO ride_packages (name, vehicle_type_id, ride_count, validity_days, price_rwf)
SELECT 'Fuso Standard', id, 50, 30, 13500 FROM vehicle_types WHERE code = 'HEAVY_FUSO';

-- Light Hilux packages (100 RWF/ride credit cost)
INSERT INTO ride_packages (name, vehicle_type_id, ride_count, validity_days, price_rwf, is_promotional)
SELECT 'Hilux Free Trial', id, 20, 30, 0, TRUE FROM vehicle_types WHERE code = 'LIGHT_HILUX';

INSERT INTO ride_packages (name, vehicle_type_id, ride_count, validity_days, price_rwf)
SELECT 'Hilux Starter', id, 20, 30, 1900 FROM vehicle_types WHERE code = 'LIGHT_HILUX';

INSERT INTO ride_packages (name, vehicle_type_id, ride_count, validity_days, price_rwf)
SELECT 'Hilux Standard', id, 50, 30, 4500 FROM vehicle_types WHERE code = 'LIGHT_HILUX';

INSERT INTO ride_packages (name, vehicle_type_id, ride_count, validity_days, price_rwf)
SELECT 'Hilux Pro', id, 100, 30, 8500 FROM vehicle_types WHERE code = 'LIGHT_HILUX';

-- Tuk Tuk packages (100 RWF/ride credit cost)
INSERT INTO ride_packages (name, vehicle_type_id, ride_count, validity_days, price_rwf, is_promotional)
SELECT 'Tuk Tuk Free Trial', id, 20, 30, 0, TRUE FROM vehicle_types WHERE code = 'TUK_TUK';

INSERT INTO ride_packages (name, vehicle_type_id, ride_count, validity_days, price_rwf)
SELECT 'Tuk Tuk Starter', id, 20, 30, 1900 FROM vehicle_types WHERE code = 'TUK_TUK';

INSERT INTO ride_packages (name, vehicle_type_id, ride_count, validity_days, price_rwf)
SELECT 'Tuk Tuk Standard', id, 50, 30, 4500 FROM vehicle_types WHERE code = 'TUK_TUK';

INSERT INTO ride_packages (name, vehicle_type_id, ride_count, validity_days, price_rwf)
SELECT 'Tuk Tuk Pro', id, 100, 30, 8500 FROM vehicle_types WHERE code = 'TUK_TUK';
