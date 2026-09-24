-- 0001_core: the core data model from SPEC §2.
--
-- Conventions
--   * Every entity id is a UUID stored as TEXT (canonical lowercase form).
--     No INTEGER PRIMARY KEY AUTOINCREMENT anywhere: export/re-import must
--     never collide.
--   * Timestamps are TEXT, RFC 3339, UTC. Calendar dates are TEXT YYYY-MM-DD.
--   * Tenure thresholds are INTEGER half-years: 0..6, where 6 means "3+".
--   * Join tables for many-to-many mappings carry no id of their own; the
--     pair is the key. Both ends edit the same row (SPEC "reciprocal
--     many-to-many").

-- ---------------------------------------------------------------- roles --

-- Hierarchical: Event Security, Event Services, Vendor/Concessions → outlet …
CREATE TABLE roles (
    id          TEXT PRIMARY KEY,
    parent_id   TEXT REFERENCES roles(id) ON DELETE RESTRICT,
    name        TEXT NOT NULL,
    sort_order  INTEGER NOT NULL DEFAULT 0
);
-- Sibling names are unique; top-level names are unique among themselves.
CREATE UNIQUE INDEX roles_sibling_name ON roles(COALESCE(parent_id, ''), name);

-- --------------------------------------------------------------- people --

CREATE TABLE persons (
    id          TEXT PRIMARY KEY,
    badge       TEXT NOT NULL UNIQUE,          -- 1D barcode, the scan lookup key
    name        TEXT NOT NULL,
    photo       TEXT,                          -- path/URL, optional
    role_id     TEXT NOT NULL REFERENCES roles(id) ON DELETE RESTRICT,
    -- Lead is deliberately absent: it is a property of the position.
    tier        TEXT NOT NULL DEFAULT 'staff'
                CHECK (tier IN ('staff', 'supervisor', 'admin')),
    hire_date   TEXT NOT NULL,                 -- tenure is computed, never stored
    -- Admin-only visibility. 'unknown' = not recorded, not a third category.
    gender      TEXT NOT NULL DEFAULT 'unknown'
                CHECK (gender IN ('male', 'female', 'unknown')),
    -- The supervisor this person is normally allocated under. Seeds each
    -- event's home supervisor (see event_staff).
    supervisor_id TEXT REFERENCES persons(id) ON DELETE SET NULL,
    active      INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0, 1)),
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);
CREATE INDEX persons_role ON persons(role_id);
CREATE INDEX persons_supervisor ON persons(supervisor_id);

-- Login for /supervisor and /admin. Kept off the person row so credentials
-- never ride along in a staff-profile query.
CREATE TABLE credentials (
    person_id     TEXT PRIMARY KEY REFERENCES persons(id) ON DELETE CASCADE,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);

-- --------------------------------------------------------- capabilities --

CREATE TABLE capabilities (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL UNIQUE,     -- "doors", "landing", "roam team" …
    -- false = hidden preference: tracked, never shown on the staff form.
    staff_selectable INTEGER NOT NULL DEFAULT 1 CHECK (staff_selectable IN (0, 1)),
    sort_order       INTEGER NOT NULL DEFAULT 0
);

-- Which roles may express a preference on a capability (security gets
-- roam team, doors, x-ray, press row, elevator, back of house, camera).
CREATE TABLE capability_roles (
    capability_id TEXT NOT NULL REFERENCES capabilities(id) ON DELETE CASCADE,
    role_id       TEXT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    PRIMARY KEY (capability_id, role_id)
);
CREATE INDEX capability_roles_role ON capability_roles(role_id);

