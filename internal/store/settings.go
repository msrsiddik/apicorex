package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// PluginSetting is one value set from the dashboard. For a secret, Value is
// empty everywhere but in what is handed to the plugin itself.
type PluginSetting struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	Secret    bool      `json:"secret"`
	Version   int64     `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by"`
}

// SettingChange is one entry in a plugin's settings history. A nil Value is a
// key cleared back to the plugin's environment or default — or a secret,
// whose value is never shown; Cleared tells the two apart.
type SettingChange struct {
	Version int64     `json:"version"`
	Key     string    `json:"key"`
	Value   *string   `json:"value"`
	Secret  bool      `json:"secret"`
	Cleared bool      `json:"cleared"`
	Note    string    `json:"note"`
	SavedAt time.Time `json:"saved_at"`
	SavedBy string    `json:"saved_by"`
}

func settingAAD(plugin, key string) string { return "setting:" + plugin + ":" + key }

// ListSettings returns what is set for plugin, by key, with secrets' values
// left out. This is the dashboard's view.
func (s *Store) ListSettings(ctx context.Context, plugin string) (map[string]PluginSetting, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value, secret, version, updated_at, updated_by
		FROM plugin_settings WHERE plugin = ?`, plugin)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]PluginSetting{}
	for rows.Next() {
		var ps PluginSetting
		var at string
		if err := rows.Scan(&ps.Key, &ps.Value, &ps.Secret, &ps.Version, &at, &ps.UpdatedBy); err != nil {
			return nil, err
		}
		if ps.Secret {
			ps.Value = ""
		}
		ps.UpdatedAt = parseTime(at)
		out[ps.Key] = ps
	}
	return out, rows.Err()
}

// SettingsForPlugin returns plugin's values as the plugin itself receives
// them. Secrets are opened and included only when withSecrets is set — Core
// sets it only for a caller holding the plugin's own key — and withheld
// counts the ones left out otherwise.
func (s *Store) SettingsForPlugin(ctx context.Context, plugin string, withSecrets bool) (values map[string]string, withheld int, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value, secret FROM plugin_settings WHERE plugin = ?`, plugin)
	if err != nil {
		return nil, 0, err
	}
	type row struct {
		key, value string
		secret     bool
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.key, &r.value, &r.secret); err != nil {
			rows.Close()
			return nil, 0, err
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	values = map[string]string{}
	for _, r := range all {
		if !r.secret {
			values[r.key] = r.value
			continue
		}
		if !withSecrets {
			withheld++
			continue
		}
		plain, err := s.openSecret(r.value, settingAAD(plugin, r.key))
		if err != nil {
			return nil, 0, fmt.Errorf("open %s: %w", r.key, err)
		}
		values[r.key] = plain
	}
	return values, withheld, nil
}

