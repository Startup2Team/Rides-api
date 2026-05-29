ALTER TABLE rides
    ADD COLUMN pricing_config_id UUID REFERENCES vehicle_pricing_configs(id),
    ADD COLUMN estimated_fare_rwf DECIMAL(10,2),
    ADD COLUMN night_surcharge_applied BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN night_surcharge_pct DECIMAL(5,4) NOT NULL DEFAULT 0.0,
    ADD COLUMN waiting_seconds INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN waiting_charge_rwf DECIMAL(10,2) NOT NULL DEFAULT 0.0,
    ADD COLUMN cancellation_fee_rwf DECIMAL(10,2) NOT NULL DEFAULT 0.0,
    ADD COLUMN final_fare_rwf DECIMAL(10,2);
