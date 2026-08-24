-- Waitlist form is relaxing to "name + area alone can submit": phone and
-- email are now both optional at the product level (see internal/waitlist
-- Submit()). Drop the NOT NULL on phone so an email-only (or contact-free)
-- signup can be inserted with phone = NULL.
ALTER TABLE waitlist_signups ALTER COLUMN phone DROP NOT NULL;

-- The existing UNIQUE(role, phone) constraint (see 086) never dedupes NULL
-- phones — Postgres treats every NULL as distinct for uniqueness purposes.
-- Without this, resubmitting the same email with no phone would create a new
-- row (and, worse, re-send a confirmation email) every time. This partial
-- unique index covers exactly the phone-less-but-has-email case; a signup
-- with neither phone nor email has nothing to dedupe on and is an accepted
-- gap (every such submission is a new row — see internal/waitlist/repository.go
-- Create()).
CREATE UNIQUE INDEX waitlist_signups_role_email_key
    ON waitlist_signups (role, email)
    WHERE phone IS NULL AND email IS NOT NULL;
