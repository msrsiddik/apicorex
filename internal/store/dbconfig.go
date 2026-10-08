package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// DefaultPlugin is the db_config row every plugin inherits from.
const DefaultPlugin = "*"

// Built-in pool settings, for a field neither the plugin's row nor the default
// row sets. Deliberately modest: a plugin that needs more says so, and the
// dashboard shows the total against what Postgres allows.
const (
	builtinMaxOpen         = 10
	builtinMaxIdle         = 2
	builtinConnMaxLifetime = 30 * time.Minute
	builtinConnMaxIdleTime = 5 * time.Minute
)

// Limits on what a save may set. Wide enough for any real pool, narrow enough
// to catch a stray digit before it opens a thousand connections.
const (
	maxPoolSize     = 500
	maxDurationSecs = 7 * 24 * 60 * 60
)

var (
	// ErrNotFound means no row for that plugin, or no such history version.
	ErrNotFound = errors.New("store: not found")
	// ErrInvalid wraps every validation failure; the message says which field.
	ErrInvalid = errors.New("invalid")
)

var pluginNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// PoolSettings are a row's pool fields. nil means inherit.
type PoolSettings struct {
	MaxOpen            *int `json:"max_open"`
	MaxIdle            *int `json:"max_idle"`
	ConnMaxLifetimeSec *int `json:"conn_max_lifetime_s"`
	ConnMaxIdleSec     *int `json:"conn_max_idle_s"`
}

// DBConfig is a stored row as the dashboard sees it. It never carries the DSN
// itself — only a display form with the password replaced.
type DBConfig struct {
	Plugin     string       `json:"plugin"`
	HasDSN     bool         `json:"has_dsn"`
	DSNDisplay string       `json:"dsn_display"`
	Pool       PoolSettings `json:"pool"`
	Version    int64        `json:"version"`
	UpdatedAt  time.Time    `json:"updated_at"`
	UpdatedBy  string       `json:"updated_by"`
}

// DSN actions for a save. The dashboard never receives a stored DSN, so it
// cannot send it back unchanged; "keep" is how a form that edits only the pool
// leaves the connection alone.
const (
	DSNKeep    = "keep"
	DSNSet     = "set"
	DSNInherit = "inherit"
)

// DBConfigInput is one save from the dashboard.
type DBConfigInput struct {
	DSNAction string       `json:"dsn_action"`
	DSN       string       `json:"dsn"`
	Pool      PoolSettings `json:"pool"`
	Note      string       `json:"note"`
}

// DBConfigVersion is one history row.
type DBConfigVersion struct {
	Version    int64        `json:"version"`
	Plugin     string       `json:"plugin"`
	Action     string       `json:"action"`
	HasDSN     bool         `json:"has_dsn"`
	DSNDisplay string       `json:"dsn_display"`
	Pool       PoolSettings `json:"pool"`
	Note       string       `json:"note"`
	SavedAt    time.Time    `json:"saved_at"`
	SavedBy    string       `json:"saved_by"`
}

// EffectiveDBConfig is what a plugin actually connects with: its own row laid
// over the default, laid over the built-ins.
type EffectiveDBConfig struct {
	DSN             string
	MaxOpen         int
	MaxIdle         int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
	// DSNSource is the row the DSN comes from: the plugin's own name, or
	// DefaultPlugin.
	DSNSource string
	// Version rises whenever the plugin's row or the default changes, a
	// delete included. A plugin compares it with the version it is running.
	Version int64
}

// ValidatePluginName accepts a plugin's registered name or DefaultPlugin.
func ValidatePluginName(name string) error {
	if name == DefaultPlugin || pluginNameRE.MatchString(name) {
		return nil
	}
	return fmt.Errorf("%w: plugin name %q", ErrInvalid, name)
}

// ValidateDSN checks a connection string is a Postgres URL with a host. The
// key=value form is refused: every service here uses the URL form, and only
// the URL form can be shown with its password masked.
func ValidateDSN(dsn string) error {
	u, err := url.Parse(strings.TrimSpace(dsn))
	if err != nil {
		return fmt.Errorf("%w: dsn is not a URL", ErrInvalid)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return fmt.Errorf("%w: dsn must start with postgres://", ErrInvalid)
	}
	if u.Host == "" {
		return fmt.Errorf("%w: dsn has no host", ErrInvalid)
	}
	return nil
}

