-- Rollback migration 030

DELETE FROM vehicle_pricing_configs WHERE vehicle_type_code IN ('LIGHT_HILUX', 'HEAVY_FUSO', 'TUK_TUK');

DELETE FROM hot_zones WHERE city = 'Kigali';

DELETE FROM landmarks WHERE name IN (
    'Nyabugogo Bus Park','Remera','Kacyiru','Kimironko','Nyamirambo',
    'Gikondo','Kicukiro','CHUK Hospital','King Faisal Hospital',
    'Rwanda Military Hospital','Gasabo District','Norrsken House',
    'Kigali SEZ','Kimihurura','Gisozi'
);
