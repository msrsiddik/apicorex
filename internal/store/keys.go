package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// PluginKey is one API key issued to a plugin, as the dashboard sees it. The
// key itself is shown once, when it is issued, and never again.
type PluginKey struct {
	ID         int64      `json:"id"`
	Plugin     string     `json:"plugin"`
	Hint       string     `json:"hint"`
	CreatedAt  time.Time  `json:"created_at"`
	CreatedBy  string     `json:"created_by"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	RevokedBy  string     `json:"revoked_by,omitempty"`
}

// keyPrefix marks a key as Core's, so one pasted into the wrong place is
// recognisable, and a leaked one easy to search for.
const keyPrefix = "akx_"

func hashKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// IssuePluginKey creates a key for plugin and returns it — the only time it
// is ever available in full.
func (s *Store) IssuePluginKey(ctx context.Context, plugin, actor string) (string, PluginKey, error) {
	if err := ValidatePluginName(plugin); err != nil || plugin == DefaultPlugin {
		return "", PluginKey{}, fmt.Errorf("%w: plugin name %q", ErrInvalid, plugin)
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", PluginKey{}, err
	}
	raw := keyPrefix + base64.RawURLEncoding.EncodeToString(b)
	hint := raw[len(raw)-4:]
	var id int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO plugin_keys (plugin, key_hash, key_hint, created_at, created_by)
			VALUES (?, ?, ?, ?, ?)`, plugin, hashKey(raw), hint, now(), actor)
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		return writeAudit(ctx, tx, actor, "key.issue", plugin, fmt.Sprintf("key %d (…%s)", id, hint))
	})
	if err != nil {
		return "", PluginKey{}, err
	}
	k, err := s.getPluginKey(ctx, id)
	return raw, k, err
}

const keyCols = `id, plugin, key_hint, created_at, created_by, last_used_at, revoked_at, COALESCE(revoked_by, '')`

func scanKey(sc scanner) (PluginKey, error) {
	var k PluginKey
	var created string
	var used, revoked sql.NullString
	if err := sc.Scan(&k.ID, &k.Plugin, &k.Hint, &created, &k.CreatedBy, &used, &revoked, &k.RevokedBy); err != nil {
		return k, err
	}
	k.CreatedAt = parseTime(created)
	k.LastUsedAt = optTime(used)
	k.RevokedAt = optTime(revoked)
	return k, nil
}

func (s *Store) getPluginKey(ctx context.Context, id int64) (PluginKey, error) {
	k, err := scanKey(s.db.QueryRowContext(ctx, `SELECT `+keyCols+` FROM plugin_keys WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return PluginKey{}, ErrNotFound
	}
	return k, err
}

// ListPluginKeys returns every key, active ones first, newest first within.
func (s *Store) ListPluginKeys(ctx context.Context) ([]PluginKey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+keyCols+` FROM plugin_keys
		ORDER BY revoked_at IS NOT NULL, plugin, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PluginKey{}
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// RevokePluginKey stops a key working. plugin must be the key's own, so a
// stale screen cannot revoke a key of a different plugin by id.
func (s *Store) RevokePluginKey(ctx context.Context, plugin string, id int64, actor string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE plugin_keys SET revoked_at = ?, revoked_by = ?
			WHERE id = ? AND plugin = ? AND revoked_at IS NULL`, now(), actor, id, plugin)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return writeAudit(ctx, tx, actor, "key.revoke", plugin, fmt.Sprintf("key %d", id))
	})
}

// LookupPluginKey returns the plugin an active key belongs to. ok is false
// for anything else — an unknown key, a revoked one, the shared key.
func (s *Store) LookupPluginKey(ctx context.Context, raw string) (plugin string, ok bool, err error) {
	if len(raw) <= len(keyPrefix) || raw[:len(keyPrefix)] != keyPrefix {
		return "", false, nil
	}
	var id int64
	err = s.db.QueryRowContext(ctx, `SELECT id, plugin FROM plugin_keys WHERE key_hash = ? AND revoked_at IS NULL`,
		hashKey(raw)).Scan(&id, &plugin)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	// Last use is what tells an operator a key is safe to revoke. A failed
	// write here must not refuse a valid caller.
	_, _ = s.db.ExecContext(ctx, `UPDATE plugin_keys SET last_used_at = ? WHERE id = ?`, now(), id)
	return plugin, true, nil
}

// Core settings.
const coreAcceptSharedKey = "accept_shared_plugin_key"

// AcceptSharedKey reports whether the shared PLUGIN_API_KEY is still
// accepted from plugins. Yes until an operator turns it off: every plugin
// starts out on it.
func (s *Store) AcceptSharedKey(ctx context.Context) (bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM core_settings WHERE key = ?`, coreAcceptSharedKey).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return true, err
	}
	return v != "false", nil
}

// SetAcceptSharedKey turns acceptance of the shared key on or off.
func (s *Store) SetAcceptSharedKey(ctx context.Context, accept bool, actor string) error {
	v := "true"
	if !accept {
		v = "false"
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO core_settings (key, value, updated_at, updated_by) VALUES (?, ?, ?, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
			coreAcceptSharedKey, v, now(), actor); err != nil {
			return err
		}
		return writeAudit(ctx, tx, actor, "core.accept_shared_key", "", v)
	})
}
