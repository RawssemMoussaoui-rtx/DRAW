package storage

var migrations = []Migration{
	{
		Version: 1,
		Name:    "baseline",
		Up: `CREATE TABLE IF NOT EXISTS schema_migrations (
  version INTEGER PRIMARY KEY,
  applied_at DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL,
  state TEXT NOT NULL CHECK(state IN ('ACTIVE','ARCHIVED','DELETED','EXPIRED')),
  intent TEXT NOT NULL,
  plan TEXT NOT NULL,
  created_at DATETIME NOT NULL,
  recovery_token TEXT
);

CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);

CREATE TABLE IF NOT EXISTS tasks (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  type TEXT NOT NULL CHECK(type IN ('DISCOVER','FETCH_HTTP','FETCH_BROWSER','VERIFY','RECONCILE')),
  state TEXT NOT NULL CHECK(state IN ('READY','RUNNING','RETRY_WAIT','COMPLETED','FAILED','BLOCKED','CANCELLED','EXPIRED')),
  priority INTEGER NOT NULL DEFAULT 0,
  source_class TEXT CHECK(source_class IN ('OFFICIAL','NEWS','SOCIAL','SPECIALIZED','UNKNOWN')),
  source_target TEXT,
  url TEXT,
  parent_task_id TEXT,
  created_by_task_id TEXT,
  discovered_from_url_id TEXT,
  crawl_depth INTEGER NOT NULL DEFAULT 0,
  task_depth INTEGER NOT NULL DEFAULT 0,
  estimated_cost INTEGER NOT NULL DEFAULT 0,
  error_weight TEXT CHECK(error_weight IN ('LOW','MODERATE','HIGH','BLOCKING')),
  retry_count INTEGER NOT NULL DEFAULT 0,
  backoff_until DATETIME,
  error_info TEXT,
  created_at DATETIME NOT NULL,
  started_at DATETIME,
  completed_at DATETIME,
  task_key TEXT NOT NULL,
  UNIQUE(session_id, task_key)
);

CREATE INDEX IF NOT EXISTS idx_tasks_session ON tasks(session_id);
CREATE INDEX IF NOT EXISTS idx_tasks_state ON tasks(state);
CREATE INDEX IF NOT EXISTS idx_tasks_priority ON tasks(session_id, priority DESC);
CREATE INDEX IF NOT EXISTS idx_tasks_url ON tasks(url);
CREATE INDEX IF NOT EXISTS idx_tasks_parent ON tasks(parent_task_id);
CREATE INDEX IF NOT EXISTS idx_tasks_discovered ON tasks(discovered_from_url_id);

CREATE TABLE IF NOT EXISTS evidence (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  task_id TEXT NOT NULL,
  source_id TEXT NOT NULL,
  topic TEXT,
  claim TEXT,
  value TEXT,
  confidence REAL CHECK(confidence >= 0 AND confidence <= 1),
  verification_state TEXT NOT NULL CHECK(verification_state IN ('UNVERIFIED','PARTIALLY_VERIFIED','VERIFIED','DISPUTED','UNRESOLVED')),
  collected_at DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_evidence_session ON evidence(session_id);
CREATE INDEX IF NOT EXISTS idx_evidence_task ON evidence(task_id);
CREATE INDEX IF NOT EXISTS idx_evidence_source ON evidence(source_id);
CREATE INDEX IF NOT EXISTS idx_evidence_topic ON evidence(topic);

CREATE TABLE IF NOT EXISTS source_profiles (
  domain TEXT PRIMARY KEY,
  class TEXT NOT NULL CHECK(class IN ('OFFICIAL','NEWS','SOCIAL','SPECIALIZED','UNKNOWN')),
  quality_score REAL NOT NULL CHECK(quality_score >= 0 AND quality_score <= 1),
  crawl_depth_limit INTEGER NOT NULL DEFAULT 3,
  per_domain_limit INTEGER NOT NULL DEFAULT 5,
  rate_limit_rpm INTEGER NOT NULL DEFAULT 60,
  auth_type TEXT NOT NULL CHECK(auth_type IN ('NONE','USER_AUTHORIZED')),
  last_observed_at DATETIME,
  provisional INTEGER NOT NULL DEFAULT 0,
  denied INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_source_class ON source_profiles(class);
CREATE INDEX IF NOT EXISTS idx_source_denied ON source_profiles(domain) WHERE denied = 1;

CREATE TABLE IF NOT EXISTS relations (
  from_id TEXT NOT NULL,
  to_id TEXT NOT NULL,
  kind TEXT NOT NULL CHECK(kind IN ('SUPPORTS','CONTRADICTS','DUPLICATES','DERIVED_FROM')),
  strength REAL CHECK(strength >= 0 AND strength <= 1),
  PRIMARY KEY (from_id, to_id, kind)
);

CREATE INDEX IF NOT EXISTS idx_rel_from ON relations(from_id);
CREATE INDEX IF NOT EXISTS idx_rel_to ON relations(to_id);
CREATE INDEX IF NOT EXISTS idx_rel_kind ON relations(kind);

CREATE TABLE IF NOT EXISTS events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL,
  task_id TEXT,
  kind TEXT NOT NULL,
  level TEXT NOT NULL,
  message TEXT NOT NULL,
  data TEXT,
  ts DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_events_session_ts ON events(session_id, ts);
CREATE INDEX IF NOT EXISTS idx_events_kind ON events(kind);

CREATE TABLE IF NOT EXISTS sessions_recovery (
  session_id TEXT PRIMARY KEY,
  snapshot TEXT NOT NULL,
  expires_at DATETIME NOT NULL,
  created_at DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_recovery_expires ON sessions_recovery(expires_at);
`,
	},
	{
		Version: 2,
		Name:    "user_id_column",
		Up: `ALTER TABLE tasks ADD COLUMN user_id TEXT NOT NULL DEFAULT '';
`,
	},
	{
		Version: 3,
		Name:    "query_indexes",
		Up: `CREATE INDEX IF NOT EXISTS idx_evidence_verification_state ON evidence(verification_state);
CREATE INDEX IF NOT EXISTS idx_events_session_id ON events(session_id, id);
`,
	},
}

var coreTables = []string{
	"sessions", "tasks", "evidence", "source_profiles", "relations", "events",
	"sessions_recovery", "schema_migrations",
}
