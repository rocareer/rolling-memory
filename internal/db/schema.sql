-- Rolling-Memory schema v1 (SQLite).
--
-- Design notes:
-- * The graph has one id space. Assertions are nodes too.
-- * Raw evidence lives in `trace`; every assertion points at a verbatim span.
-- * All state changes are append-only. Old assertion content is never edited;
--   supersession/conflict information is recorded as new rows.
-- * `private` rows are local-only by construction (see privacy.py / sync.py).

CREATE TABLE IF NOT EXISTS db_meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

-- L0: raw evidence. Never uploaded by the tool itself.
CREATE TABLE IF NOT EXISTS trace (
  hash        TEXT PRIMARY KEY,
  scope       TEXT NOT NULL,
  session     TEXT NOT NULL DEFAULT 'default',
  turn        INTEGER NOT NULL DEFAULT 0,
  kind        TEXT NOT NULL DEFAULT 'message',
  author      TEXT NOT NULL DEFAULT '',
  audience    TEXT NOT NULL DEFAULT 'shared'
              CHECK (audience IN ('private', 'group', 'shared', 'public')),
  text        TEXT NOT NULL,
  occurred_at INTEGER NOT NULL,
  recorded_at INTEGER NOT NULL,
  meta_json   TEXT NOT NULL DEFAULT '{}',
  processed   INTEGER NOT NULL DEFAULT 0 CHECK (processed IN (0, 1)),
  processed_at INTEGER
);
CREATE INDEX IF NOT EXISTS trace_scope_time ON trace(scope, occurred_at);
CREATE INDEX IF NOT EXISTS trace_scope_processed ON trace(scope, processed, recorded_at);
CREATE INDEX IF NOT EXISTS trace_session ON trace(scope, session, turn);

-- One node table for referents, relation types, assertion nodes, roles, etc.
CREATE TABLE IF NOT EXISTS node (
  id          TEXT PRIMARY KEY,
  scope       TEXT NOT NULL,
  kind        TEXT NOT NULL
              CHECK (kind IN ('referent', 'relation_type', 'assertion', 'role', 'expression', 'incarnation')),
  label       TEXT NOT NULL,
  norm_label  TEXT NOT NULL,
  status      TEXT NOT NULL DEFAULT 'provisional'
              CHECK (status IN ('provisional', 'active', 'merged', 'retired')),
  audience    TEXT NOT NULL DEFAULT 'shared'
              CHECK (audience IN ('private', 'group', 'shared', 'public')),
  world       TEXT,
  carrier     TEXT,
  locus       TEXT,
  created_at  INTEGER NOT NULL,
  retired_at  INTEGER
);
-- Labels are unique per kind while live.  Same-name different-person cases are
-- split later by explicit node ids / governance (see docs/design.md).
CREATE UNIQUE INDEX IF NOT EXISTS node_identity
  ON node(scope, kind, norm_label)
  WHERE status IN ('provisional', 'active') AND kind != 'assertion';
CREATE INDEX IF NOT EXISTS node_scope_kind ON node(scope, kind, status);
CREATE INDEX IF NOT EXISTS node_scope_audience ON node(scope, audience, status);

CREATE TABLE IF NOT EXISTS node_alias (
  node_id    TEXT NOT NULL REFERENCES node(id) ON DELETE CASCADE,
  alias      TEXT NOT NULL,
  norm_alias TEXT NOT NULL,
  source     TEXT NOT NULL DEFAULT 'manual',
  created_at INTEGER NOT NULL,
  PRIMARY KEY (node_id, norm_alias)
);
CREATE INDEX IF NOT EXISTS node_alias_norm ON node_alias(norm_alias);

