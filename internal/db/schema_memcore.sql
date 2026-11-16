-- Rolling-Memory 1:1 Isomorphic Extension Schema for the SQLite store.
-- Applied idempotently on both fresh and existing SQLite databases while keeping
-- schema.sql v1 digest and the 15 base DumpTables column layout untouched.

-- 1:1 extended node attributes (incarnation_id, ext_ref, origin).
CREATE TABLE IF NOT EXISTS node_ext (
  node_id        TEXT PRIMARY KEY REFERENCES node(id) ON DELETE CASCADE,
  scope          TEXT NOT NULL,
  incarnation_id TEXT NOT NULL DEFAULT '',
  ext_ref        TEXT NOT NULL DEFAULT '',
  origin         TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS node_ext_scope ON node_ext(scope);

-- 1:1 extended assertion attributes:
--   * dual forms (subject_absent)
--   * unbound slots (subject_gap, object_gap, holder_gap)
--   * audience_subject
--   * event time axis (t_event, event_src)
--   * cold read counters (read_hits, last_read_at)
CREATE TABLE IF NOT EXISTS assertion_ext (
  node_id          TEXT PRIMARY KEY REFERENCES assertion(node_id) ON DELETE CASCADE,
  scope            TEXT NOT NULL,
  subject_absent   INTEGER NOT NULL DEFAULT 0 CHECK (subject_absent IN (0, 1)),
  subject_gap      TEXT NOT NULL DEFAULT ''
                   CHECK (subject_gap IN ('', 'absent_in_source', 'referent_unresolvable', 'unattributed', 'out_of_scope')),
  object_gap       TEXT NOT NULL DEFAULT ''
                   CHECK (object_gap IN ('', 'absent_in_source', 'referent_unresolvable', 'unattributed', 'out_of_scope')),
  holder_gap       TEXT NOT NULL DEFAULT ''
                   CHECK (holder_gap IN ('', 'absent_in_source', 'referent_unresolvable', 'unattributed', 'out_of_scope')),
  audience_subject TEXT NOT NULL DEFAULT '',
  t_event          INTEGER NOT NULL DEFAULT 0,
  event_src        TEXT NOT NULL DEFAULT ''
                   CHECK (event_src IN ('', 'explicit', 'relative_resolved')),
  read_hits        INTEGER NOT NULL DEFAULT 0,
  last_read_at     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS assertion_ext_scope_event ON assertion_ext(scope, t_event);

-- Δ3: n-ary role participants attached to an assertion.
CREATE TABLE IF NOT EXISTS participant (
  assertion_node_id TEXT NOT NULL REFERENCES assertion(node_id) ON DELETE CASCADE,
  seq               INTEGER NOT NULL,
  role_id           TEXT NOT NULL REFERENCES node(id),
  target_id         TEXT REFERENCES node(id),
  literal           TEXT,
  PRIMARY KEY (assertion_node_id, seq)
);
CREATE INDEX IF NOT EXISTS participant_target ON participant(target_id);

-- Δ4: Name-level vector rows (one row per node label and one row per node alias).
CREATE TABLE IF NOT EXISTS node_vector (
  scope      TEXT NOT NULL,
  node_id    TEXT NOT NULL REFERENCES node(id) ON DELETE CASCADE,
  name_hash  TEXT NOT NULL,
  name       TEXT NOT NULL,
  model      TEXT NOT NULL,
  dim        INTEGER NOT NULL,
  vec        BLOB NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (scope, node_id, name_hash, model)
);
CREATE INDEX IF NOT EXISTS node_vector_scope_node ON node_vector(scope, node_id);

-- C7: Episode Universe containers (L1 scene containers & L2 super-episodes).
CREATE TABLE IF NOT EXISTS episode (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  scope        TEXT NOT NULL,
  ep_key       TEXT NOT NULL UNIQUE,
  generation   INTEGER NOT NULL DEFAULT 1,
  level        INTEGER NOT NULL DEFAULT 1,
  world        TEXT NOT NULL DEFAULT 'actual',
  carrier      TEXT NOT NULL DEFAULT 'virtual',
  locus        TEXT NOT NULL DEFAULT '',
  session_key  TEXT NOT NULL DEFAULT '',
  t_start      INTEGER NOT NULL DEFAULT 0,
  t_end        INTEGER NOT NULL DEFAULT 0,
  t_source_min INTEGER NOT NULL DEFAULT 0,
  t_source_max INTEGER NOT NULL DEFAULT 0,
  member_count INTEGER NOT NULL DEFAULT 0,
  centroid_vec BLOB,
  variance_vec BLOB,
  updated_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS episode_scope_level ON episode(scope, level, updated_at);

CREATE TABLE IF NOT EXISTS episode_member (
  episode_id  INTEGER NOT NULL REFERENCES episode(id) ON DELETE CASCADE,
  scope       TEXT NOT NULL,
  member_id   TEXT NOT NULL,
  member_kind TEXT NOT NULL DEFAULT 'assertion' CHECK (member_kind IN ('assertion', 'episode')),
  facet       TEXT NOT NULL DEFAULT 'event'
              CHECK (facet IN ('time', 'person', 'scene', 'event', 'speech', 'feeling', 'learning', 'thought')),
  created_at  INTEGER NOT NULL,
  PRIMARY KEY (episode_id, member_id)
);
CREATE INDEX IF NOT EXISTS episode_member_lookup ON episode_member(scope, member_id);

-- A14 / U7: Two-zone working hot memory pointer ledger (recall & experience).
CREATE TABLE IF NOT EXISTS memory_hot (
  id                     INTEGER PRIMARY KEY AUTOINCREMENT,
  scope                  TEXT NOT NULL,
  session_key            TEXT NOT NULL DEFAULT 'default',
  kind                   TEXT NOT NULL DEFAULT 'recall' CHECK (kind IN ('recall', 'experience')),
  query_text             TEXT NOT NULL DEFAULT '',
  trace_hash             TEXT NOT NULL DEFAULT '',
  hit_node_ids_json      TEXT NOT NULL DEFAULT '[]',
  hit_assertion_ids_json TEXT NOT NULL DEFAULT '[]',
  created_at             INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS memory_hot_scope_time ON memory_hot(scope, created_at DESC);

-- D7: Restatement & occurrence ledger.
CREATE TABLE IF NOT EXISTS restate (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  scope             TEXT NOT NULL,
  assertion_node_id TEXT NOT NULL REFERENCES assertion(node_id) ON DELETE CASCADE,
  trace_hash        TEXT NOT NULL REFERENCES trace(hash) ON DELETE CASCADE,
  anchor_start      INTEGER NOT NULL DEFAULT 0,
  anchor_end        INTEGER NOT NULL DEFAULT 0,
  kind              TEXT NOT NULL DEFAULT 'mention' CHECK (kind IN ('mention', 'occurrence')),
  occurred_at       INTEGER NOT NULL,
  created_at        INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS restate_assertion ON restate(assertion_node_id, occurred_at DESC);

-- D3: Covering index for fast O(1) bounded active subject pre-probe & locus filtering at 10k+ scale.
CREATE INDEX IF NOT EXISTS assertion_active_preprobe
  ON assertion(scope, status, bare, confidence DESC, created_at DESC)
  WHERE status = 'active' AND bare = 0;

CREATE INDEX IF NOT EXISTS assertion_scope_subj_all
  ON assertion(scope, subject_id)
  WHERE status IN ('active', 'superseded');

CREATE INDEX IF NOT EXISTS assertion_scope_obj_all
  ON assertion(scope, object_id)
  WHERE status IN ('active', 'superseded');

-- L3 Noetic Memory Table (Downstream Self-Maintained):
-- Stores Agent Harness retrieval-induced reconsolidation records (Query + Cited Sentences/UnitIDs + Answer).
CREATE TABLE IF NOT EXISTS noetic (
  id                   TEXT PRIMARY KEY,
  scope                TEXT NOT NULL,
  session_key          TEXT NOT NULL DEFAULT 'default',
  status               TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'stale', 'retracted')),
  query_text           TEXT NOT NULL,
  answer_text          TEXT NOT NULL,
  cited_unit_ids_json  TEXT NOT NULL DEFAULT '[]',
  cited_sentences_json TEXT NOT NULL DEFAULT '[]',
  occurred_at          INTEGER NOT NULL DEFAULT 0,
  created_at           INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS noetic_scope_status_time ON noetic(scope, status, occurred_at DESC);


