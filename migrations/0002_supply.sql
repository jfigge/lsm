-- 0002_supply: the supply side from SPEC §7 and §10 — login fields, open
-- months, shift windows, availability and supervisor ratings.
--
-- Same conventions as 0001: UUID TEXT ids, UTC RFC 3339 timestamps, enum
-- values spelled with underscores.

-- --------------------------------------------------------------- people --

-- Login name and the address staff are emailed at when a month opens.
ALTER TABLE persons ADD COLUMN email TEXT;
CREATE UNIQUE INDEX persons_email ON persons(email) WHERE email IS NOT NULL;

-- Seeded credentials are demo convenience only (SPEC §10.1): force a reset
-- on first login. password_hash carries its scheme prefix ("sha256:…") so
-- the verifier can accept a legacy hash and replace it on reset.
ALTER TABLE credentials ADD COLUMN must_change_password INTEGER NOT NULL DEFAULT 0
    CHECK (must_change_password IN (0, 1));

-- --------------------------------------------------------------- events --

-- Local calendar date of the event (venue time zone). Month membership is
-- substr(event_date, 1, 7); storing it avoids time-zone arithmetic in SQL.
ALTER TABLE events ADD COLUMN event_date TEXT NOT NULL DEFAULT '';
CREATE INDEX events_date ON events(event_date);

-- A month an admin has opened for signups (or closed afterwards). No row =
-- not yet opened. Opening a month exposes that month's events to staff.
CREATE TABLE schedule_months (
    id          TEXT PRIMARY KEY,
    month       TEXT NOT NULL UNIQUE,          -- YYYY-MM
    status      TEXT NOT NULL CHECK (status IN ('open', 'closed')),
    changed_at  TEXT NOT NULL,
    changed_by  TEXT REFERENCES persons(id) ON DELETE SET NULL
);

-- An event has one or more shift windows (1:30–9:30, 2:00–9:30 …); a
-- signup is for one window or for all of them.
CREATE TABLE shift_windows (
    id          TEXT PRIMARY KEY,
    event_id    TEXT NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    start_at    TEXT NOT NULL,
    end_at      TEXT NOT NULL,
    sort_order  INTEGER NOT NULL DEFAULT 0,
    -- Target of availability's composite key: a window must belong to the
    -- event it is chosen for.
    UNIQUE (id, event_id),
    CHECK (end_at > start_at)
);
CREATE INDEX shift_windows_event ON shift_windows(event_id, sort_order);

-- ---------------------------------------------------------- availability --

-- What a person offered for an event. This is the signup, before capping;
-- event_staff is the roster that capping produces from it.
CREATE TABLE availability (
    id           TEXT PRIMARY KEY,
    person_id    TEXT NOT NULL REFERENCES persons(id) ON DELETE CASCADE,
    event_id     TEXT NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    window_id    TEXT,                         -- NULL unless status = 'window'
    status       TEXT NOT NULL CHECK (status IN ('not_available', 'all_shifts', 'window')),
    -- First time the person offered themselves: the signup-time tiebreak.
    signed_up_at TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    UNIQUE (person_id, event_id),
    CHECK ((status = 'window') = (window_id IS NOT NULL)),
    FOREIGN KEY (window_id, event_id) REFERENCES shift_windows(id, event_id) ON DELETE CASCADE
);
CREATE INDEX availability_event ON availability(event_id, status);

-- --------------------------------------------------------------- ratings --

-- Supervisor performance ratings (SPEC §10.4). These are employment
-- records, so the table is append-only: a change is a new row, never an
-- UPDATE, and the full history of who rated whom and when is the table.
CREATE TABLE rating_entries (
    id        TEXT PRIMARY KEY,
    person_id TEXT NOT NULL REFERENCES persons(id) ON DELETE CASCADE,
    event_id  TEXT NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    rating    TEXT NOT NULL CHECK (rating IN
              ('exceptional', 'above_average', 'acceptable', 'below_par', 'needs_discipline')),
    rated_by  TEXT REFERENCES persons(id) ON DELETE SET NULL,
    rated_at  TEXT NOT NULL,
    source    TEXT NOT NULL CHECK (source IN ('seed', 'app', 'admin'))
);
CREATE INDEX rating_entries_person ON rating_entries(person_id, event_id, rated_at);

-- The rating in force per person per event: the latest entry (ties broken
-- by id, which is time-ordered for new rows).
CREATE VIEW ratings_current AS
SELECT id, person_id, event_id, rating, rated_by, rated_at, source
FROM (
    SELECT r.*, ROW_NUMBER() OVER (
        PARTITION BY person_id, event_id ORDER BY rated_at DESC, id DESC) AS rn
    FROM rating_entries r
)
WHERE rn = 1;
