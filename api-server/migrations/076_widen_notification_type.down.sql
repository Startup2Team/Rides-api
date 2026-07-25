-- Truncate anything that would no longer fit before narrowing the column back.
UPDATE notifications SET type = left(type, 20) WHERE length(type) > 20;
ALTER TABLE notifications ALTER COLUMN type TYPE VARCHAR(20);
