# Lenovo Staff Manager (LSM) — Build Spec

LSM is a staff scheduling and check-in system for Lenovo Center (Carolina Hurricanes arena), intended as an alternative to ABI MasterMind. It is a real potential product, not a hobby piece: build it with the structure and care of something that will be handed to an operations department.

**Immediate goal:** a working demo on a laptop with a USB barcode scanner, within one to two weeks.

**The pitch:** check-in is the plumbing. The feature that sells this is **second-shift coverage** — showing, per event, who has no second-shift post and how many staff will be idle once the break rotation ends. Today staff learn their second role on the night, and many have none. The arena has just merged security and event services into one floating group under a cost mandate; this tool makes the idle capacity visible.

---

## 1. Stack and architecture

- **Server:** Go. A single binary serves the JSON API and the static front end.
- **Front end:** plain web pages, usable on laptop, iPad and iPhone. No native app, no heavy SPA framework required. Keep it server-friendly and fast.
- **Database:** your choice, but it must run inside a single Docker container for the demo. SQLite is acceptable for the demo if the data layer is cleanly abstracted; Postgres is fine too.
- **IDs:** UUIDs on every entity from day one — never auto-increment. Export/re-import must never collide.
- **Serialisation:** every entity must be exportable to and importable from a structured format (JSON). This is the backup format and the migration path off ABI.

### Deployment
- Runs locally via Docker, and the same image deploys to an EC2 instance. No Fargate.
- Demo/interim: single container with everything in it.
- Scheduled backup exports are configured from the admin pages and written to a mounted secondary volume (EC2 can snapshot that disk).

### Repo layout
```
cmd/            single server entry point
internal/
  staff/        people, roles, capabilities
  shifts/       events, event types, positions, assignments, matcher, breaks
  checkin/      check-in records, resources, issue/return
  archive/      backup export and restore
  config/       configuration
  http/         routes and middleware
web/            front end assets
migrations/
deploy/         Dockerfile + compose
seed/           demo seed data (staff.json provided)
```

### Makefile — exactly six targets
`build`, `run`, `test`, `migrate`, `image`, `clean`.
Lint, vet and asset steps run as dependencies of these; they are **not** separately callable targets. Do not add further targets.

---

## 2. Data model

### Person
- `id` (UUID), `badge` (1D barcode string, unique lookup key), `name`, `photo` (optional)
- `role` → Role (hierarchical, see below)
- `tier` — enum: `staff`, `supervisor`, `admin` on the person record. Everyone checks in and out regardless of tier. **Lead is a fourth tier operationally but is not stored here** — see below.
  - **staff** — the majority.
  - **lead** — runs a group of roughly 10 to 20 people. **Lead is a property of the position, not the person**: there is no lead flag on a person record. Whoever is assigned a lead position for an event (each of the six door sets — East/West/South fast and slow — has one, as do the priority lounges) is a lead for that night, and carries it into their second shift, where door-set leads become the leads of the roam teams. Seed data therefore contains no leads.

    **Filling lead positions.** The matcher fills a lead position automatically by picking the **most tenured** eligible person already assigned to that group. This is a default, not a rule — a supervisor or admin can override it and name anyone, and the override is recorded like any other. Tenure is a reasonable first guess, but it is not the same thing as suitability.
  - **supervisor** — around ten on duty on a given night; owns an area and uses the `/supervisor` phone page.
  - **admin** — full access, including fields hidden from every other tier.

  This hierarchy exists in Event Security and is roughly mirrored in Event Services, which is the primary target of the system.
- `hire_date` — store this; **compute** tenure. Tenure is displayed in half-year bands: 0, 0.5, 1, 1.5, 2, 2.5, 3+
- `gender` — enum: `male`, `female`, `unknown`. Defaults to `unknown` when not supplied. Recorded solely to satisfy the roam-team pairing rule. **Roam teams are the only gender-specific positions in the system**: a roam pair must be one male and one female, because they have to enter both bathrooms. Every other position is gender-neutral, and the matcher must ignore `gender` when filling them.
  - **Visibility: admin only.** `gender` is not shown to staff, leads or supervisors — not on any sheet, not on a person's own profile. Supervisors pairing a roam team work from the people in front of them; if they get it wrong, the person says so. The matcher uses the field; humans below admin never see it.
  - `unknown` means the value hasn't been recorded, not a third category. For roam teams, treat an `unknown` person as eligible for **either** half of a pair, and surface the pair in the coverage view so a human can confirm it. Never exclude someone from a post because the field is unset.
- Per-position willingness (see Capabilities)

### Role (hierarchical)
Top level: Event Security, Event Services, Vendor/Concessions, Parking, Professional Services, Medical. Sub-roles nest beneath (e.g. Vendor → specific outlet). Anything set at a level (e.g. a banner) applies to everything beneath it.

