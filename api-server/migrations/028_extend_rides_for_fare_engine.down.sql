ALTER TABLE rides
    DROP COLUMN IF EXISTS pricing_config_id,
    DROP COLUMN IF EXISTS estimated_fare_rwf,
    DROP COLUMN IF EXISTS night_surcharge_applied,
    DROP COLUMN IF EXISTS night_surcharge_pct,
    DROP COLUMN IF EXISTS waiting_seconds,
    DROP COLUMN IF EXISTS waiting_charge_rwf,
    DROP COLUMN IF EXISTS cancellation_fee_rwf,
    DROP COLUMN IF EXISTS final_fare_rwf;
