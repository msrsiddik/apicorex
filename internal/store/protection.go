package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/msrsiddik/apicorex/internal/config"
)

// ProtectionFields are a protection_limits row's limits. nil means inherit:
// from the default row, and under that from Core's environment and
// CONFIG_FILE. Durations are milliseconds, which is what the dashboard edits.
type ProtectionFields struct {
	RatePerSec       *float64 `json:"rate_per_sec"`
	RateBurst        *float64 `json:"rate_burst"`
	TenantRatePerSec *float64 `json:"tenant_rate_per_sec"`
	TenantRateBurst  *float64 `json:"tenant_rate_burst"`
	BulkheadMax      *int     `json:"bulkhead_max"`
	CBThreshold      *int     `json:"cb_threshold"`
	CBResetTimeoutMs *int64   `json:"cb_reset_timeout_ms"`
	RequestTimeoutMs *int64   `json:"request_timeout_ms"`
}

// ProtectionLimits is a stored row.
type ProtectionLimits struct {
	Plugin    string           `json:"plugin"`
	Limits    ProtectionFields `json:"limits"`
	Version   int64            `json:"version"`
	UpdatedAt time.Time        `json:"updated_at"`
	UpdatedBy string           `json:"updated_by"`
}

// ProtectionVersion is one history row.
type ProtectionVersion struct {
	Version int64            `json:"version"`
	Plugin  string           `json:"plugin"`
	Action  string           `json:"action"`
	Limits  ProtectionFields `json:"limits"`
	Note    string           `json:"note"`
	SavedAt time.Time        `json:"saved_at"`
	SavedBy string           `json:"saved_by"`
}

// Where an effective field comes from, besides a plugin's own name.
const (
	SourceDefault = DefaultPlugin // the dashboard's default row
	SourceConfig  = "config"      // Core's environment or CONFIG_FILE
)

// EffectiveProtection is what a plugin runs with.
type EffectiveProtection struct {
	Limits config.Limits
	// Source says where each field comes from, keyed by its JSON name: the
	// plugin's own name, SourceDefault or SourceConfig.
	Source map[string]string
	// OwnRate reports that rate_per_sec is the plugin's own. A public plugin
	// without one runs at a tenth of the inherited rate.
	OwnRate bool
	// Version rises whenever the plugin's row or the default changes.
	Version int64
}

// Bounds on what a save may set: wide enough for any real deployment, narrow
// enough to catch a slip of the keyboard before it reaches the hot path.
const (
	maxRate           = 1e6
	maxBulkhead       = 100_000
	maxCBThreshold    = 10_000
	minCBResetMs      = 100
	maxCBResetMs      = 24 * 60 * 60 * 1000
	minRequestTimeout = 1000
	maxRequestTimeout = 60 * 60 * 1000
)

func validateProtection(f ProtectionFields) error {
	float := func(name string, v *float64, lo float64, loOpen bool) error {
		if v == nil {
			return nil
		}
		if math.IsNaN(*v) || *v > maxRate || *v < lo || (loOpen && *v == lo) {
			op := "at least"
			if loOpen {
				op = "more than"
			}
			return fmt.Errorf("%w: %s must be %s %g and at most %g", ErrInvalid, name, op, lo, float64(maxRate))
		}
		return nil
	}
	whole := func(name string, v *int64, lo, hi int64) error {
		if v != nil && (*v < lo || *v > hi) {
			return fmt.Errorf("%w: %s must be between %d and %d", ErrInvalid, name, lo, hi)
		}
		return nil
	}
	i64 := func(p *int) *int64 {
		if p == nil {
			return nil
		}
		v := int64(*p)
		return &v
	}
	for _, err := range []error{
		float("rate_per_sec", f.RatePerSec, 0, true),
		float("rate_burst", f.RateBurst, 1, false),
		// Zero is how a plugin's own row turns off a sub-limit the default sets.
		float("tenant_rate_per_sec", f.TenantRatePerSec, 0, false),
		float("tenant_rate_burst", f.TenantRateBurst, 0, false),
		whole("bulkhead_max", i64(f.BulkheadMax), 1, maxBulkhead),
		whole("cb_threshold", i64(f.CBThreshold), 1, maxCBThreshold),
		whole("cb_reset_timeout_ms", f.CBResetTimeoutMs, minCBResetMs, maxCBResetMs),
		whole("request_timeout_ms", f.RequestTimeoutMs, minRequestTimeout, maxRequestTimeout),
	} {
		if err != nil {
			return err
		}
	}
	return nil
}

