package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// PluginSetting is one value set from the dashboard.
type PluginSetting struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	Version   int64     `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by"`
}

// SettingChange is one entry in a plugin's settings history. A nil Value is
// a key cleared back to the plugin's environment or default.
type SettingChange struct {
	Version int64     `json:"version"`
	Key     string    `json:"key"`
	Value   *string   `json:"value"`
	Note    string    `json:"note"`
	SavedAt time.Time `json:"saved_at"`
	SavedBy string    `json:"saved_by"`
}

// ListSettings returns what is set for plugin, by key.
func (s *Store) ListSettings(ctx context.Context, plugin string) (map[string]PluginSetting, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value, version, updated_at, updated_by
		FROM plugin_settings WHERE plugin = ?`, plugin)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]PluginSetting{}
	for rows.Next() {
		var ps PluginSetting
		var at string
		if err := rows.Scan(&ps.Key, &ps.Value, &ps.Version, &at, &ps.UpdatedBy); err != nil {
			return nil, err
		}
		ps.UpdatedAt = parseTime(at)
		out[ps.Key] = ps
	}
	return out, rows.Err()
}

// SettingsVersion is plugin's settings version: the latest history id for
// it, so it rises with every change, a cleared key included. Zero means
// nothing has ever been set.
func (s *Store) SettingsVersion(ctx context.Context, plugin string) (int64, error) {
	var v int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM plugin_settings_history WHERE plugin = ?`, plugin).Scan(&v)
	return v, err
}

// SaveSettings applies changes to plugin's settings in one transaction: a
// value sets the key, nil clears it. Keys whose value would not change are
// skipped, so saving a form unchanged writes no history. The caller has
// validated the values against the plugin's declaration — the store does not
// know what a setting means.
//
// Values go into the audit trail as they are. Only settings not declared
// secret reach here; secrets are a separate path that seals them.
func (s *Store) SaveSettings(ctx context.Context, plugin string, changes map[string]*string, note, actor string) (int64, error) {
	if err := ValidatePluginName(plugin); err != nil || plugin == DefaultPlugin {
		return 0, fmt.Errorf("%w: plugin name %q", ErrInvalid, plugin)
	}
	keys := make([]string, 0, len(changes))
	for k := range changes {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var described []string
		for _, k := range keys {
			v := changes[k]
			var cur sql.NullString
			err := tx.QueryRowContext(ctx, `SELECT value FROM plugin_settings WHERE plugin = ? AND key = ?`, plugin, k).Scan(&cur)
			if err != nil && err != sql.ErrNoRows {
				return err
			}
			if (v == nil && !cur.Valid) || (v != nil && cur.Valid && cur.String == *v) {
				continue
			}
			at := now()
			res, err := tx.ExecContext(ctx, `INSERT INTO plugin_settings_history (plugin, key, value, note, saved_at, saved_by)
				VALUES (?, ?, ?, ?, ?, ?)`, plugin, k, v, note, at, actor)
			if err != nil {
				return err
			}
			version, err := res.LastInsertId()
			if err != nil {
				return err
			}
			if v == nil {
				if _, err := tx.ExecContext(ctx, `DELETE FROM plugin_settings WHERE plugin = ? AND key = ?`, plugin, k); err != nil {
					return err
				}
				described = append(described, k+" cleared")
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO plugin_settings (plugin, key, value, version, updated_at, updated_by)
				VALUES (?, ?, ?, ?, ?, ?)
				ON CONFLICT (plugin, key) DO UPDATE SET value = excluded.value, version = excluded.version,
					updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
				plugin, k, *v, version, at, actor); err != nil {
				return err
			}
			described = append(described, k+"="+*v)
		}
		if len(described) == 0 {
			return nil
		}
		detail := strings.Join(described, ", ")
		if note != "" {
			detail += " — " + note
		}
		return writeAudit(ctx, tx, actor, "settings.save", plugin, detail)
	})
	if err != nil {
		return 0, err
	}
	return s.SettingsVersion(ctx, plugin)
}

// SettingsHistory returns plugin's setting changes, newest first.
func (s *Store) SettingsHistory(ctx context.Context, plugin string, limit int) ([]SettingChange, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, key, value, note, saved_at, saved_by
		FROM plugin_settings_history WHERE plugin = ? ORDER BY id DESC LIMIT ?`, plugin, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SettingChange{}
	for rows.Next() {
		var c SettingChange
		var v sql.NullString
		var at string
		if err := rows.Scan(&c.Version, &c.Key, &v, &c.Note, &at, &c.SavedBy); err != nil {
			return nil, err
		}
		if v.Valid {
			val := v.String
			c.Value = &val
		}
		c.SavedAt = parseTime(at)
		out = append(out, c)
	}
	return out, rows.Err()
}
