-- Driver gender, captured during onboarding (optional; male | female | other).
ALTER TABLE driver_profiles
    ADD COLUMN IF NOT EXISTS gender VARCHAR(20);
