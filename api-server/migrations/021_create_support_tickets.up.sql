CREATE TABLE support_tickets (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subject      TEXT        NOT NULL,
    type         VARCHAR(50) NOT NULL DEFAULT 'OTHER',
    priority     VARCHAR(20) NOT NULL DEFAULT 'MEDIUM',
    status       VARCHAR(20) NOT NULL DEFAULT 'OPEN',
    from_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    from_role    VARCHAR(20),
    ride_id      UUID REFERENCES rides(id) ON DELETE SET NULL,
    assigned_to  UUID,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE ticket_messages (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ticket_id  UUID        NOT NULL REFERENCES support_tickets(id) ON DELETE CASCADE,
    from_role  VARCHAR(20) NOT NULL,
    author     TEXT        NOT NULL,
    body       TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_tickets_status    ON support_tickets(status);
CREATE INDEX idx_tickets_priority  ON support_tickets(priority);
CREATE INDEX idx_tickets_created   ON support_tickets(created_at DESC);
CREATE INDEX idx_ticket_messages   ON ticket_messages(ticket_id);