### Event type (hierarchical)
Hockey, Concert, Comedy, College Sport, plus one-offs (e.g. a medical conference). Events belong to a type.

### Event
`id`, `name`, `event_type`, `start`, `end` (or duration), plus notes.

### Position
Admin-defined and dynamic. Each door slot is **its own position record** (e.g. "East Fast Door 1", "East Fast Door 2") — no slots-within-positions. The receptionist's list is one row per post.

Fields:
- `name`, `department` (Role), `location`/`floor`
- `shift_pattern`: `first_shift_only` | `second_shift_only` | `full_session` | `two_shift_door` (door → redeploy)
- `required_resources` → Resource types
- `needs_breaking` (bool). Roam teams = **no** (three teams per floor cover each other). Most fixed posts = yes.
- `desirability` — admin-reorderable rank
- `applicable_event_types` (e.g. doors/ushers/ticket takers always; barricade only for concerts)
- `lead_of` — optional link making this a lead over a pool of positions
- `pairing_rule` — optional; used by roam teams only (one male + one female). No other position sets this.
- Optional eligibility rules (e.g. minimum tenure) — design the hook, don't overbuild

### Assignment
Person → Position → Event, with shift (first / second / breaker). A door person has two assignments in one event.

### Priority ladder per position
When filling a position, candidates are ranked:
1. Named primary
2. Ordered first / second / third backups
3. Anyone who opted **in** to that capability
4. Anyone with a matching role/type (no preference)

Never assign anyone **restricted** on that position. This captures real practice (the same person always on the south door x-ray) without hardcoding it, and makes informal favouritism visible.

### Capabilities / willingness
Every position type defined by the admin appears on every staff profile with a **ternary state**: `yes` / `no preference` / `restricted`.
Use the word **"restricted"**, never "barred". Any reason attached to a restriction is effectively a performance record: store it separately and make it **supervisor-visible only**. Build this permission in from the start.

Current capabilities: doors, ticket taker, usher, roam team, camera, elevator, back of house, x-ray, landing, press row. Usher and ticket taker are separate. The stop sign is a resource, not a role.

**Which capabilities staff can express a preference on.** Today the arena's sign-up sheet offers security staff only two options — **roam team** and **doors**. LSM extends the selectable list for security with **x-ray, press row, elevator and back of house**: posts with enough slots to be worth asking about and plausible for a supervisor to grant.

Security's selectable list is therefore: **roam team, doors, x-ray, press row, elevator, back of house, camera**.

### Hidden preferences

Some capabilities are tracked as preferences but **never shown to staff**. Capabilities carry a flag:

- `staff_selectable` (bool) — false means the capability does not appear on the staff preference form at all.

Currently hidden: **lounges** and **landings**. The arena will not let staff choose these; they go to tenured people. Offering a choice that is never honoured is worse than not asking. But the preference is still *recorded*, so fairness reporting can measure the queue for premium posts even though the queue was never advertised.

Visibility: supervisors and admins can see hidden preference values. Staff cannot — not on their own profile, not anywhere.

### Depth charts

Premium posts have a ranked queue rather than a single assignee. Per **position** (not per capability), staff are ranked at a depth:

- depth 1 = primary, depth 2 = secondary, depth 3+ = further backups (no cap on depth).

**Depth 1 or 2 automatically sets the hidden preference for that position's capability. Depth 3 or lower does not.**

The preference is derived as an **OR across every position the person is ranked on**. Worked example: a person is primary on the 112 landing and tertiary on the 118 landing. Primary on 112 sets their hidden *landings* preference; tertiary on 118 would not have. The preference is set, because at least one qualifying rank exists.

Consequences the implementation must honour:

- Recompute the derived preference whenever any depth entry for that person changes. Moving someone from primary to fourth on 118 must **not** clear their landings preference while they remain primary on 112.
- Store depth **per position**, and keep that detail behind the scenes. The staff-facing and report-facing concept is the capability ("landings"); which specific landing someone is ranked on is supervisor-level detail.
- Never let a reshuffle on one post silently strip a status earned on another. This is the failure mode Jason called out explicitly.

### Minimum experience on a position

Positions carry an optional **`min_tenure`** field, expressed in the same half-year increments as the person record (0, 0.5, 1, 1.5, 2, 2.5, 3+). It means *general* experience at the arena — **not** experience in that specific role.

Example: a landing is not normally given to anyone with under two years.

Behaviour:

- The **matcher treats it as a filter** — people below the threshold are not auto-assigned to the position.
- A **supervisor or admin can override it** and place someone anyway. The override is always allowed; do not block it and do not nag.
- **Record who overrode it and when**, and surface the override on the printed sheet so it is traceable on the night.
- Leave `min_tenure` unset on most positions. It is the exception, not a field to populate everywhere.

