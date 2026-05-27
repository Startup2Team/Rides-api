CREATE TABLE IF NOT EXISTS otp_verifications (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    phone_number VARCHAR(20) NOT NULL,
    otp_hash     VARCHAR(255) NOT NULL,
    purpose      VARCHAR(30) NOT NULL,
    is_used      BOOLEAN NOT NULL DEFAULT FALSE,
    expires_at   TIMESTAMPTZ NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_otp_phone_purpose ON otp_verifications(phone_number, purpose);
CREATE INDEX IF NOT EXISTS idx_otp_expires_at ON otp_verifications(expires_at);