-- The single storage unit: a structured memory assertion with a verbatim anchor.
CREATE TABLE IF NOT EXISTS assertion (
  node_id           TEXT PRIMARY KEY REFERENCES node(id) ON DELETE CASCADE,
  scope             TEXT NOT NULL,
  subject_id        TEXT REFERENCES node(id),
  subject_literal   TEXT,
  rel_id            TEXT NOT NULL REFERENCES node(id),
  object_id         TEXT REFERENCES node(id),
  object_literal    TEXT,
  bare              INTEGER NOT NULL DEFAULT 0 CHECK (bare IN (0, 1)),
  world             TEXT,
  carrier           TEXT,
  locus             TEXT,
  t_valid_from      INTEGER NOT NULL DEFAULT 0,
  t_valid_to        INTEGER NOT NULL DEFAULT 0,
  t_record          INTEGER NOT NULL,
  t_source          INTEGER NOT NULL,
  temporal_class    TEXT NOT NULL DEFAULT 'unknown'
                    CHECK (temporal_class IN ('past', 'present', 'future', 'unknown')),
  holder_id         TEXT REFERENCES node(id),
  warrant           TEXT NOT NULL DEFAULT 'referential'
                    CHECK (warrant IN ('referential', 'performative', 'procedural', 'experiential')),
  anchor_trace_hash TEXT NOT NULL REFERENCES trace(hash),
  anchor_start      INTEGER NOT NULL,
  anchor_end        INTEGER NOT NULL,
  source_trace_hash TEXT NOT NULL REFERENCES trace(hash),
  audience          TEXT NOT NULL
                    CHECK (audience IN ('private', 'group', 'shared', 'public')),
  confidence        REAL NOT NULL DEFAULT 0.5 CHECK (confidence >= 0 AND confidence <= 1),
  status            TEXT NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active', 'superseded', 'expired', 'disputed', 'retracted')),
  loop_state        TEXT CHECK (loop_state IS NULL OR loop_state IN ('open', 'fulfilled', 'waived', 'broken')),
  falsifier         TEXT,
  hash              TEXT NOT NULL UNIQUE,
  created_at        INTEGER NOT NULL,
  CHECK (subject_id IS NOT NULL OR subject_literal IS NOT NULL),
  CHECK (object_id IS NOT NULL OR object_literal IS NOT NULL),
  CHECK (anchor_start >= 0 AND anchor_end > anchor_start)
);
CREATE INDEX IF NOT EXISTS assertion_scope_subject
  ON assertion(scope, subject_id) WHERE status = 'active';
CREATE INDEX IF NOT EXISTS assertion_scope_object
  ON assertion(scope, object_id) WHERE status = 'active';
CREATE INDEX IF NOT EXISTS assertion_scope_rel ON assertion(scope, rel_id);
CREATE INDEX IF NOT EXISTS assertion_scope_time ON assertion(scope, t_valid_from);
CREATE INDEX IF NOT EXISTS assertion_anchor ON assertion(anchor_trace_hash);