// RedactDSN is the form shown on screen and written to the audit trail.
func RedactDSN(dsn string) string {
	u, err := url.Parse(strings.TrimSpace(dsn))
	if err != nil {
		return "(unparseable)"
	}
	return u.Redacted()
}

func validatePool(p PoolSettings) error {
	check := func(name string, v *int, lo, hi int) error {
		if v != nil && (*v < lo || *v > hi) {
			return fmt.Errorf("%w: %s must be between %d and %d", ErrInvalid, name, lo, hi)
		}
		return nil
	}
	if err := check("max_open", p.MaxOpen, 1, maxPoolSize); err != nil {
		return err
	}
	if err := check("max_idle", p.MaxIdle, 0, maxPoolSize); err != nil {
		return err
	}
	if err := check("conn_max_lifetime_s", p.ConnMaxLifetimeSec, 0, maxDurationSecs); err != nil {
		return err
	}
	if err := check("conn_max_idle_s", p.ConnMaxIdleSec, 0, maxDurationSecs); err != nil {
		return err
	}
	if p.MaxOpen != nil && p.MaxIdle != nil && *p.MaxIdle > *p.MaxOpen {
		return fmt.Errorf("%w: max_idle cannot exceed max_open", ErrInvalid)
	}
	return nil
}

func dsnAAD(plugin string) string { return "db_config:" + plugin + ":dsn" }

// row is the shape shared by db_config and db_config_history.
type row struct {
	dsnSealed  sql.NullString
	dsnDisplay sql.NullString
	pool       PoolSettings
}

func intPtr(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int64)
	return &v
}

func nullInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullString(s sql.NullString) any {
	if !s.Valid {
		return nil
	}
	return s.String
}

// ListDBConfigs returns every stored row, the default first.
func (s *Store) ListDBConfigs(ctx context.Context) ([]DBConfig, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT plugin, dsn_sealed, dsn_display, max_open, max_idle,
		conn_max_lifetime_s, conn_max_idle_s, version, updated_at, updated_by
		FROM db_config ORDER BY plugin = '*' DESC, plugin`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DBConfig{}
	for rows.Next() {
		c, err := scanDBConfig(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type scanner interface{ Scan(dest ...any) error }

func scanDBConfig(sc scanner) (DBConfig, error) {
	var c DBConfig
	var sealed, display sql.NullString
	var mo, mi, ml, mit sql.NullInt64
	var at string
	if err := sc.Scan(&c.Plugin, &sealed, &display, &mo, &mi, &ml, &mit, &c.Version, &at, &c.UpdatedBy); err != nil {
		return c, err
	}
	c.HasDSN = sealed.Valid
	c.DSNDisplay = display.String
	c.Pool = PoolSettings{intPtr(mo), intPtr(mi), intPtr(ml), intPtr(mit)}
	c.UpdatedAt = parseTime(at)
	return c, nil
}

func (s *Store) getRow(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, plugin string) (row, bool, error) {
	var r row
	var mo, mi, ml, mit sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT dsn_sealed, dsn_display, max_open, max_idle, conn_max_lifetime_s, conn_max_idle_s
		FROM db_config WHERE plugin = ?`, plugin).Scan(&r.dsnSealed, &r.dsnDisplay, &mo, &mi, &ml, &mit)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	r.pool = PoolSettings{intPtr(mo), intPtr(mi), intPtr(ml), intPtr(mit)}
	return r, true, nil
}

