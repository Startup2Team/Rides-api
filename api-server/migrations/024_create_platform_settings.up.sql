CREATE TABLE platform_settings (
    key        VARCHAR(100) PRIMARY KEY,
    value      JSONB        NOT NULL,
    updated_at TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

INSERT INTO platform_settings (key, value) VALUES
    ('commission',    '{"moto":15,"cab":15,"hilux":12,"fuso":10,"tuk_tuk":12}'),
    ('negotiation',   '{"maxRounds":3,"responseTimeoutSec":60,"maskedCallSec":120}'),
    ('fares',         '{"motoBase":500,"motoPerKm":200,"cabBase":1000,"cabPerKm":400,"hiluxBase":800,"hiluxPerKm":300,"fusoBase":1500,"fusoPerKm":500,"tukTukBase":600,"tukTukPerKm":250}'),
    ('regions',       '[{"id":"kigali","name":"Kigali","status":"Active","driverCount":0},{"id":"musanze","name":"Musanze","status":"Coming soon","driverCount":0},{"id":"rubavu","name":"Rubavu","status":"Coming soon","driverCount":0}]'),
    ('integrations',  '{"mtnMomo":true,"airtelMoney":true,"mapsProvider":"Google","sms":true,"email":true}'),
    ('notifications', '{"sosToOps":true,"sosToAdmins":true,"payoutSummary":true,"weeklyDigest":true,"incidentEscalation":true}');
