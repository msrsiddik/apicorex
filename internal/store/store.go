// Package store is Core's infrastructure config store: a SQLite file holding
// what operators set from the gateway dashboard — plugin database connections,
// pool sizes, commands sent to plugins — and the audit trail of who changed it.
//
// It is SQLite rather than a schema in the platform's Postgres because it has to
// work before Postgres is reachable: the address of Postgres is one of the
// things it holds.
//
// Nothing that describes a tenant, a user or a product belongs here. Core holds
// no domain data (see CLAUDE.md); this is config about the platform's plumbing.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Store is the open config store.
type Store struct {
	db     *sql.DB
	sealer *sealer // nil when Core started without CORE_MASTER_KEY
	path   string
}

// Open opens (creating if needed) the store at path and applies any pending
// migrations. masterKey may be nil: the store then works for everything except
// secrets, which return ErrNoMasterKey.
func Open(ctx context.Context, path string, masterKey []byte) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		// 0700: the file holds sealed secrets, and its directory listing need
		// not be anyone else's business either.
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("store: create %s: %w", dir, err)
		}
	}

	// busy_timeout waits out a lock instead of failing (a backup run from the
	// CLI while Core is up takes one); WAL lets that backup read while Core
	// writes; foreign_keys is off by default in SQLite and the schema relies
	// on it.
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	// One connection. The load is a dashboard and a heartbeat per plugin every
	// 15s, nowhere near needing more, and a single connection rules out
	// SQLITE_BUSY between Core's own goroutines entirely. The cost is that code
	// in this package must never run a query on s.db while holding a tx — it
	// would wait for itself.
	db.SetMaxOpenConns(1)

	s := &Store{db: db, path: path}
	if masterKey != nil {
		if s.sealer, err = newSealer(masterKey); err != nil {
			db.Close()
			return nil, fmt.Errorf("store: master key: %w", err)
		}
	}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// HasMasterKey reports whether secrets can be stored. The dashboard uses it to
// explain an empty, disabled form instead of failing on save.
func (s *Store) HasMasterKey() bool { return s.sealer != nil }

// Path is the file the store lives in.
func (s *Store) Path() string { return s.path }

// migrate applies every migration newer than the recorded version, each in its
// own transaction together with its version row, so a crash mid-way leaves the
// store at a whole version rather than half of one.
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}

	var current int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}
	// A store written by a newer Core — a rollback of the binary. Running on
	// would mean writing rows in a shape the newer code does not expect.
	if current > len(migrations) {
		return fmt.Errorf("store: schema version %d is newer than this build knows (%d); run the newer Core or restore a backup", current, len(migrations))
	}

	for i := current; i < len(migrations); i++ {
		version := i + 1
		err := s.inTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, version, now())
			return err
		})
		if err != nil {
			return fmt.Errorf("store: migration %d: %w", version, err)
		}
		log.Printf("[store] applied migration %d", version)
	}
	return nil
}

// inTx runs fn in a transaction, committing on success.
func (s *Store) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			return errors.Join(err, rbErr)
		}
		return err
	}
	return tx.Commit()
}

// now is the timestamp format every table uses: UTC RFC 3339, which sorts as
// text in time order.
func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}