// SaveDBConfig writes a plugin's row (or the default's) and records the
// change in history and the audit trail, all in one transaction.
func (s *Store) SaveDBConfig(ctx context.Context, plugin string, in DBConfigInput, actor string) (DBConfig, error) {
	if err := ValidatePluginName(plugin); err != nil {
		return DBConfig{}, err
	}
	if err := validatePool(in.Pool); err != nil {
		return DBConfig{}, err
	}
	if plugin == DefaultPlugin && in.DSNAction == DSNInherit {
		return DBConfig{}, fmt.Errorf("%w: the default has nothing to inherit from", ErrInvalid)
	}

	// Sealing happens before the transaction: it needs no database, and the
	// store's single connection is held for the whole of a transaction.
	var newSealed, newDisplay sql.NullString
	switch in.DSNAction {
	case DSNSet:
		if err := ValidateDSN(in.DSN); err != nil {
			return DBConfig{}, err
		}
		sealed, err := s.sealSecret(strings.TrimSpace(in.DSN), dsnAAD(plugin))
		if err != nil {
			return DBConfig{}, err
		}
		newSealed = sql.NullString{String: sealed, Valid: true}
		newDisplay = sql.NullString{String: RedactDSN(in.DSN), Valid: true}
	case DSNKeep, DSNInherit, "":
	default:
		return DBConfig{}, fmt.Errorf("%w: dsn_action must be keep, set or inherit", ErrInvalid)
	}

	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if in.DSNAction == DSNKeep || in.DSNAction == "" {
			cur, ok, err := s.getRow(ctx, tx, plugin)
			if err != nil {
				return err
			}
			if ok {
				newSealed, newDisplay = cur.dsnSealed, cur.dsnDisplay
			}
		}
		r := row{dsnSealed: newSealed, dsnDisplay: newDisplay, pool: in.Pool}
		detail := describe(r)
		if in.Note != "" {
			detail += " — " + in.Note
		}
		if err := writeVersion(ctx, tx, plugin, "save", r, in.Note, actor); err != nil {
			return err
		}
		return writeAudit(ctx, tx, actor, "db_config.save", plugin, detail)
	})
	if err != nil {
		return DBConfig{}, err
	}
	return s.GetDBConfig(ctx, plugin)
}