**How `min_tenure` interacts with the fairness report.** The report treats the threshold as a **state, not a filter** — it never silently drops people below the line. For each person, for each capability they have expressed a preference on (visible or hidden), classify into one of three buckets:

1. **Wants it, not yet eligible** — below `min_tenure`. Not a fairness problem. Show the date they cross the threshold; someone about to become eligible is useful to see coming.
2. **Wants it, eligible, not getting it** — the only bucket that is a question. This is the report's actual signal.
3. **Wants it and getting it** — no action.

This matters because `min_tenure` can be abused. A high threshold is a defensible-sounding way to reserve a post for the same few people, and the report cannot distinguish a genuine requirement from a convenient one. Keeping bucket 1 visible rather than filtered means a threshold that is quietly parking people stays in view.

### Queue depth is unlimited

There is **no cap on queue length**. When a supervisor wants someone considered for a position, they add them to that position's queue and they join at the back. A person may sit on the queue for every landing at once.

A queue is a **fallback ladder, not a roster**. Depth 1 or 2 buys two things and nothing more: the hidden preference gets set, and you get first refusal. It never guarantees the post.

### Resolving premium posts on the night

For a capability group (e.g. all landings), given who actually turned up:

1. The candidate pool is **everyone present who is queued on any position in that group**, at any depth.
2. Work down each position's queue in order, filling from the top.
3. When a candidate is queued on several positions in the group, **prefer a position they are actually ranked on**. If none of theirs is still open, assign them to **any available position in the group** — their ranking got them into the pool, it does not bind them to one post.
4. If the queues do not yield enough people, the remaining posts **fall through to open assignment**: anyone with the matching capability, per the normal matcher rules.

Worked example: someone is fifth on every landing except 118. Only three queued landing people show up. All three are placed, and the fifth-place person is pulled in to fill a fourth landing — the depth gap is irrelevant once the people above them are absent. Any landing still unfilled after that is simply open.

### Resources
- Admin-defined: `name`, `description`, `id`
- `tracked` (individual numbered instances, e.g. radios on a numbered rack) vs `untracked` (binary issued/not issued, e.g. stop signs)
- Positions declare required resources; the check-in form derives its fields from the assigned position
- Issue record: resource, instance number (if tracked), issued time, returned time, issued-by

### Check-in record
Hangs off the assignment: `checked_in_at`, `checked_out_at`, `recorded_by`, `location` (kiosk/reception), plus dynamic resource fields. Radio is one configurable field, not a hardcoded column.

### Banners
- Top-of-screen message; eye-catching but clearly **not** an error state
- Two independent axes: **who** (Role, at any level of the hierarchy, or everyone) and **when** (event type, or a single event)
- On creation, pick from existing banners in a drop-down or free-type; predefining is not required
- Example: "Staff meeting 5:30 East Priority" for Security and Services on all hockey events

**Precedence, not validity windows.** Banners have no start or end dates and no scheduling.
- A banner attached to an **event type** (e.g. hockey) is the default for every event of that type and persists until changed. Removing it means un-specifying the attachment.
- A banner attached to a **single event** overrides the event-type banner for that event. Once the event completes the override lapses, and the event-type banner takes over again.
- Resolution at display time: most specific wins — event banner, else event-type banner (walking up the type hierarchy), else none.

**The "this event only" flag controls reuse, not scope.** Its single job is whether the banner joins the reusable pool.
- **Set** — used once; does not appear in the drop-down for future events.
- **Unset** — joins the pool of selectable banners for reuse later, but still applies only to the event it was attached to.

Either way an event-level banner applies to that event alone. The flag decides only whether the text is offered again.

### Admin principle — reciprocal many-to-many
Every many-to-many mapping must be editable **from both ends** over the same underlying table: positions ↔ event types, capabilities ↔ positions, banners ↔ roles. Open a position and tick event types, or open an event type and tick positions.

---

## 3. Screens

Distinguish screen modes by URL — no separate build or config on the night.

### `/kiosk` — non-reception entrances
- Single text field with permanent focus. The scanner types the badge number + Enter. No mouse.
- Shows photo, name, time, and only the valid action (check in or check out).
- After check-in, show one of:
  - the person's initial assignment with a "go to your post" message
  - **"Resources required — see reception"** if the post needs anything issued
  - **"No assignment — see reception"** if unassigned (never a blank screen)
- Resets after about two seconds.

### `/reception`
- Same always-focused scan field.
- **Idempotent about arrival:** if already checked in at a kiosk, show "Checked in at 6:42" and go straight to the resource step; otherwise check in and issue in one action.
- Confirmation shows name, photo, position, banner, and resource fields derived from the position.
- **Radio picker:** first available / pick a specific number / free-type an unregistered number. The system must **never block** a radio entry. Issued instances drop off the available list.
- **Check-out** shows what was issued so return can be confirmed.