// fieldsFromConfig is the non-zero part of l, as a row: zero in Limits means
// "not set", and a row's nil means the same.
func fieldsFromConfig(l config.Limits) ProtectionFields {
	var f ProtectionFields
	fl := func(v float64) *float64 {
		if v == 0 {
			return nil
		}
		return &v
	}
	in := func(v int) *int {
		if v == 0 {
			return nil
		}
		return &v
	}
	ms := func(d time.Duration) *int64 {
		if d == 0 {
			return nil
		}
		v := d.Milliseconds()
		return &v
	}
	f.RatePerSec, f.RateBurst = fl(l.RatePerSec), fl(l.RateBurst)
	f.TenantRatePerSec, f.TenantRateBurst = fl(l.TenantRatePerSec), fl(l.TenantRateBurst)
	f.BulkheadMax, f.CBThreshold = in(l.BulkheadMax), in(l.CBThreshold)
	f.CBResetTimeoutMs, f.RequestTimeoutMs = ms(l.CBResetTimeout), ms(l.RequestTimeout)
	return f
}

func (f ProtectionFields) empty() bool { return f == ProtectionFields{} }

const protectionCols = `rate_per_sec, rate_burst, tenant_rate_per_sec, tenant_rate_burst,
	bulkhead_max, cb_threshold, cb_reset_timeout_ms, request_timeout_ms`

func (f ProtectionFields) args() []any {
	fl := func(p *float64) any {
		if p == nil {
			return nil
		}
		return *p
	}
	i64 := func(p *int64) any {
		if p == nil {
			return nil
		}
		return *p
	}
	return []any{fl(f.RatePerSec), fl(f.RateBurst), fl(f.TenantRatePerSec), fl(f.TenantRateBurst),
		nullInt(f.BulkheadMax), nullInt(f.CBThreshold), i64(f.CBResetTimeoutMs), i64(f.RequestTimeoutMs)}
}

// protectionScan collects a row's fields; dest is passed to Scan, fields
// reads them back after.
type protectionScan struct {
	rate, burst, trate, tburst sql.NullFloat64
	bulk, cbt, cbr, rto        sql.NullInt64
}

func (p *protectionScan) dest() []any {
	return []any{&p.rate, &p.burst, &p.trate, &p.tburst, &p.bulk, &p.cbt, &p.cbr, &p.rto}
}

func (p *protectionScan) fields() ProtectionFields {
	fl := func(n sql.NullFloat64) *float64 {
		if !n.Valid {
			return nil
		}
		v := n.Float64
		return &v
	}
	i64 := func(n sql.NullInt64) *int64 {
		if !n.Valid {
			return nil
		}
		v := n.Int64
		return &v
	}
	return ProtectionFields{fl(p.rate), fl(p.burst), fl(p.trate), fl(p.tburst),
		intPtr(p.bulk), intPtr(p.cbt), i64(p.cbr), i64(p.rto)}
}

