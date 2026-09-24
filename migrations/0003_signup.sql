-- 0003_signup: sign-in sessions, and the signup capping of SPEC §10.3 —
-- rule weights, required headcount and cutoff overrides.

-- -------------------------------------------------------------- sessions --

-- A signed-in device. The app keeps the token in the Keychain; only its
-- SHA-256 is stored, so a leaked database does not leak live sessions.
CREATE TABLE sessions (
    id           TEXT PRIMARY KEY,
    person_id    TEXT NOT NULL REFERENCES persons(id) ON DELETE CASCADE,
    token_hash   TEXT NOT NULL UNIQUE,
    created_at   TEXT NOT NULL,
    last_used_at TEXT NOT NULL,
    expires_at   TEXT NOT NULL,
    revoked_at   TEXT
);
CREATE INDEX sessions_person ON sessions(person_id);

-- --------------------------------------------------------- ranking rules --

-- One row per rule. The rules themselves are code (each scores a person 0
-- to 100); their weights are data, editable in /admin without a rebuild.
-- Weight 0 disables a rule without removing it. The signup-time tiebreak
-- is not weighted and has no row.
CREATE TABLE ranking_rules (
    id          TEXT PRIMARY KEY,
    rule        TEXT NOT NULL UNIQUE,
    weight      INTEGER NOT NULL CHECK (weight >= 0),
    sort_order  INTEGER NOT NULL DEFAULT 0,
    updated_at  TEXT NOT NULL,
    updated_by  TEXT REFERENCES persons(id) ON DELETE SET NULL
);

-- Starting weights from SPEC §10.3, expected to be tuned after a month.
INSERT INTO ranking_rules (id, rule, weight, sort_order, updated_at) VALUES
    ('0192f000-0000-7000-8000-000000000001', 'availability_rate',  25, 1, '2026-09-24T00:00:00.000000Z'),
    ('0192f000-0000-7000-8000-000000000002', 'performance_rating', 25, 2, '2026-09-24T00:00:00.000000Z'),
    ('0192f000-0000-7000-8000-000000000003', 'capability_match',   20, 3, '2026-09-24T00:00:00.000000Z'),
    ('0192f000-0000-7000-8000-000000000004', 'season_load',        15, 4, '2026-09-24T00:00:00.000000Z'),
    ('0192f000-0000-7000-8000-000000000005', 'recency',            10, 5, '2026-09-24T00:00:00.000000Z'),
    ('0192f000-0000-7000-8000-000000000006', 'tenure',              5, 6, '2026-09-24T00:00:00.000000Z');

-- ----------------------------------------------------- required headcount --

-- How many people a department needs for an event. Signups are ranked per
-- department (security does not compete with concessions for a slot).
-- Without a row the requirement is derived from the department's posts
-- that apply to the event; a row on an event type overrides that for
-- every event of the type, and a row on a single event overrides both.
CREATE TABLE headcount_overrides (
    id            TEXT PRIMARY KEY,
    role_id       TEXT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    event_type_id TEXT REFERENCES event_types(id) ON DELETE CASCADE,
    event_id      TEXT REFERENCES events(id) ON DELETE CASCADE,
    headcount     INTEGER NOT NULL CHECK (headcount >= 0),
    updated_at    TEXT NOT NULL,
    updated_by    TEXT REFERENCES persons(id) ON DELETE SET NULL,
    CHECK ((event_type_id IS NULL) <> (event_id IS NULL))
);
CREATE UNIQUE INDEX headcount_overrides_type ON headcount_overrides(role_id, event_type_id)
    WHERE event_type_id IS NOT NULL;
CREATE UNIQUE INDEX headcount_overrides_event ON headcount_overrides(role_id, event_id)
    WHERE event_id IS NOT NULL;

-- ---------------------------------------------------- cutoff overrides --

-- The cutoff is a recommendation, not a lock: an admin may pull a named
-- person above the line. Recorded like a min_tenure override; withdrawing
-- one is a soft delete so the history stays.
CREATE TABLE signup_overrides (
    id          TEXT PRIMARY KEY,
    event_id    TEXT NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    person_id   TEXT NOT NULL REFERENCES persons(id) ON DELETE CASCADE,
    created_at  TEXT NOT NULL,
    created_by  TEXT REFERENCES persons(id) ON DELETE SET NULL,
    note        TEXT NOT NULL DEFAULT '',
    removed_at  TEXT,
    removed_by  TEXT REFERENCES persons(id) ON DELETE SET NULL
);
CREATE UNIQUE INDEX signup_overrides_live ON signup_overrides(event_id, person_id)
    WHERE removed_at IS NULL;