### `/admin`
One screen with sections rather than sprawling pages: events and event types, positions, capabilities, resources, roles and banners, staff profiles, backup/restore.

### `/supervisor` — on-the-night adjustments (phone)
A mobile-oriented page for leads and supervisors, used on the venue network. Basic username/password auth for now.

**Purpose:** the printed sheet and the pencil marks stay. This page is where a supervisor transcribes those changes when they get a quiet minute, so the system catches up with reality instead of fighting it.

- Opens on **their own area's posts only**: one row per post, showing post and assigned person. Phone-shaped, minimal chrome, works one-handed in a corridor.
- Must be brutally simple. A supervisor mid-game will not navigate an admin UI.

**Moving people across areas — handoff, not placement.** A supervisor does not assign into another area's posts (they don't know them). Instead:
- Supervisor A picks a person and **assigns them to another supervisor** (B), not to a position.
- The person leaves A's list and appears in **B's unassigned** bucket on B's next refresh.
- B places them into one of their own posts.
- The handoff is **immediate** — no acceptance step. It mirrors the radio call that already happened.

**The return tag.** When a person is handed off, they carry a tag pointing at their **home supervisor** (A) — the supervisor they were originally allocated under.
- The tag **survives rehoming**: if B passes them to C, the tag still points at A.
- A small, unobtrusive button beside the person reads as "return to home supervisor". B or C can press it at any time; the person goes back to **A's unassigned**.
- Manually assigning them back to A has the **same effect** as pressing the button.
- Either way the tag is then **cleared** and the button disappears.
- A returned person lands in A's **unassigned** — never back in their original post, because A may have already backfilled it. It is then A's job to place them.

**Over-assignment is legal.** A supervisor may assign a second (or third) person to a post that nominally holds one. The system must **never reject** this. Real case: the barricade in front of a stage often needs more bodies than were allocated.
- Show filled-versus-established as a discreet but clearly intentional value beside each post: **`0 of 1 filled`**, **`1 of 1 filled`**, **`2 of 1 filled`**.
- Same notation for under, exact and over, so the eye catches a mismatch without anything shouting. `0 of 1` is a gap; `2 of 1` is deliberate reinforcement.
- Positions therefore carry a **nominal headcount, not a cap**. The coverage view must distinguish established headcount from actual, so surplus and shortfall both stay visible.

**Known gap (accepted for now):** pencil changes made on paper and never transcribed are not captured. See §5 — fairness reporting measures *allocation*, not what was actually worked, and must say so.

### Reports
- **End-of-night unreturned resources** — matches how the radio rack is reconciled. Strong demo moment.
- **Second-shift coverage** (per event) — see §5.

---

## 4. Arena positions (seed as data, not code)

Counts below are Jason's current best estimates. Real deployment sheets run two to three A4 pages (~100–150 lines), so this list is **incomplete** — the admin UI must make adding positions trivial.

### Security — first shift (door sets): 45 people
Six lanes: East Fast, East Slow, West Fast, West Slow, South Fast, South Slow. Each lane:
- 4 doors × 1 person (Door 1–4)
- 2 wanders
- 1 lead
- X-ray: +1 on East Fast, West Fast, South Slow only

**Door 1 on each lane stays for the whole game ("door after door").** The rest redeploy at second shift. Door leads become one half of a roam team.

### Security — full session: 31 + back of house
- Landings: NE, SE, NW, SW (4)
- Main elevator (1)
- Press row north, press row south (2)
- North glass (2)
- East Priority Lounge: lead, backup lead, 4 staff (6)
- West Priority Lounge: lead, backup lead, 4 staff (6)
- Back of house (named so far, ~12; more exist):
  back-of-house elevator (1, separate from the main elevator), referee door (1), away players (2), victory bar (2 — a bar position and a ticket-checker security position), home team door (1), press box (1, distinct from press row), ice box (1), away corridor (3)

### Security — second shift: 30
- Roam teams: 3 teams per floor on floors 1 and 3 (none on floor 2), 2 per team = 12. Always one male + one female — the only gender-specific positions in the system. `needs_breaking = false`.
- Ice store (2), two smaller stores (1 each) — intermission guarding
- Plaza doors (12) — stop people opening doors and letting others in
- View bar, 3rd floor (2)

### Event services
- Stop-sign usher: one per seating section (seed ~90). Seat people before the event, then hold the stop sign. Requires a stop sign (untracked resource).
- Corridor ushers (20)
- 2nd-floor staircase posts (10)
- Ticket takers: one per door (24) — free up at second shift

### Radio rules
Every lead, every landing, and Door 1 on each lane carries a radio.