// ListProtectionLimits returns every stored row, the default first.
func (s *Store) ListProtectionLimits(ctx context.Context) ([]ProtectionLimits, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT plugin, `+protectionCols+`, version, updated_at, updated_by
		FROM protection_limits ORDER BY plugin = '*' DESC, plugin`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProtectionLimits{}
	for rows.Next() {
		var p ProtectionLimits
		var sc protectionScan
		var at string
		dest := append([]any{&p.Plugin}, sc.dest()...)
		if err := rows.Scan(append(dest, &p.Version, &at, &p.UpdatedBy)...); err != nil {
			return nil, err
		}
		p.Limits = sc.fields()
		p.UpdatedAt = parseTime(at)
		out = append(out, p)
	}
	return out, rows.Err()
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getProtection(ctx context.Context, q queryRower, plugin string) (ProtectionFields, bool, error) {
	var sc protectionScan
	err := q.QueryRowContext(ctx, `SELECT `+protectionCols+` FROM protection_limits WHERE plugin = ?`, plugin).Scan(sc.dest()...)
	if errors.Is(err, sql.ErrNoRows) {
		return ProtectionFields{}, false, nil
	}
	if err != nil {
		return ProtectionFields{}, false, err
	}
	return sc.fields(), true, nil
}

// SaveProtectionLimits replaces plugin's row (or the default's): every field
// given is set, every nil one inherits. History and the audit trail are
// written in the same transaction.
func (s *Store) SaveProtectionLimits(ctx context.Context, plugin string, f ProtectionFields, note, actor string) error {
	if err := ValidatePluginName(plugin); err != nil {
		return err
	}
	if err := validateProtection(f); err != nil {
		return err
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := writeProtection(ctx, tx, plugin, "save", f, note, actor); err != nil {
			return err
		}
		return writeAudit(ctx, tx, actor, "protection.save", plugin, withNote(describeProtection(f), note))
	})
}

// DeleteProtectionLimits removes plugin's own row, so it inherits everything
// again. The default row can be deleted too: under it is Core's own config,
// so nothing is left without a limit.
func (s *Store) DeleteProtectionLimits(ctx context.Context, plugin, actor string) error {
	if err := ValidatePluginName(plugin); err != nil {
		return err
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, ok, err := getProtection(ctx, tx, plugin); err != nil {
			return err
		} else if !ok {
			return ErrNotFound
		}
		if err := writeProtection(ctx, tx, plugin, "delete", ProtectionFields{}, "", actor); err != nil {
			return err
		}
		return writeAudit(ctx, tx, actor, "protection.delete", plugin, "now inherits")
	})
}

// ProtectionHistory returns plugin's versions, newest first.
func (s *Store) ProtectionHistory(ctx context.Context, plugin string, limit int) ([]ProtectionVersion, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, plugin, action, `+protectionCols+`, note, saved_at, saved_by
		FROM protection_limits_history WHERE plugin = ? ORDER BY id DESC LIMIT ?`, plugin, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProtectionVersion{}
	for rows.Next() {
		v, err := scanProtectionVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func scanProtectionVersion(sc scanner) (ProtectionVersion, error) {
	var v ProtectionVersion
	var ps protectionScan
	var at string
	dest := append([]any{&v.Version, &v.Plugin, &v.Action}, ps.dest()...)
	if err := sc.Scan(append(dest, &v.Note, &at, &v.SavedBy)...); err != nil {
		return v, err
	}
	v.Limits = ps.fields()
	v.SavedAt = parseTime(at)
	return v, nil
}

// RollbackProtectionLimits makes an earlier version current again, as a new
// version. Rolling back to a delete deletes the row.
func (s *Store) RollbackProtectionLimits(ctx context.Context, plugin string, version int64, actor string) error {
	if err := ValidatePluginName(plugin); err != nil {
		return err
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		v, err := scanProtectionVersion(tx.QueryRowContext(ctx, `SELECT id, plugin, action, `+protectionCols+`, note, saved_at, saved_by
			FROM protection_limits_history WHERE id = ? AND plugin = ?`, version, plugin))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		note := fmt.Sprintf("rollback to version %d", version)
		if v.Action == "delete" {
			if err := writeProtection(ctx, tx, plugin, "delete", ProtectionFields{}, note, actor); err != nil {
				return err
			}
			return writeAudit(ctx, tx, actor, "protection.rollback", plugin, note+": now inherits")
		}
		if err := writeProtection(ctx, tx, plugin, "rollback", v.Limits, note, actor); err != nil {
			return err
		}
		return writeAudit(ctx, tx, actor, "protection.rollback", plugin, note+": "+describeProtection(v.Limits))
	})
}

func writeProtection(ctx context.Context, tx *sql.Tx, plugin, action string, f ProtectionFields, note, actor string) error {
	at := now()
	args := append([]any{plugin, action}, f.args()...)
	res, err := tx.ExecContext(ctx, `INSERT INTO protection_limits_history (plugin, action, `+protectionCols+`, note, saved_at, saved_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, append(args, note, at, actor)...)
	if err != nil {
		return err
	}
	version, err := res.LastInsertId()
	if err != nil {
		return err
	}
	if action == "delete" {
		_, err = tx.ExecContext(ctx, `DELETE FROM protection_limits WHERE plugin = ?`, plugin)
		return err
	}
	args = append([]any{plugin}, f.args()...)
	_, err = tx.ExecContext(ctx, `INSERT INTO protection_limits (plugin, `+protectionCols+`, version, updated_at, updated_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (plugin) DO UPDATE SET
			rate_per_sec = excluded.rate_per_sec, rate_burst = excluded.rate_burst,
			tenant_rate_per_sec = excluded.tenant_rate_per_sec, tenant_rate_burst = excluded.tenant_rate_burst,
			bulkhead_max = excluded.bulkhead_max, cb_threshold = excluded.cb_threshold,
			cb_reset_timeout_ms = excluded.cb_reset_timeout_ms, request_timeout_ms = excluded.request_timeout_ms,
			version = excluded.version, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
		append(args, version, at, actor)...)
	return err
}

func describeProtection(f ProtectionFields) string {
	var parts []string
	fl := func(name string, v *float64) {
		if v != nil {
			parts = append(parts, fmt.Sprintf("%s %g", name, *v))
		}
	}
	in := func(name string, v *int64) {
		if v != nil {
			parts = append(parts, fmt.Sprintf("%s %d", name, *v))
		}
	}
	fl("rate_per_sec", f.RatePerSec)
	fl("rate_burst", f.RateBurst)
	fl("tenant_rate_per_sec", f.TenantRatePerSec)
	fl("tenant_rate_burst", f.TenantRateBurst)
	if f.BulkheadMax != nil {
		parts = append(parts, fmt.Sprintf("bulkhead_max %d", *f.BulkheadMax))
	}
	if f.CBThreshold != nil {
		parts = append(parts, fmt.Sprintf("cb_threshold %d", *f.CBThreshold))
	}
	in("cb_reset_timeout_ms", f.CBResetTimeoutMs)
	in("request_timeout_ms", f.RequestTimeoutMs)
	if len(parts) == 0 {
		return "everything inherited"
	}
	return strings.Join(parts, ", ")
}

func withNote(detail, note string) string {
	if note == "" {
		return detail
	}
	return detail + " — " + note
}

// SeedProtectionLimits copies CONFIG_FILE's per-plugin overrides into the
// store, for each plugin that has never had protection limits here, so they
// show and can be changed in the dashboard. A plugin with any history is
// left alone: the dashboard owns it now, and a file left unchanged on disk
// must not undo what was done there. Returns the plugins seeded, and those
// whose stored row now differs from the file, for the startup log.
func (s *Store) SeedProtectionLimits(ctx context.Context, plugins map[string]config.Limits) (seeded, differ []string, err error) {
	for name, l := range plugins {
		f := fieldsFromConfig(l)
		if err := ValidatePluginName(name); err != nil || name == DefaultPlugin {
			return seeded, differ, fmt.Errorf("CONFIG_FILE plugin %q: %w", name, ErrInvalid)
		}
		if err := validateProtection(f); err != nil {
			return seeded, differ, fmt.Errorf("CONFIG_FILE plugin %q: %w", name, err)
		}
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM protection_limits_history WHERE plugin = ?`, name).Scan(&n); err != nil {
			return seeded, differ, err
		}
		if n > 0 {
			cur, _, err := getProtection(ctx, s.db, name)
			if err != nil {
				return seeded, differ, err
			}
			if !equalFields(cur, f) {
				differ = append(differ, name)
			}
			continue
		}
		if f.empty() {
			continue
		}
		if err := s.SaveProtectionLimits(ctx, name, f, "seeded from CONFIG_FILE", "seed"); err != nil {
			return seeded, differ, err
		}
		seeded = append(seeded, name)
	}
	return seeded, differ, nil
}

func equalFields(a, b ProtectionFields) bool {
	return describeProtection(a) == describeProtection(b)
}

// EffectiveProtection resolves plugin's limits field by field: its own row,
// then the default row, then base — Core's environment and CONFIG_FILE
// default, which already has the built-ins under it.
func (s *Store) EffectiveProtection(ctx context.Context, plugin string, base config.Limits) (EffectiveProtection, error) {
	if err := ValidatePluginName(plugin); err != nil {
		return EffectiveProtection{}, err
	}
	var own, def ProtectionFields
	var ownVer, defVer int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		if own, _, err = getProtection(ctx, tx, plugin); err != nil {
			return err
		}
		if def, _, err = getProtection(ctx, tx, DefaultPlugin); err != nil {
			return err
		}
		const latest = `SELECT COALESCE(MAX(id), 0) FROM protection_limits_history WHERE plugin = ?`
		if err := tx.QueryRowContext(ctx, latest, plugin).Scan(&ownVer); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, latest, DefaultPlugin).Scan(&defVer)
	})
	if err != nil {
		return EffectiveProtection{}, err
	}
	if plugin == DefaultPlugin {
		own = ProtectionFields{}
	}

	eff := EffectiveProtection{Limits: base, Source: map[string]string{}, Version: max(ownVer, defVer)}
	fl := func(name string, o, d *float64, out *float64) {
		switch {
		case o != nil:
			*out, eff.Source[name] = *o, plugin
		case d != nil:
			*out, eff.Source[name] = *d, SourceDefault
		default:
			eff.Source[name] = SourceConfig
		}
	}
	in := func(name string, o, d *int, out *int) {
		switch {
		case o != nil:
			*out, eff.Source[name] = *o, plugin
		case d != nil:
			*out, eff.Source[name] = *d, SourceDefault
		default:
			eff.Source[name] = SourceConfig
		}
	}
	dur := func(name string, o, d *int64, out *time.Duration) {
		switch {
		case o != nil:
			*out, eff.Source[name] = time.Duration(*o)*time.Millisecond, plugin
		case d != nil:
			*out, eff.Source[name] = time.Duration(*d)*time.Millisecond, SourceDefault
		default:
			eff.Source[name] = SourceConfig
		}
	}
	l := &eff.Limits
	fl("rate_per_sec", own.RatePerSec, def.RatePerSec, &l.RatePerSec)
	fl("rate_burst", own.RateBurst, def.RateBurst, &l.RateBurst)
	fl("tenant_rate_per_sec", own.TenantRatePerSec, def.TenantRatePerSec, &l.TenantRatePerSec)
	fl("tenant_rate_burst", own.TenantRateBurst, def.TenantRateBurst, &l.TenantRateBurst)
	in("bulkhead_max", own.BulkheadMax, def.BulkheadMax, &l.BulkheadMax)
	in("cb_threshold", own.CBThreshold, def.CBThreshold, &l.CBThreshold)
	dur("cb_reset_timeout_ms", own.CBResetTimeoutMs, def.CBResetTimeoutMs, &l.CBResetTimeout)
	dur("request_timeout_ms", own.RequestTimeoutMs, def.RequestTimeoutMs, &l.RequestTimeout)
	eff.OwnRate = own.RatePerSec != nil
	return eff, nil
}
