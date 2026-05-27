CREATE TABLE IF NOT EXISTS landmarks (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT NOT NULL,
    category   TEXT NOT NULL,
    lat        DOUBLE PRECISION NOT NULL,
    lng        DOUBLE PRECISION NOT NULL,
    geohash6   TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_landmarks_geohash  ON landmarks(geohash6);
CREATE INDEX IF NOT EXISTS idx_landmarks_category ON landmarks(category);

INSERT INTO landmarks (name, category, lat, lng, geohash6) VALUES
('Kigali CBD',               'district',  -1.9441, 30.0619, 'ks3g7v'),
('Kimironko Market',         'market',    -1.9355, 30.1127, 'ks3gcx'),
('Remera',                   'district',  -1.9513, 30.1103, 'ks3g9m'),
('Nyamirambo',               'district',  -1.9731, 30.0380, 'ks3g4h'),
('Kacyiru',                  'district',  -1.9284, 30.0619, 'ks3g9v'),
('Gishushu',                 'district',  -1.9500, 30.0750, 'ks3g7x'),
('Kanombe Airport',          'transport', -1.9687, 30.1395, 'ks3gbq'),
('Nyabugogo Bus Park',       'transport', -1.9317, 30.0469, 'ks3g8e'),
('Kigali Convention Centre', 'landmark',  -1.9536, 30.0928, 'ks3g8k'),
('Sonatubes',                'district',  -1.9600, 30.1050, 'ks3g8v'),
('Gikondo',                  'district',  -1.9800, 30.0900, 'ks3g6j'),
('Muhima',                   'district',  -1.9600, 30.0500, 'ks3g7b'),
('Kisimenti',                'district',  -1.9350, 30.0900, 'ks3g9h'),
('UTC / Downtown',           'landmark',  -1.9500, 30.0610, 'ks3g7v'),
('Kigali Heights',           'landmark',  -1.9400, 30.0940, 'ks3g9c'),
('Nyarutarama',              'district',  -1.9200, 30.1100, 'ks3gcv'),
('Gaculiro',                 'district',  -1.9650, 30.0850, 'ks3g7p'),
('Kibagabaga Hospital',      'hospital',  -1.9250, 30.1050, 'ks3gc5'),
('King Faisal Hospital',     'hospital',  -1.9380, 30.0780, 'ks3g8h'),
('CHUK Hospital',            'hospital',  -1.9550, 30.0600, 'ks3g7q')
ON CONFLICT DO NOTHING;