CREATE TABLE IF NOT EXISTS mention (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  scope       TEXT NOT NULL,
  trace_hash  TEXT NOT NULL REFERENCES trace(hash) ON DELETE CASCADE,
  span_start  INTEGER NOT NULL,
  span_end    INTEGER NOT NULL,
  surface     TEXT NOT NULL,
  node_id     TEXT REFERENCES node(id),
  created_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS mention_trace ON mention(trace_hash);
CREATE INDEX IF NOT EXISTS mention_node ON mention(node_id);
CREATE UNIQUE INDEX IF NOT EXISTS mention_identity
  ON mention(trace_hash, span_start, span_end, surface);

CREATE TABLE IF NOT EXISTS meta_edge (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  scope           TEXT NOT NULL,
  meta_kind       TEXT NOT NULL
                  CHECK (meta_kind IN ('same-as', 'conflicts-with', 'subtype-of', 'inverse-of', 'supersedes', 'supplement')),
  lhs_id          TEXT NOT NULL REFERENCES node(id),
  rhs_id          TEXT NOT NULL REFERENCES node(id),
  status          TEXT NOT NULL DEFAULT 'active'
                  CHECK (status IN ('active', 'proposed', 'reverted')),
  evidence_anchor TEXT,
  created_at      INTEGER NOT NULL,
  created_by      TEXT NOT NULL DEFAULT 'system',
  revert_of       INTEGER REFERENCES meta_edge(id),
  UNIQUE (scope, meta_kind, lhs_id, rhs_id, status)
);
CREATE INDEX IF NOT EXISTS meta_lhs ON meta_edge(lhs_id);
CREATE INDEX IF NOT EXISTS meta_rhs ON meta_edge(rhs_id);
CREATE INDEX IF NOT EXISTS meta_kind_status ON meta_edge(scope, meta_kind, status);

CREATE TABLE IF NOT EXISTS event (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  scope       TEXT NOT NULL,
  op          TEXT NOT NULL,
  refs_json   TEXT NOT NULL DEFAULT '{}',
  note        TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL,
  created_by  TEXT NOT NULL DEFAULT 'system'
);
CREATE INDEX IF NOT EXISTS event_scope_time ON event(scope, created_at);

-- Name-level vectors only (label + aliases), keyed by model so switching
-- models never mixes incompatible spaces.
CREATE TABLE IF NOT EXISTS vector (
  owner_kind TEXT NOT NULL CHECK (owner_kind IN ('node', 'query')),
  owner_hash TEXT NOT NULL,
  model      TEXT NOT NULL,
  dim        INTEGER NOT NULL CHECK (dim > 0),
  vec        BLOB NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (owner_kind, owner_hash, model)
);
CREATE INDEX IF NOT EXISTS vector_owner ON vector(owner_kind, owner_hash);

CREATE TABLE IF NOT EXISTS replay_state (
  scope            TEXT PRIMARY KEY,
  produced_through INTEGER NOT NULL DEFAULT 0,
  last_trace_hash  TEXT,
  updated_at       INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS session_cursor (
  scope      TEXT NOT NULL,
  session    TEXT NOT NULL,
  turn_done  INTEGER NOT NULL DEFAULT -1,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (scope, session)
);

CREATE TABLE IF NOT EXISTS activation (
  node_id    TEXT NOT NULL REFERENCES node(id) ON DELETE CASCADE,
  scope      TEXT NOT NULL,
  score      REAL NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (node_id, scope)
);

CREATE TABLE IF NOT EXISTS sync_state (
  direction  TEXT NOT NULL CHECK (direction IN ('push', 'pull')),
  local_hash TEXT NOT NULL,
  remote_id  TEXT NOT NULL DEFAULT '',
  synced_at  INTEGER NOT NULL,
  PRIMARY KEY (direction, local_hash)
);

CREATE TABLE IF NOT EXISTS injection_ledger (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  session      TEXT NOT NULL,
  prompt_hash  TEXT NOT NULL,
  content_hash TEXT NOT NULL,
  created_at   INTEGER NOT NULL,
  UNIQUE (session, prompt_hash, content_hash)
);
CREATE INDEX IF NOT EXISTS injection_time ON injection_ledger(created_at);

CREATE TABLE IF NOT EXISTS transcript_cursor (
  path       TEXT NOT NULL,
  session    TEXT NOT NULL,
  offset     INTEGER NOT NULL DEFAULT 0,
  size       INTEGER NOT NULL DEFAULT 0,
  mtime      REAL NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (path, session)
);

-- Lexical legs.  trigram tokenizer is available in SQLite >= 3.34 and handles
-- CJK without an external tokenizer.  Short terms (<3 chars) use LIKE.
CREATE VIRTUAL TABLE IF NOT EXISTS node_fts USING fts5(
  label,
  aliases,
  node_id UNINDEXED,
  tokenize = 'trigram'
);

CREATE VIRTUAL TABLE IF NOT EXISTS trace_fts USING fts5(
  text,
  trace_hash UNINDEXED,
  tokenize = 'trigram'
);
