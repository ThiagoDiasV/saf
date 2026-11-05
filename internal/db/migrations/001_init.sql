-- Schema from docs/PLAN.md §4. STRICT tables reject values of the wrong type instead of storing them.
-- Times are UTC RFC 3339 text; dates ('occurred_on') are local YYYY-MM-DD.

CREATE TABLE users (
  id            INTEGER PRIMARY KEY,
  name          TEXT NOT NULL,
  login         TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  role          TEXT NOT NULL CHECK (role IN ('admin', 'viewer')),
  active        INTEGER NOT NULL DEFAULT 1,
  failed_logins INTEGER NOT NULL DEFAULT 0,
  locked_until  TEXT,
  last_login_at TEXT,
  created_at    TEXT NOT NULL
) STRICT;

CREATE TABLE sessions (
  token_hash BLOB PRIMARY KEY,                 -- SHA-256 of the cookie value
  user_id    INTEGER NOT NULL REFERENCES users(id),
  expires_at TEXT NOT NULL
) STRICT;

CREATE TABLE medicines (
  id                INTEGER PRIMARY KEY,
  active_ingredient TEXT NOT NULL,
  strength          TEXT NOT NULL,             -- "500 mg", "50 mg/mL"
  dosage_form       TEXT NOT NULL,             -- "comprimido"
  unit              TEXT NOT NULL,             -- how stock is counted
  match_key         TEXT NOT NULL UNIQUE,      -- ingredient+strength+form, folded
  archived_at       TEXT,
  created_at        TEXT NOT NULL
) STRICT;

CREATE TABLE batches (
  id           INTEGER PRIMARY KEY,
  medicine_id  INTEGER NOT NULL REFERENCES medicines(id),
  lot          TEXT NOT NULL,
  manufacturer TEXT NOT NULL,
  expiry_month TEXT NOT NULL,                  -- 'YYYY-MM', valid until the last day of the month
  match_key    TEXT NOT NULL,                  -- lot+manufacturer, folded
  UNIQUE (medicine_id, match_key)
) STRICT;

CREATE TABLE movements (
  id           INTEGER PRIMARY KEY,
  kind         TEXT NOT NULL CHECK (kind IN ('in', 'out', 'reversal')),
  type         TEXT NOT NULL,                  -- 'Doação', 'Dispensação', 'Estorno de saída', ...
  occurred_on  TEXT NOT NULL,                  -- movement date, may be backdated
  recorded_at  TEXT NOT NULL,                  -- save time
  user_id      INTEGER NOT NULL REFERENCES users(id),
  note         TEXT NOT NULL DEFAULT '',
  patient_name BLOB,                           -- sealed first name; NULL if none or purged
  reverses_id  INTEGER UNIQUE REFERENCES movements(id)
) STRICT;
CREATE INDEX movements_occurred_on ON movements(occurred_on);

CREATE TABLE movement_lines (
  id          INTEGER PRIMARY KEY,
  movement_id INTEGER NOT NULL REFERENCES movements(id),
  batch_id    INTEGER NOT NULL REFERENCES batches(id),
  quantity    INTEGER NOT NULL CHECK (quantity <> 0)    -- + in, - out
) STRICT;
CREATE INDEX movement_lines_batch ON movement_lines(batch_id);
CREATE INDEX movement_lines_movement ON movement_lines(movement_id);

CREATE TABLE photos (
  id          INTEGER PRIMARY KEY,
  movement_id INTEGER NOT NULL REFERENCES movements(id),
  added_at    TEXT NOT NULL,
  added_by    INTEGER NOT NULL REFERENCES users(id),
  removed_at  TEXT,                            -- file deleted
  removed_by  INTEGER REFERENCES users(id)     -- NULL with removed_at set = 6-month purge
) STRICT;
CREATE INDEX photos_movement ON photos(movement_id);

CREATE TABLE changes (                         -- shown as "Ajuste" in Histórico
  id          INTEGER PRIMARY KEY,
  medicine_id INTEGER REFERENCES medicines(id),  -- exactly one of these three is set
  batch_id    INTEGER REFERENCES batches(id),
  movement_id INTEGER REFERENCES movements(id),
  field       TEXT NOT NULL,                     -- 'Lote', 'Validade', 'Observação', ...
  old_value   TEXT NOT NULL,
  new_value   TEXT NOT NULL,
  user_id     INTEGER NOT NULL REFERENCES users(id),
  changed_at  TEXT NOT NULL,                     -- one save = one timestamp = one Ajuste entry
  CHECK ((medicine_id IS NOT NULL) + (batch_id IS NOT NULL) + (movement_id IS NOT NULL) = 1)
) STRICT;
CREATE INDEX changes_medicine ON changes(medicine_id);
CREATE INDEX changes_batch ON changes(batch_id);
CREATE INDEX changes_movement ON changes(movement_id);