-- Explicit willingness. Absence of a row means 'no_preference'.
CREATE TABLE person_capabilities (
    person_id     TEXT NOT NULL REFERENCES persons(id) ON DELETE CASCADE,
    capability_id TEXT NOT NULL REFERENCES capabilities(id) ON DELETE CASCADE,
    state         TEXT NOT NULL CHECK (state IN ('yes', 'no_preference', 'restricted')),
    updated_at    TEXT NOT NULL,
    updated_by    TEXT REFERENCES persons(id) ON DELETE SET NULL,
    PRIMARY KEY (person_id, capability_id)
);
CREATE INDEX person_capabilities_cap ON person_capabilities(capability_id, state);

-- Restriction reasons are a performance record: separate table so the
-- supervisor-only permission is a table-level decision, not a column filter.
CREATE TABLE restriction_notes (
    id            TEXT PRIMARY KEY,
    person_id     TEXT NOT NULL REFERENCES persons(id) ON DELETE CASCADE,
    capability_id TEXT NOT NULL REFERENCES capabilities(id) ON DELETE CASCADE,
    note          TEXT NOT NULL,
    recorded_by   TEXT REFERENCES persons(id) ON DELETE SET NULL,
    recorded_at   TEXT NOT NULL
);
CREATE INDEX restriction_notes_person ON restriction_notes(person_id, capability_id);

-- ---------------------------------------------------- events and types --

