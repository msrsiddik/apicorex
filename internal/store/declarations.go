package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// SaveDeclarations records a plugin's settings declarations, as JSON, when it
// registers. Raw rather than typed: the store holds them for the dashboard
// and does not read them.
func (s *Store) SaveDeclarations(ctx context.Context, plugin string, settings json.RawMessage) error {
	if len(settings) == 0 {
		settings = json.RawMessage("[]")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO plugin_declarations (plugin, settings, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (plugin) DO UPDATE SET settings = excluded.settings, updated_at = excluded.updated_at`,
		plugin, string(settings), now())
	return err
}

// Declarations returns a plugin's last recorded settings declarations and
// when they were recorded. ok is false for a plugin never seen.
func (s *Store) Declarations(ctx context.Context, plugin string) (settings json.RawMessage, at time.Time, ok bool, err error) {
	var raw, ts string
	err = s.db.QueryRowContext(ctx, `SELECT settings, updated_at FROM plugin_declarations WHERE plugin = ?`, plugin).Scan(&raw, &ts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, time.Time{}, false, nil
	}
	if err != nil {
		return nil, time.Time{}, false, err
	}
	return json.RawMessage(raw), parseTime(ts), true, nil
}

// DeclaredPlugins lists every plugin with recorded declarations.
func (s *Store) DeclaredPlugins(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT plugin FROM plugin_declarations ORDER BY plugin`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