// writeVersion appends a history row and makes it the plugin's current row.
func writeVersion(ctx context.Context, tx *sql.Tx, plugin, action string, r row, note, actor string) error {
	at := now()
	res, err := tx.ExecContext(ctx, `INSERT INTO db_config_history
		(plugin, action, dsn_sealed, dsn_display, max_open, max_idle, conn_max_lifetime_s, conn_max_idle_s, note, saved_at, saved_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		plugin, action, nullString(r.dsnSealed), nullString(r.dsnDisplay),
		nullInt(r.pool.MaxOpen), nullInt(r.pool.MaxIdle), nullInt(r.pool.ConnMaxLifetimeSec), nullInt(r.pool.ConnMaxIdleSec),
		note, at, actor)
	if err != nil {
		return err
	}
	version, err := res.LastInsertId()
	if err != nil {
		return err
	}
	if action == "delete" {
		_, err = tx.ExecContext(ctx, `DELETE FROM db_config WHERE plugin = ?`, plugin)
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO db_config
		(plugin, dsn_sealed, dsn_display, max_open, max_idle, conn_max_lifetime_s, conn_max_idle_s, version, updated_at, updated_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (plugin) DO UPDATE SET
			dsn_sealed = excluded.dsn_sealed, dsn_display = excluded.dsn_display,
			max_open = excluded.max_open, max_idle = excluded.max_idle,
			conn_max_lifetime_s = excluded.conn_max_lifetime_s, conn_max_idle_s = excluded.conn_max_idle_s,
			version = excluded.version, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
		plugin, nullString(r.dsnSealed), nullString(r.dsnDisplay),
		nullInt(r.pool.MaxOpen), nullInt(r.pool.MaxIdle), nullInt(r.pool.ConnMaxLifetimeSec), nullInt(r.pool.ConnMaxIdleSec),
		version, at, actor)
	return err
}

// describe summarises a row for the audit trail, without the password.
func describe(r row) string {
	parts := []string{}
	if r.dsnSealed.Valid {
		parts = append(parts, "dsn "+r.dsnDisplay.String)
	} else {
		parts = append(parts, "dsn inherited")
	}
	add := func(name string, v *int) {
		if v != nil {
			parts = append(parts, fmt.Sprintf("%s %d", name, *v))
		}
	}
	add("max_open", r.pool.MaxOpen)
	add("max_idle", r.pool.MaxIdle)
	add("conn_max_lifetime_s", r.pool.ConnMaxLifetimeSec)
	add("conn_max_idle_s", r.pool.ConnMaxIdleSec)
	return strings.Join(parts, ", ")
}

// GetDBConfig returns one stored row.
func (s *Store) GetDBConfig(ctx context.Context, plugin string) (DBConfig, error) {
	c, err := scanDBConfig(s.db.QueryRowContext(ctx, `SELECT plugin, dsn_sealed, dsn_display, max_open, max_idle,
		conn_max_lifetime_s, conn_max_idle_s, version, updated_at, updated_by
		FROM db_config WHERE plugin = ?`, plugin))
	if errors.Is(err, sql.ErrNoRows) {
		return DBConfig{}, ErrNotFound
	}
	return c, err
}

// DeleteDBConfig removes a plugin's own row, so it inherits everything from
// the default again. The default itself cannot be deleted: with nothing to
// fall back to, every plugin relying on it would lose its connection at once.
func (s *Store) DeleteDBConfig(ctx context.Context, plugin, actor string) error {
	if err := ValidatePluginName(plugin); err != nil {
		return err
	}
	if plugin == DefaultPlugin {
		return fmt.Errorf("%w: the default cannot be deleted", ErrInvalid)
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, ok, err := s.getRow(ctx, tx, plugin); err != nil {
			return err
		} else if !ok {
			return ErrNotFound
		}
		if err := writeVersion(ctx, tx, plugin, "delete", row{}, "", actor); err != nil {
			return err
		}
		return writeAudit(ctx, tx, actor, "db_config.delete", plugin, "now inherits the default")
	})
}

// DBConfigHistory returns a plugin's versions, newest first.
func (s *Store) DBConfigHistory(ctx context.Context, plugin string, limit int) ([]DBConfigVersion, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, plugin, action, dsn_sealed, dsn_display, max_open, max_idle,
		conn_max_lifetime_s, conn_max_idle_s, note, saved_at, saved_by
		FROM db_config_history WHERE plugin = ? ORDER BY id DESC LIMIT ?`, plugin, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DBConfigVersion{}
	for rows.Next() {
		v, _, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func scanVersion(sc scanner) (DBConfigVersion, row, error) {
	var v DBConfigVersion
	var r row
	var mo, mi, ml, mit sql.NullInt64
	var at string
	if err := sc.Scan(&v.Version, &v.Plugin, &v.Action, &r.dsnSealed, &r.dsnDisplay, &mo, &mi, &ml, &mit, &v.Note, &at, &v.SavedBy); err != nil {
		return v, r, err
	}
	r.pool = PoolSettings{intPtr(mo), intPtr(mi), intPtr(ml), intPtr(mit)}
	v.HasDSN = r.dsnSealed.Valid
	v.DSNDisplay = r.dsnDisplay.String
	v.Pool = r.pool
	v.SavedAt = parseTime(at)
	return v, r, nil
}

// RollbackDBConfig makes an earlier version current again, as a new version:
// history only ever grows, so the rollback can itself be rolled back.
func (s *Store) RollbackDBConfig(ctx context.Context, plugin string, version int64, actor string) (DBConfig, error) {
	if err := ValidatePluginName(plugin); err != nil {
		return DBConfig{}, err
	}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		v, r, err := scanVersion(tx.QueryRowContext(ctx, `SELECT id, plugin, action, dsn_sealed, dsn_display, max_open, max_idle,
			conn_max_lifetime_s, conn_max_idle_s, note, saved_at, saved_by
			FROM db_config_history WHERE id = ? AND plugin = ?`, version, plugin))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		note := fmt.Sprintf("rollback to version %d", version)
		if v.Action == "delete" {
			// Rolling back to "this plugin had no row" means deleting its row.
			if err := writeVersion(ctx, tx, plugin, "delete", row{}, note, actor); err != nil {
				return err
			}
			return writeAudit(ctx, tx, actor, "db_config.rollback", plugin, note+": now inherits the default")
		}
		// The sealed DSN is copied as is: it was sealed for this same plugin,
		// so it opens under this row's binding.
		if err := writeVersion(ctx, tx, plugin, "rollback", r, note, actor); err != nil {
			return err
		}
		return writeAudit(ctx, tx, actor, "db_config.rollback", plugin, note+": "+describe(r))
	})
	if err != nil {
		return DBConfig{}, err
	}
	c, err := s.GetDBConfig(ctx, plugin)
	if errors.Is(err, ErrNotFound) {
		// A rollback to a delete leaves no row; that is the expected result.
		return DBConfig{Plugin: plugin}, nil
	}
	return c, err
}

// EffectiveDBConfig resolves what plugin connects with. ErrNotFound means no
// DSN is set for it anywhere — neither its own row nor the default.
func (s *Store) EffectiveDBConfig(ctx context.Context, plugin string) (EffectiveDBConfig, error) {
	eff, sealed, err := s.resolve(ctx, plugin)
	if err != nil {
		return EffectiveDBConfig{}, err
	}
	if eff.DSNSource == "" {
		return EffectiveDBConfig{}, ErrNotFound
	}
	if eff.DSN, err = s.openSecret(sealed, dsnAAD(eff.DSNSource)); err != nil {
		return EffectiveDBConfig{}, err
	}
	return eff, nil
}

// ResolveDBConfig is EffectiveDBConfig without opening the DSN: the pool a
// plugin would get and where its DSN comes from, for the dashboard. DSN is
// always empty; DSNSource is empty when no DSN is set anywhere.
func (s *Store) ResolveDBConfig(ctx context.Context, plugin string) (EffectiveDBConfig, error) {
	eff, _, err := s.resolve(ctx, plugin)
	return eff, err
}

func (s *Store) resolve(ctx context.Context, plugin string) (eff EffectiveDBConfig, sealed string, err error) {
	if err := ValidatePluginName(plugin); err != nil {
		return eff, "", err
	}
	var own, def row
	var ownOK, defOK bool
	var ownVer, defVer int64
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		if own, ownOK, err = s.getRow(ctx, tx, plugin); err != nil {
			return err
		}
		if def, defOK, err = s.getRow(ctx, tx, DefaultPlugin); err != nil {
			return err
		}
		// The latest history row of each, not db_config.version: a delete
		// leaves no db_config row but does write history, and it has to move
		// the version too — removing an override changes what the plugin
		// connects with. Taken this way the version only ever rises.
		const latest = `SELECT COALESCE(MAX(id), 0) FROM db_config_history WHERE plugin = ?`
		if err := tx.QueryRowContext(ctx, latest, plugin).Scan(&ownVer); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, latest, DefaultPlugin).Scan(&defVer)
	})
	if err != nil {
		return eff, "", err
	}

	switch {
	case ownOK && own.dsnSealed.Valid:
		sealed, eff.DSNSource = own.dsnSealed.String, plugin
	case defOK && def.dsnSealed.Valid:
		sealed, eff.DSNSource = def.dsnSealed.String, DefaultPlugin
	}

	pick := func(o, d *int, builtin int) int {
		if ownOK && o != nil {
			return *o
		}
		if defOK && d != nil {
			return *d
		}
		return builtin
	}
	eff.MaxOpen = pick(own.pool.MaxOpen, def.pool.MaxOpen, builtinMaxOpen)
	eff.MaxIdle = pick(own.pool.MaxIdle, def.pool.MaxIdle, builtinMaxIdle)
	eff.ConnMaxLifetime = time.Duration(pick(own.pool.ConnMaxLifetimeSec, def.pool.ConnMaxLifetimeSec, int(builtinConnMaxLifetime.Seconds()))) * time.Second
	eff.ConnMaxIdleTime = time.Duration(pick(own.pool.ConnMaxIdleSec, def.pool.ConnMaxIdleSec, int(builtinConnMaxIdleTime.Seconds()))) * time.Second
	eff.Version = max(ownVer, defVer)
	// Inherited fields can combine into idle > open (a plugin lowers max_open
	// below the default's max_idle). database/sql would quietly cap it; capping
	// here keeps what the dashboard shows equal to what the plugin does.
	if eff.MaxIdle > eff.MaxOpen {
		eff.MaxIdle = eff.MaxOpen
	}
	return eff, sealed, nil
}