CREATE TABLE event_types (
    id          TEXT PRIMARY KEY,
    parent_id   TEXT REFERENCES event_types(id) ON DELETE RESTRICT,
    name        TEXT NOT NULL,
    sort_order  INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX event_types_sibling_name ON event_types(COALESCE(parent_id, ''), name);

CREATE TABLE events (
    id             TEXT PRIMARY KEY,
    event_type_id  TEXT NOT NULL REFERENCES event_types(id) ON DELETE RESTRICT,
    name           TEXT NOT NULL,
    start_at       TEXT NOT NULL,
    end_at         TEXT NOT NULL,
    -- Start of second shift; NULL until known. Drives the break window.
    second_shift_at TEXT,
    notes          TEXT NOT NULL DEFAULT '',
    published_at   TEXT,                       -- NULL = draft assignment
    CHECK (end_at > start_at)
);
CREATE INDEX events_start ON events(start_at);
CREATE INDEX events_type ON events(event_type_id);

-- ------------------------------------------------------------ resources --

CREATE TABLE resources (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,          -- "Radio", "Stop sign"
    description TEXT NOT NULL DEFAULT '',
    -- 1 = numbered instances on a rack (radios); 0 = issued/not issued.
    tracked     INTEGER NOT NULL DEFAULT 0 CHECK (tracked IN (0, 1))
);

-- Registered instances of a tracked resource. Issue records may also carry
-- an unregistered number: the system never blocks a radio entry.
CREATE TABLE resource_instances (
    id          TEXT PRIMARY KEY,
    resource_id TEXT NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
    number      TEXT NOT NULL,
    retired     INTEGER NOT NULL DEFAULT 0 CHECK (retired IN (0, 1)),
    UNIQUE (resource_id, number)
);

-- ------------------------------------------------------------ positions --

CREATE TABLE positions (
    id             TEXT PRIMARY KEY,
    name           TEXT NOT NULL UNIQUE,       -- one record per post: "East Fast Door 1"
    department_id  TEXT NOT NULL REFERENCES roles(id) ON DELETE RESTRICT,
    area           TEXT NOT NULL DEFAULT '',   -- print grouping: lane / area
    location       TEXT NOT NULL DEFAULT '',
    floor          TEXT NOT NULL DEFAULT '',   -- '100' | '200' | '300' | …
    shift_pattern  TEXT NOT NULL CHECK (shift_pattern IN
                   ('first_shift_only', 'second_shift_only', 'full_session', 'two_shift_door')),
    needs_breaking INTEGER NOT NULL DEFAULT 1 CHECK (needs_breaking IN (0, 1)),
    is_lead        INTEGER NOT NULL DEFAULT 0 CHECK (is_lead IN (0, 1)),
    -- Admin-reorderable rank; lower = more desirable.
    desirability   INTEGER NOT NULL DEFAULT 0,
    -- Nominal headcount, NOT a cap. Over-assignment is always legal.
    headcount      INTEGER NOT NULL DEFAULT 1 CHECK (headcount >= 0),
    -- Roam teams only. NULL on every other position.
    pairing_rule   TEXT CHECK (pairing_rule IS NULL OR pairing_rule = 'mixed_gender'),
    -- Optional general-arena-experience floor in half-years (6 = 3+).
    min_tenure_half_years INTEGER
                   CHECK (min_tenure_half_years IS NULL OR min_tenure_half_years BETWEEN 0 AND 6),
    -- Hook for future eligibility rules; JSON, unused by 0001.
    eligibility    TEXT NOT NULL DEFAULT '{}',
    notes          TEXT NOT NULL DEFAULT ''
);
CREATE INDEX positions_department ON positions(department_id);

-- Capabilities ↔ positions (reciprocal).
CREATE TABLE position_capabilities (
    position_id   TEXT NOT NULL REFERENCES positions(id) ON DELETE CASCADE,
    capability_id TEXT NOT NULL REFERENCES capabilities(id) ON DELETE CASCADE,
    PRIMARY KEY (position_id, capability_id)
);
CREATE INDEX position_capabilities_cap ON position_capabilities(capability_id);

-- Positions ↔ event types (reciprocal). Applies to the type and its subtypes.
CREATE TABLE position_event_types (
    position_id   TEXT NOT NULL REFERENCES positions(id) ON DELETE CASCADE,
    event_type_id TEXT NOT NULL REFERENCES event_types(id) ON DELETE CASCADE,
    PRIMARY KEY (position_id, event_type_id)
);
CREATE INDEX position_event_types_type ON position_event_types(event_type_id);

CREATE TABLE position_resources (
    position_id TEXT NOT NULL REFERENCES positions(id) ON DELETE CASCADE,
    resource_id TEXT NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
    quantity    INTEGER NOT NULL DEFAULT 1 CHECK (quantity >= 1),
    PRIMARY KEY (position_id, resource_id)
);

-- lead_of: a lead position over a pool of positions (a door lane, a lounge).
CREATE TABLE position_lead_pool (
    lead_position_id TEXT NOT NULL REFERENCES positions(id) ON DELETE CASCADE,
    position_id      TEXT NOT NULL REFERENCES positions(id) ON DELETE CASCADE,
    PRIMARY KEY (lead_position_id, position_id),
    CHECK (lead_position_id <> position_id)
);
CREATE INDEX position_lead_pool_member ON position_lead_pool(position_id);

-- Priority ladder / depth chart, per position. depth 1 = named primary,
-- 2.. = ordered backups. No cap on depth. Depth is kept dense by the
-- application; no UNIQUE on (position_id, depth) so reorders need no
-- temporary values.
CREATE TABLE position_queue (
    position_id TEXT NOT NULL REFERENCES positions(id) ON DELETE CASCADE,
    person_id   TEXT NOT NULL REFERENCES persons(id) ON DELETE CASCADE,
    depth       INTEGER NOT NULL CHECK (depth >= 1),
    added_at    TEXT NOT NULL,
    added_by    TEXT REFERENCES persons(id) ON DELETE SET NULL,
    PRIMARY KEY (position_id, person_id)
);
CREATE INDEX position_queue_person ON position_queue(person_id);

-- Effective preference = explicit state, OR'd with the depth-derived hidden
-- preference (depth 1 or 2 on ANY position carrying the capability).
-- Derived, never stored: a reshuffle on one post cannot strip a status
-- earned on another because there is nothing to recompute or forget.
-- 'restricted' always wins.
CREATE VIEW person_capability_effective AS
WITH derived AS (
    SELECT DISTINCT q.person_id, pc.capability_id
    FROM position_queue q
    JOIN position_capabilities pc ON pc.position_id = q.position_id
    WHERE q.depth <= 2
),
pairs AS (
    SELECT person_id, capability_id FROM person_capabilities
    UNION
    SELECT person_id, capability_id FROM derived
)
SELECT
    p.person_id,
    p.capability_id,
    CASE
        WHEN e.state = 'restricted'       THEN 'restricted'
        WHEN e.state = 'yes'              THEN 'yes'
        WHEN d.person_id IS NOT NULL      THEN 'yes'
        ELSE 'no_preference'
    END AS state,
    (d.person_id IS NOT NULL) AS depth_derived
FROM pairs p
LEFT JOIN person_capabilities e
       ON e.person_id = p.person_id AND e.capability_id = p.capability_id
LEFT JOIN derived d
       ON d.person_id = p.person_id AND d.capability_id = p.capability_id;

-- --------------------------------------------------- event staffing --

-- Who signed up for an event, and which supervisor currently holds them.
-- Handoff = change current_supervisor_id; home_supervisor_id is the return
-- tag and survives rehoming. NULL home tag = not handed off.
CREATE TABLE event_staff (
    event_id              TEXT NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    person_id             TEXT NOT NULL REFERENCES persons(id) ON DELETE CASCADE,
    current_supervisor_id TEXT REFERENCES persons(id) ON DELETE SET NULL,
    home_supervisor_id    TEXT REFERENCES persons(id) ON DELETE SET NULL,
    signed_up_at          TEXT NOT NULL,
    PRIMARY KEY (event_id, person_id)
);
CREATE INDEX event_staff_supervisor ON event_staff(event_id, current_supervisor_id);

-- Person → position → event, per shift. A door person has two rows.
-- Unfilled posts are positions without assignments, so they stay visible.
CREATE TABLE assignments (
    id          TEXT PRIMARY KEY,
    event_id    TEXT NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    position_id TEXT NOT NULL REFERENCES positions(id) ON DELETE RESTRICT,
    person_id   TEXT NOT NULL REFERENCES persons(id) ON DELETE RESTRICT,
    shift       TEXT NOT NULL CHECK (shift IN ('first', 'second', 'breaker')),
    -- matcher = auto; manual = admin/supervisor placement (always pinned,
    -- a matcher re-run fills only unpinned slots).
    source      TEXT NOT NULL CHECK (source IN ('matcher', 'manual')),
    pinned      INTEGER NOT NULL DEFAULT 0 CHECK (pinned IN (0, 1)),
    -- Roam pair containing an 'unknown' gender: flagged for a human.
    needs_confirmation INTEGER NOT NULL DEFAULT 0 CHECK (needs_confirmation IN (0, 1)),
    created_at  TEXT NOT NULL,
    created_by  TEXT REFERENCES persons(id) ON DELETE SET NULL,
    removed_at  TEXT,                          -- soft delete keeps the night's history
    removed_by  TEXT REFERENCES persons(id) ON DELETE SET NULL
);
-- Live rows only: a person can be removed from a post and placed back.
CREATE UNIQUE INDEX assignments_live ON assignments(event_id, position_id, person_id, shift)
    WHERE removed_at IS NULL;
CREATE INDEX assignments_event ON assignments(event_id, shift);
CREATE INDEX assignments_person ON assignments(person_id, event_id);

-- Overrides are always allowed, never blocked; they are recorded and
-- printed on the sheet.
CREATE TABLE assignment_overrides (
    id            TEXT PRIMARY KEY,
    assignment_id TEXT NOT NULL REFERENCES assignments(id) ON DELETE CASCADE,
    kind          TEXT NOT NULL CHECK (kind IN ('min_tenure', 'lead', 'restricted', 'pairing', 'placement')),
    by_person_id  TEXT REFERENCES persons(id) ON DELETE SET NULL,
    at            TEXT NOT NULL,
    note          TEXT NOT NULL DEFAULT ''
);
CREATE INDEX assignment_overrides_assignment ON assignment_overrides(assignment_id);

-- ------------------------------------------------------------- check-in --

-- One visit per person per event. Keyed on (event, person) rather than the
-- assignment because unassigned people check in too ("see reception").
CREATE TABLE checkins (
    id              TEXT PRIMARY KEY,
    event_id        TEXT NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    person_id       TEXT NOT NULL REFERENCES persons(id) ON DELETE RESTRICT,
    checked_in_at   TEXT NOT NULL,
    checked_in_location  TEXT NOT NULL CHECK (checked_in_location IN ('kiosk', 'reception')),
    checked_in_by   TEXT REFERENCES persons(id) ON DELETE SET NULL,
    checked_out_at  TEXT,
    checked_out_location TEXT CHECK (checked_out_location IS NULL OR checked_out_location IN ('kiosk', 'reception')),
    checked_out_by  TEXT REFERENCES persons(id) ON DELETE SET NULL,
    UNIQUE (event_id, person_id),
    CHECK (checked_out_at IS NULL OR checked_out_at >= checked_in_at)
);

CREATE TABLE resource_issues (
    id              TEXT PRIMARY KEY,
    checkin_id      TEXT NOT NULL REFERENCES checkins(id) ON DELETE CASCADE,
    resource_id     TEXT NOT NULL REFERENCES resources(id) ON DELETE RESTRICT,
    -- Registered instance when known; instance_number always holds what was
    -- entered, including free-typed unregistered numbers.
    instance_id     TEXT REFERENCES resource_instances(id) ON DELETE SET NULL,
    instance_number TEXT,
    issued_at       TEXT NOT NULL,
    issued_by       TEXT REFERENCES persons(id) ON DELETE SET NULL,
    returned_at     TEXT,
    returned_by     TEXT REFERENCES persons(id) ON DELETE SET NULL,
    CHECK (returned_at IS NULL OR returned_at >= issued_at)
);
CREATE INDEX resource_issues_checkin ON resource_issues(checkin_id);
-- Outstanding items: the unreturned report and the "available radios" list.
CREATE INDEX resource_issues_outstanding ON resource_issues(resource_id, returned_at);

-- -------------------------------------------------------------- banners --

CREATE TABLE banners (
    id              TEXT PRIMARY KEY,
    text            TEXT NOT NULL,
    -- The "this event only" flag: controls reuse (drop-down pool), not scope.
    this_event_only INTEGER NOT NULL DEFAULT 0 CHECK (this_event_only IN (0, 1)),
    -- Audience: everyone, or the roles in banner_roles (any hierarchy level).
    all_roles       INTEGER NOT NULL DEFAULT 0 CHECK (all_roles IN (0, 1)),
    created_at      TEXT NOT NULL
);

CREATE TABLE banner_roles (
    banner_id TEXT NOT NULL REFERENCES banners(id) ON DELETE CASCADE,
    role_id   TEXT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    PRIMARY KEY (banner_id, role_id)
);
CREATE INDEX banner_roles_role ON banner_roles(role_id);

-- When: attached to an event type (default, persists) or a single event
-- (override for that event). No validity windows.
CREATE TABLE banner_event_types (
    banner_id     TEXT NOT NULL REFERENCES banners(id) ON DELETE CASCADE,
    event_type_id TEXT NOT NULL REFERENCES event_types(id) ON DELETE CASCADE,
    PRIMARY KEY (banner_id, event_type_id)
);
CREATE INDEX banner_event_types_type ON banner_event_types(event_type_id);

CREATE TABLE banner_events (
    banner_id TEXT NOT NULL REFERENCES banners(id) ON DELETE CASCADE,
    event_id  TEXT NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    PRIMARY KEY (banner_id, event_id)
);
CREATE INDEX banner_events_event ON banner_events(event_id);

-- ------------------------------------------------------------- settings --

-- Admin-configured key/value settings (backup schedule, etc.). JSON values.
CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
