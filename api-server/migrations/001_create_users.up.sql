CREATE EXTENSION IF NOT EXISTS "pgcrypto";
CREATE EXTENSION IF NOT EXISTS "postgis";

CREATE TABLE IF NOT EXISTS users (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    phone_number       VARCHAR(20) UNIQUE NOT NULL,
    full_name          VARCHAR(255),
    role_state         VARCHAR(30) NOT NULL DEFAULT 'CUSTOMER_ONLY',
    device_id          VARCHAR(255),
    fcm_token          VARCHAR(512),
    is_suspended       BOOLEAN NOT NULL DEFAULT FALSE,
    suspension_until   TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_users_phone ON users(phone_number);
CREATE INDEX IF NOT EXISTS idx_users_role_state ON users(role_state);
