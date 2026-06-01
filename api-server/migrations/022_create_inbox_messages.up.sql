CREATE TABLE inbox_messages (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    from_name  TEXT        NOT NULL,
    from_email TEXT        NOT NULL,
    category   VARCHAR(50) NOT NULL DEFAULT 'GENERAL',
    status     VARCHAR(20) NOT NULL DEFAULT 'NEW',
    subject    TEXT        NOT NULL,
    body       TEXT        NOT NULL,
    reply_body TEXT,
    replied_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_inbox_status   ON inbox_messages(status);
CREATE INDEX idx_inbox_created  ON inbox_messages(created_at DESC);
