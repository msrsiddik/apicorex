package store

// migrations is the store's schema history. Version N is migrations[N-1].
//
// Append only. A migration that has shipped is never edited: a store that
// already applied it would keep the old shape while a fresh one got the new,
// and nothing would say they differ.
var migrations = []string{
	// 1: the audit trail. Every change made through the dashboard writes a row
	// in the same transaction as the change, so there is no change without its
	// record. detail never holds a secret — callers describe what changed, not
	// the value it changed to.
	`CREATE TABLE audit_log (
		id     INTEGER PRIMARY KEY AUTOINCREMENT,
		at     TEXT NOT NULL,
		actor  TEXT NOT NULL,
		action TEXT NOT NULL,
		target TEXT NOT NULL DEFAULT '',
		detail TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX audit_log_at ON audit_log (at);`,

	// 2: plugin database connections. One row per plugin, plus plugin '*', the
	// default every plugin inherits field by field; NULL means "inherit".
	//
	// db_config_history is append-only and every save, delete and rollback
	// writes a row. Its id doubles as the config version: one sequence across
	// all plugins, so a plugin's effective version (the larger of its own and
	// the default's latest history id) rises whenever either changes.
	`CREATE TABLE db_config_history (
		id                  INTEGER PRIMARY KEY AUTOINCREMENT,
		plugin              TEXT NOT NULL,
		action              TEXT NOT NULL CHECK (action IN ('save', 'delete', 'rollback')),
		dsn_sealed          TEXT,
		dsn_display         TEXT,
		max_open            INTEGER,
		max_idle            INTEGER,
		conn_max_lifetime_s INTEGER,
		conn_max_idle_s     INTEGER,
		note                TEXT NOT NULL DEFAULT '',
		saved_at            TEXT NOT NULL,
		saved_by            TEXT NOT NULL
	);
	CREATE INDEX db_config_history_plugin ON db_config_history (plugin, id);

	CREATE TABLE db_config (
		plugin              TEXT PRIMARY KEY,
		dsn_sealed          TEXT,
		dsn_display         TEXT,
		max_open            INTEGER,
		max_idle            INTEGER,
		conn_max_lifetime_s INTEGER,
		conn_max_idle_s     INTEGER,
		version             INTEGER NOT NULL REFERENCES db_config_history (id),
		updated_at          TEXT NOT NULL,
		updated_by          TEXT NOT NULL
	);`,

	// 3: commands an operator sends a plugin from the dashboard. Delivered on
	// the plugin's next heartbeat, so a command waits here until then. At most
	// one is pending per plugin: a newer one supersedes it.
	`CREATE TABLE plugin_commands (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		plugin       TEXT NOT NULL,
		kind         TEXT NOT NULL CHECK (kind IN ('restart', 'reload')),
		state        TEXT NOT NULL CHECK (state IN ('pending', 'delivered', 'done', 'failed', 'superseded', 'expired')),
		result       TEXT NOT NULL DEFAULT '',
		requested_at TEXT NOT NULL,
		requested_by TEXT NOT NULL,
		delivered_at TEXT,
		finished_at  TEXT
	);
	CREATE INDEX plugin_commands_plugin ON plugin_commands (plugin, id);`,
}
