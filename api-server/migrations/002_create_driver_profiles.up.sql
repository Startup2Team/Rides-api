CREATE TABLE IF NOT EXISTS driver_profiles (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id            UUID UNIQUE NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    transport_type     VARCHAR(20) NOT NULL,
    vehicle_plate      VARCHAR(20) UNIQUE NOT NULL,
    license_number     VARCHAR(50) UNIQUE NOT NULL,
    date_of_birth      DATE NOT NULL,
    city               VARCHAR(100) NOT NULL,
    momo_pay_code      VARCHAR(100) NOT NULL,
    approval_status    VARCHAR(20) NOT NULL DEFAULT 'PENDING_REVIEW',
    approved_by        UUID REFERENCES users(id),
    approved_at        TIMESTAMPTZ,
    rejection_reason   TEXT,
    suspension_reason  TEXT,
    is_online          BOOLEAN NOT NULL DEFAULT FALSE,
    priority_tier      INT NOT NULL DEFAULT 1,
    offline_at         TIMESTAMPTZ,
    acceptance_rate    DECIMAL(5,2) NOT NULL DEFAULT 100.00,
    total_rides        INT NOT NULL DEFAULT 0,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_driver_profiles_user_id ON driver_profiles(user_id);
CREATE INDEX IF NOT EXISTS idx_driver_profiles_approval_status ON driver_profiles(approval_status);
CREATE INDEX IF NOT EXISTS idx_driver_profiles_transport_type ON driver_profiles(transport_type);
CREATE INDEX IF NOT EXISTS idx_driver_profiles_is_online ON driver_profiles(is_online);
