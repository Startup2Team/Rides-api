ALTER TABLE driver_profiles 
ADD COLUMN IF NOT EXISTS referred_by_driver_id UUID REFERENCES driver_profiles(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_driver_profiles_referred_by ON driver_profiles(referred_by_driver_id);
