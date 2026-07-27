-- notifications.type was VARCHAR(20). 'package_purchase_paid' is 21 characters,
-- so every "Payment successful" notification for an automatic MoMo purchase failed
-- to insert. Persist() only logs a warning, so drivers silently got no notification
-- while their credits landed — and nothing in the API surfaced an error.
--
-- Widen rather than shorten the value: type names are descriptive and will keep
-- growing, and a length cap that silently drops notifications is a bad trade.
ALTER TABLE notifications ALTER COLUMN type TYPE VARCHAR(40);
