-- 0004_stations: unattended kiosk screens (SPEC §3 /kiosk).
--
-- A kiosk is paired once by an admin and then holds a station token rather
-- than anyone's personal session: the token can scan badges at that kiosk
-- and nothing else, so a kiosk left at an entrance exposes no account.

CREATE TABLE stations (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,                 -- "East entrance"
    kind         TEXT NOT NULL CHECK (kind IN ('kiosk')),
    token_hash   TEXT NOT NULL UNIQUE,          -- SHA-256; the token is shown once
    created_at   TEXT NOT NULL,
    created_by   TEXT REFERENCES persons(id) ON DELETE SET NULL,
    last_seen_at TEXT,
    revoked_at   TEXT
);

-- Check-ins record the kiosk they happened at, so "checked in at 6:42"
-- can say where.
ALTER TABLE checkins ADD COLUMN checked_in_station TEXT REFERENCES stations(id) ON DELETE SET NULL;
ALTER TABLE checkins ADD COLUMN checked_out_station TEXT REFERENCES stations(id) ON DELETE SET NULL;