### Arena structure
Three levels: 100 (lower bowl), 200 (club), 300 (upper).

---

## 5. Second shift, breaks and the coverage view

### Break rules
- Breaks are 20 minutes. One breaker covers three posts.
- Breaks run in roughly a one-hour window from the start of second shift.
- **No breaks in the last hour** of an event.
- One break per person. (A multiple-breaks parameter was considered and rejected — it pushes breaks to the end.)
- Only positions with `needs_breaking = true` get a breaker.
- **Each department breaks its own.** Services ticket takers break the services fixed posts; security breaks security. If services can't cover itself, security spills over to help.
- Anyone with no other second-shift role becomes a breaker for the break hour.

### Coverage view (the headline feature)
After allocation, per event, show:
1. **People with no second-shift post** — a gap to fill (list them).
2. **People idle after breaking** — a count; a staffing decision for management.

Present facts, not editorialising. In a hockey game the break hour leaves roughly another hour and forty minutes, which is when staff with nothing to do are currently told to make themselves scarce.

Reference numbers with the current (incomplete) post list, security only: 45 on door sets → 6 stay → 39 redeploy; 30 second-shift posts → 9 spare; ~55 posts need breaking → ~19 breakers needed. These will change as posts are added; the view must compute them, never hardcode them.

### Matcher
- Sort posts by scarcest capability first.
- Prefer opted-in over no-preference; never place restricted staff.
- Newer staff to doors and less desirable posts; experienced staff to desirable posts.
- Respect the priority ladder and pairing rules. Apply the gender-pairing constraint to roam teams **only**; every other position is gender-neutral. An `unknown` gender is eligible for either half of a roam pair and is flagged for human confirmation, never excluded.
- Leave unfillable posts **visibly empty** — never silently drop them.
- Assume ~70% of staff sign up for a given event.

### Assignment workflow (decided)
1. The matcher **auto-fills** a complete draft assignment for the event.
2. An admin reviews it on screen and can **override any assignment** before publishing. Overrides are manual placements that the matcher must not silently undo on a re-run — re-running the matcher fills only unpinned slots.
3. The published assignment is **printed** and handed to the leads/supervisors.
4. Leads make **pencil adjustments on the night**. The system is not the source of truth once the sheet is printed — the paper is.

