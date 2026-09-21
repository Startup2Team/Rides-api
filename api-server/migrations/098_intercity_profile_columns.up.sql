-- =====================================================
-- Migration 098: Intercity — profile columns
-- =====================================================

-- Recorded from day one so the deposit decision can later be made on evidence.
-- Nobody is blocked on this counter in Phase 1.
ALTER TABLE customer_profiles
    ADD COLUMN IF NOT EXISTS no_show_count INTEGER NOT NULL DEFAULT 0;

-- Driver availability gate, read on BOTH matching paths:
--   * primary  — matching/engine.go, from the profile FindProfileByID already loads
--   * fallback — driver/repository.go FindNearby, one column comparison
--
-- NULLABLE ON PURPOSE: NULL (or a past timestamp) means "not committed", so the
-- safe default is VISIBLE. An earlier design used a Redis key whose ABSENCE
-- meant "skip this driver" — a TTL there decayed to invisible, not available,
-- and would have stranded drivers silently. A timestamp expires by comparison,
-- so every failure mode here makes a driver MORE visible, never less.
ALTER TABLE driver_profiles
    ADD COLUMN IF NOT EXISTS intercity_committed_until TIMESTAMPTZ;

-- ADD COLUMN ... NOT NULL DEFAULT 0 is catalog-only on PG16 (no table rewrite).
ANALYZE customer_profiles;
ANALYZE driver_profiles;