// SettingsVersion is plugin's settings version: the latest history id for
// it, so it rises with every change, a cleared key included. Zero means
// nothing has ever been set.
func (s *Store) SettingsVersion(ctx context.Context, plugin string) (int64, error) {
	var v int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM plugin_settings_history WHERE plugin = ?`, plugin).Scan(&v)
	return v, err
}

// SaveSettings applies changes to non-secret settings. See
// SaveSettingsWithSecrets.
func (s *Store) SaveSettings(ctx context.Context, plugin string, changes map[string]*string, note, actor string) (int64, error) {
	return s.SaveSettingsWithSecrets(ctx, plugin, changes, nil, note, actor)
}

// SaveSettingsWithSecrets applies changes in one transaction: a value sets
// the key, nil clears it. Keys in secret are sealed under the master key, in
// the row and in its history, and their values never reach the audit trail.
// Keys whose value would not change are skipped, so a form saved unchanged
// writes no history — for a secret, "unchanged" cannot be known without
// opening it, so a secret given a value is always written.
//
// The caller has validated values against the plugin's declaration; the
// store does not know what a setting means.
func (s *Store) SaveSettingsWithSecrets(ctx context.Context, plugin string, changes map[string]*string, secret map[string]bool, note, actor string) (int64, error) {
	if err := ValidatePluginName(plugin); err != nil || plugin == DefaultPlugin {
		return 0, fmt.Errorf("%w: plugin name %q", ErrInvalid, plugin)
	}
	keys := make([]string, 0, len(changes))
	for k := range changes {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Seal before the transaction: it needs no database, and the store's one
	// connection is held for all of a transaction.
	stored := map[string]*string{}
	for _, k := range keys {
		v := changes[k]
		if v == nil || !secret[k] {
			stored[k] = v
			continue
		}
		sealed, err := s.sealSecret(*v, settingAAD(plugin, k))
		if err != nil {
			return 0, err
		}
		stored[k] = &sealed
	}

	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var described []string
		for _, k := range keys {
			v := stored[k]
			var cur sql.NullString
			err := tx.QueryRowContext(ctx, `SELECT value FROM plugin_settings WHERE plugin = ? AND key = ?`, plugin, k).Scan(&cur)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if v == nil && !cur.Valid {
				continue
			}
			if v != nil && !secret[k] && cur.Valid && cur.String == *v {
				continue
			}
			if err := writeSetting(ctx, tx, plugin, k, v, secret[k], note, actor); err != nil {
				return err
			}
			switch {
			case v == nil:
				described = append(described, k+" cleared")
			case secret[k]:
				described = append(described, k+" set (secret)")
			default:
				described = append(described, k+"="+*v)
			}
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

// writeSetting appends a history row and applies it to the current value. v
// is what is stored: already sealed when secret.
func writeSetting(ctx context.Context, tx *sql.Tx, plugin, key string, v *string, secret bool, note, actor string) error {
	at := now()
	res, err := tx.ExecContext(ctx, `INSERT INTO plugin_settings_history (plugin, key, value, secret, note, saved_at, saved_by)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, plugin, key, v, secret, note, at, actor)
	if err != nil {
		return err
	}
	version, err := res.LastInsertId()
	if err != nil {
		return err
	}
	if v == nil {
		_, err := tx.ExecContext(ctx, `DELETE FROM plugin_settings WHERE plugin = ? AND key = ?`, plugin, key)
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO plugin_settings (plugin, key, value, secret, version, updated_at, updated_by)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (plugin, key) DO UPDATE SET value = excluded.value, secret = excluded.secret,
			version = excluded.version, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
		plugin, key, *v, secret, version, at, actor)
	return err
}

// RestoreSetting makes an earlier version of one key current again, as a new
// version. For a secret the sealed value is copied as it is — it was sealed
// for this same plugin and key — so going back never puts the value in the
// open. This is the way back from replacing a set-once secret by mistake.
func (s *Store) RestoreSetting(ctx context.Context, plugin string, version int64, actor string) (int64, error) {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var key string
		var v sql.NullString
		var secret bool
		err := tx.QueryRowContext(ctx, `SELECT key, value, secret FROM plugin_settings_history WHERE id = ? AND plugin = ?`,
			version, plugin).Scan(&key, &v, &secret)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		var val *string
		if v.Valid {
			val = &v.String
		}
		note := fmt.Sprintf("restored version %d", version)
		if err := writeSetting(ctx, tx, plugin, key, val, secret, note, actor); err != nil {
			return err
		}
		detail := fmt.Sprintf("%s %s", key, note)
		if !secret && val != nil {
			detail = fmt.Sprintf("%s=%s %s", key, *val, note)
		}
		return writeAudit(ctx, tx, actor, "settings.restore", plugin, detail)
	})
	if err != nil {
		return 0, err
	}
	return s.SettingsVersion(ctx, plugin)
}

// SettingsHistory returns plugin's setting changes, newest first, with
// secrets' values left out.
func (s *Store) SettingsHistory(ctx context.Context, plugin string, limit int) ([]SettingChange, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, key, value, secret, note, saved_at, saved_by
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
		if err := rows.Scan(&c.Version, &c.Key, &v, &c.Secret, &c.Note, &at, &c.SavedBy); err != nil {
			return nil, err
		}
		c.Cleared = !v.Valid
		if v.Valid && !c.Secret {
			val := v.String
			c.Value = &val
		}
		c.SavedAt = parseTime(at)
		out = append(out, c)
	}
	return out, rows.Err()
}
