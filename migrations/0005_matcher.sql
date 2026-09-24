-- 0005_matcher: explainable drafts (SPEC §5 assignment workflow).

-- Why the matcher chose this person ("named primary", "opted in", "most
-- tenured in the group"), shown on the review screen.
ALTER TABLE assignments ADD COLUMN reason TEXT NOT NULL DEFAULT '';

-- When the matcher last drafted the event, and who published the sheet.
ALTER TABLE events ADD COLUMN matched_at TEXT;
ALTER TABLE events ADD COLUMN published_by TEXT REFERENCES persons(id) ON DELETE SET NULL;