Implication: the printed sheet is a first-class output, not an afterthought. It must be legible, one row per post, grouped by area/lane, with space to write. Leads should be able to reconcile pencil changes back into the system afterwards (or not at all — don't force it).

---

## 6. Backup and restore
- Scheduled export (JSON archive) configured from admin, written to a mounted volume.
- Restore is an **admin web page**, not a script: choose an archive and import; recovery mode can overwrite everything.
- Full-overwrite restore requires a **typed confirmation** and takes an **automatic pre-wipe export** first.
- Deprioritised for the demo, but the architecture must support it (UUIDs, serialisable entities).

---

## 7. Seed data
- `seed/staff.json` — 521 staff with badge, name, role, sub_role, tier, hire_date, tenure_half_years, capabilities, gender, and login fields.
  - Event Security 201, Event Services 200, Concessions 50 (five spoof outlets: Chick-fil-Yay, Windy's, Burgers R Us, Pizzas R Us, Ice Creams R Us), Parking 30, Professional Services 30, Medical 10
  - `tier` is one of `staff` (494), `supervisor` (24), `admin` (3). There is no lead tier — lead is a property of the position, not the person (section 3). 40 restricted flags scattered for demo purposes.
  - `gender` is `male` (287), `female` (208) or `unknown` (26). First names match gender so the roam-team pairing rule has real pairs to work with. **Admin-visible only** — never shown to leads or supervisors, never on a printed sheet, never on a person's own profile.
  - Badge **100000** is Jason Figge, a real record (`"real": true`), seeded as `admin` so admin-only views can be demoed with his own badge. His physical badge will scan in the demo.
- `seed/events.json` — 43 events across four months, each with four shift windows (13:30, 14:00, 14:30, 15:00 starts, all ending 21:30). June, July and August 2026 are `closed`; October 2026 is `open`. September is deliberately empty so the open month is unambiguous.
- `seed/availability.json` — one row per person per event (`person_badge`, `event_id`, `window_id` nullable, `status` of `not available` / `all shifts` / `window`). Roughly 25 percent of staff sit at about 50 percent attendance, 25 percent at 70 percent or above, the rest in between — so the 60 percent expectation and the availability-rate rule both have something to bite on.
- `seed/ratings.json` — historical supervisor ratings against closed events only, attributed to a seeded supervisor or admin badge. Roughly 8 percent of staff carry a poor history, 15 percent an exceptional one, the rest above average. This is what makes the capping demo produce a defensible ranking rather than an arbitrary one.
- **Availability and ratings reference people by `person_badge`, not UUID.** The loader resolves badge to person ID on import.
- **Required headcount per event is not seeded.** Capping needs it — either default it per event type or add it to `events.json` during step 3.
- **Badge numbers persist across seasons** — a badge follows the person year to year rather than being reissued with the annual sticker. So badge is a stable attribute, with no badge-history table and no date-scoped join to resolve an old check-in. Two things to hold to anyway:
  - Badge number is **not the primary key**. Person keeps its UUID; badge is a unique indexed attribute. Persistent is not immutable — badges get lost and reissued, numbers get typo'd, and years of check-in and ratings history hanging off a natural key you cannot change is a problem you only discover once.
  - Lookup is **badge-to-person, not badge-as-person**. The kiosk parses a scan, resolves it to a person, and reports "badge not recognised" distinctly from "person not scheduled tonight" — different problems on the night, and reception has to tell them apart.
  - **Symbology and field length are not yet known.** Jason's badge carries a 1D barcode with no human-readable interpretation line printed beneath it, which is itself a finding: the real badges assume a scanner, not manual entry. Confirm the symbology and whether the payload is fixed-width numeric or variable-length alphanumeric before writing scan validation, and keep manual lookup by name in `/reception` as a first-class flow for the night a badge will not scan or is left at home.
- Positions: seed from §4.
- **Do not generate `seed/events.json` — it is supplied.** `availability.json` and `ratings.json` reference its event and shift-window IDs, so regenerating or reshaping it breaks both. Load it as-is. Its event names and dates are synthetic, chosen for demo history; they are not the real schedule.
- **Real 2026-27 schedule: later, and additive.** The Hurricanes home schedule and Lenovo Center shows (e.g. Jonas Brothers Oct 1, Weezer Oct 4, Johnny Blue Skies Oct 21, Motionless In White Nov 3, beabadoobee Nov 11) may be added as further events once verified. Add them as new records; never replace the supplied ones.
- Seed is loaded via a `migrate`-adjacent step, not a separate Make target.

---

## 8. Build order for the demo

**Ordering principle: the supply side comes before the matcher.** Signup capping (section 10.3) is the headline fix for overstaffing, and everything from the matcher onwards consumes the roster it produces. Building it late would mean building the matcher twice.

1. Scaffold: repo layout, Makefile, Dockerfile, migrations, UUID base types, JSON export for every entity.
2. Core model + seed loading. Now includes **events, shift windows, availability and ratings history** alongside people, roles, positions, event types and resources — `seed/events.json`, `seed/availability.json` and `seed/ratings.json` as well as `seed/staff.json`.
3. Availability and signup API: open a month, record availability against shift windows, and the **weighted ranking and cutoff** of section 10.3, with per-person explainability.
4. `/admin` for the above: open and close a month, set required headcount, edit rule weights with the before-and-after preview, override the cutoff for a named person.
5. iOS app, first build: sign-in and Keychain/biometrics, then **Home, Schedule and Availability** functional, Time and More as placeholders.
6. `/kiosk` and `/reception` scan flows with resource issue and check-out return.
7. End-of-night unreturned resources report.
8. Matcher + assignments for an event, consuming the capped roster from step 3.
9. Second-shift coverage view and break allocation.
10. Remaining `/admin` sections (reciprocal many-to-many editing), including position add/edit/delete — see section 11, adding a missing post live is a demo feature.
11. Supervisor tab in the app (section 10.1b) and the end-of-night ratings flow.
12. Backup/restore (architecturally supported; UI can follow the demo).

Steps 1 and 2 are unchanged from the original order, so work already completed against them still stands.

Out of scope: payroll integration (reproduce ABI's export file format instead, later), Android.

---

## 10. Staff availability and signup (mobile)

This is the **supply side** — everything above assumes a roster already exists for the night. This section is how it gets there.

### 10.1 The app

- **iOS only for now**, built as a **developer app** side-loaded onto Jason's own iPhone. Android comes later.
- **Sign in once, then biometrics.** The person signs in with username and password on first launch only. The app then stores a long-lived token in the **iOS Keychain** and gates its retrieval behind **Face ID / Touch ID** on each subsequent launch, with device passcode as the fallback. Day to day the person opens the app and is straight in.
  - Still keep the surrounding account machinery minimal: no onboarding flow, no self-service password reset, no account management. Admin sets credentials.
  - **Seed credentials.** Every staff record carries `email` (`first.last@lenovo.com`, numeric suffix on collision), `password_hash`, and `must_change_password: true`. The seeded password is first initial plus surname, lowercase — **demo convenience only**. It is derivable from the email address, so every account is guessable by anyone who knows the naming convention, and these accounts write performance records. Force the reset on first login and never adopt this scheme in production. `initial_password` is present in the seed file for demo purposes and must not exist in the real system.
  - This gives the supervisor tab and the performance ratings a real identity to attribute actions to.
- **One Make target**, to build locally. Nothing else — no distribution, no signing pipeline, no TestFlight.
- Talks to the same Go server over the same API.
- **Tabbed.** Staff see the availability/signup tabs. A **supervisor tab is shown only to supervisors** and carries the functionality previously specced as the `/supervisor` mobile web page (§3): their area's posts, one row per post, handoffs, return tags, fill state, and the end-of-night ratings slider (§10.4). Contents to be detailed — Jason has examples to supply.
- **Supersedes the separate `/supervisor` web page.** Build the supervisor tools in the app rather than as a mobile web page. Keep the server-side API identical either way, so the web page remains possible as a fallback.
- Identity for the supervisor tab and for ratings comes from the signed-in token above.

### 10.1a Staff tab structure (modelled on ABI's app)

Bottom tab bar, in this order: **Home, Schedule, Availability, Time, More**. Header carries a messaging icon and a profile icon. The supervisor tab (above) is added only for supervisors.

**First build: every tab exists, but only Home, Schedule and Availability are functional.** Time and More are placeholder screens with a title and "coming soon" so the app looks complete in the demo. Availability is functional because the signup-capping story (section 10.3) is the headline fix for overstaffing, and it only lands if the supervisor can watch a signup go in on the phone and then see the cutoff applied on the admin pages.

- **Home (functional)** — welcome card showing the person's name and the current banner/announcement (reuse the banner precedence rules, §3); **Your upcoming shifts** (date block, time range, area, event name, "in N days"); a training-expiry card (placeholder content is fine).
- **Schedule (functional)** — month calendar with a list/calendar toggle; days with a shift are marked; **My shifts** below the calendar, each expandable, with **Add to Calendar** (write to the iOS calendar via EventKit); a **Department notes** card.
- **Availability (functional)** — Events / Exceptions / General sub-tabs; Events is the one that must work. Month navigation, a message card carrying the 60 percent expectation and the person's current rate for the month, then one card per open event: date, event name, and a dropdown offering Not Available, All Shifts, or each named shift window. Selection writes through immediately. Exceptions and General may be placeholders in the first build.
- **Time (placeholder)** — eventual design: pay periods, each expanding to in / out / hours per event, derived from LSM check-in records. Display only; payroll stays out of scope.
- **More (placeholder)** — eventual design: Training (active/completed, completed date, renewal required, due date, "Expires soon" badge) and Documents (list of policy PDFs).
- **Messaging (placeholder, from header icon)** — eventual design: Messages, Contact Scheduler (free-text send to the department), Notifications (schedule reminders).

### 10.1b Supervisor tab (proposed — no reference screens available)

ABI has no supervisor equivalent to copy, so this is designed from the workflow in section 3. Treat it as a first proposal, not a settled design.

Four sub-tabs: **Posts, People, Ratings, Notes**.

**Posts** — opens here by default. One row per post in the supervisor's own area only, grouped by area sub-section, ordered as on the printed sheet. Each row: post name, fill state as `1 of 1` / `0 of 1` / `2 of 1` (under, exact and over all use the same notation — over-assignment is legal and never rejected), and the assigned names. Tapping a row expands it to add or remove people. Everything is one-handed and thumb-reachable; this is used standing in a corridor.

**People** — the supervisor's roster for the night in three buckets:

- *Unassigned* — arrived, no post yet. Includes people handed over from another supervisor.
- *Assigned* — with their current post.
- *Not checked in* — expected but not yet scanned.

Each person row carries an overflow action to **hand off to another supervisor**: pick supervisor B from a list, and the person leaves this list immediately and appears in B's unassigned bucket on refresh. No acceptance step — it mirrors the radio call that already happened. A handed-off person shows a **return tag** naming their home supervisor, which survives rehoming (A to B to C still reads A), with a small return button that sends them back to A's unassigned. Assigning them back to A manually does the same thing; either way the tag clears.

**Ratings** — the end-of-night slider list from section 10.4. One row per person worked tonight, five-detent slider defaulted to *acceptable*, untouched rows recorded as acceptable. A single Submit at the bottom. Unavailable until the event is underway; prompted at check-out time.

**Notes** — free-text log against the event, timestamped and attributed. This is where "Jones went home sick at 8" lands so it is not lost.

Throughout: no typing where a tap will do, large touch targets, and every change writes through immediately rather than batching behind a save — a supervisor will lock their phone mid-task.

### 10.2 Opening a month

- An admin **opens a month** for scheduling. Events and their dates already exist in the system (§2), so opening a month exposes that month's events to staff.
- On open, staff are **emailed** that the month is available.
- Staff open the app and see **a list of available dates/events**, and mark their availability against each.
- **Availability is not yes/no.** ABI offers, per event: *Not Available*, *All Shifts*, or a specific **shift window** (e.g. 1:30–9:30, 2:00–9:30, 2:30–9:30, 3:00–9:30 pm). So an **event has one or more shift windows**, and a signup is for a window (or all). Model this: `shift_window` (id, event_id, start, end) and `availability` (person_id, event_id, window_id nullable = all, status).
- The arena already publishes the policy that **staff are expected to sign up for at least 60% of events each month** — it is printed on ABI's availability screen. The availability-rate ranking rule (§10.3) enforces an existing, known policy; make the 60% threshold admin-configurable and show it on the Availability tab the same way.

### 10.3 Capping signups — the core change

**Today, essentially everyone who signs up works the event.** Headcount is therefore whatever turns up rather than what the event needs, and this is a major source of the overstaffing and idle-staff problem the rest of this spec deals with downstream. LSM caps it.

Signups are **ranked**, and the cutoff falls where the event's required headcount lands.

**Ranking must be rule-driven and admin-configurable** — an ordered set of rules, each contributing to a person's rank, editable without a rebuild so the policy can be tuned after the first month. Do not hardcode a single policy.

Rules to support:

- **Availability rate** — how often the person makes themselves available across the season. Someone who signs up for very few events is **ranked lower**. Rationale: consistently unavailable staff are less valuable to the operation.
- **Supervisor performance rating** — see §10.4.
- **Tenure.**
- **Recency** — how long since they last worked.
- **Season load** — how many events they have worked this season.
- **Capability match** against what the event actually needs.
- **Signup time** as a tiebreaker.

**Scoring model: weighted, not a ladder.** Every rule scores the person 0 to 100 on its own terms, each rule has an admin-editable weight, and the person's rank is the weighted sum. A strict tiebreak ladder was rejected: the first rule would decide almost every case and the rest would never fire.

Starting weights, all editable, and expected to be tuned after the first month:

| Rule | Weight | Scores 100 when | Scores 0 when |
| --- | --- | --- | --- |
| Availability rate | 25 | at or above the 60 percent expectation | well below it |
| Performance rating | 25 | exceptional | needs discipline |
| Capability match | 20 | holds what the event still needs | holds nothing it needs |
| Season load | 15 | worked few events this season | worked many |
| Recency | 10 | long since they last worked | worked very recently |
| Tenure | 5 | most tenured | newest |
| Signup time | tiebreak | earliest | latest |

Note the deliberate tension: availability rate rewards signing up often, while season load and recency push work **towards** people who have not had much. That is the point — signing up often earns you consideration, it does not earn you every event. A supervisor will ask about this, so the preview below has to be able to answer it.

**Requirements:**

- Weights are stored as data, editable in `/admin`, with a **preview**: change a weight and see who crosses the cutoff line in both directions **before** committing. Nothing is defensible if the supervisor cannot see what a change does.
- A rule can be **disabled** (weight zero) without being removed.
- Every ranked list is **explainable per person** — show the score each rule contributed, so "why am I not on Saturday" has an answer on screen rather than an argument.
- The cutoff is a **recommendation, not a lock**. Admin can pull a specific person above the line; record who did it and when, exactly as with `min_tenure` overrides.
- Ranking runs against **required headcount for the event**, which varies by event type.

### 10.4 Supervisor performance ratings

At the end of a night, supervisors rate their staff. Five bands:

1. Exceptional
2. Above average
3. Acceptable
4. Below par
5. Needs discipline

**Interaction design matters more than the data model here.** A supervisor rating fifteen people at the end of a long shift will mark everyone "acceptable" unless it is near-frictionless:

- A **row per person, with a five-detent slider** defaulted to the middle (**acceptable**). The supervisor runs down the list and nudges left or right only for the exceptions. One thumb, no typing.
- **Untouched rows record as acceptable.** Rating is effectively optional; only the exceptions cost any effort.

**These are employment records, and they feed scheduling priority.** Therefore:

- Keep a full **audit trail**: who rated whom, when, for which event, and every subsequent change.
- Plan for a person being able to **see their own rating history**. This is the kind of record that surfaces in a grievance, and "the computer decided" is not a defensible answer.

---

## 11. Open questions
- The position list is **best recollection, not a real deployment sheet** — a real sheet runs two to three A4 pages (roughly 100-150 lines), so the list here is materially incomplete. No sheet is available, so the demo proceeds on that basis. Position admin must therefore support add, edit and delete cleanly, and the demo deliberately uses that: adding a missing post live is a feature showcase, not an apology.
- **Note:** the build container used for seed generation had no network access; Faker couldn't be installed, so names came from hardcoded lists with a fixed seed.
