CREATE TABLE IF NOT EXISTS driver_documents (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    driver_id      UUID NOT NULL REFERENCES driver_profiles(id) ON DELETE CASCADE,
    document_type  VARCHAR(50) NOT NULL,
    -- LICENCE_FRONT | VEHICLE_INSURANCE | VEHICLE_AUTHORIZATION
    file_url       TEXT NOT NULL,
    uploaded_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_driver_documents_driver_id ON driver_documents(driver_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_driver_documents_driver_type ON driver_documents(driver_id, document_type);
