package store

import (
	"context"
	"database/sql"
	"time"
)

// AuditEntry is one row of the audit trail.
type AuditEntry struct {
	ID     int64     `json:"id"`
	At     time.Time `json:"at"`
	Actor  string    `json:"actor"`
	Action string    `json:"action"`
	Target string    `json:"target"`
	Detail string    `json:"detail"`
}

// execer is what both *sql.DB and *sql.Tx offer, so audit rows can be written
// inside the transaction of the change they describe.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func writeAudit(ctx context.Context, x execer, actor, action, target, detail string) error {
	_, err := x.ExecContext(ctx,
		`INSERT INTO audit_log (at, actor, action, target, detail) VALUES (?, ?, ?, ?, ?)`,
		now(), actor, action, target, detail)
	return err
}

// Audit records an action that changes nothing in the store itself — a
// restart sent to a plugin, a backup taken. Changes to stored config write
// their own row as part of the change.
func (s *Store) Audit(ctx context.Context, actor, action, target, detail string) error {
	return writeAudit(ctx, s.db, actor, action, target, detail)
}

// ListAudit returns the newest entries first. before pages backwards: pass the
// smallest id of the previous page, or 0 for the newest.
func (s *Store) ListAudit(ctx context.Context, limit int, before int64) ([]AuditEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT id, at, actor, action, target, detail FROM audit_log`
	args := []any{}
	if before > 0 {
		q += ` WHERE id < ?`
		args = append(args, before)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		var at string
		if err := rows.Scan(&e.ID, &at, &e.Actor, &e.Action, &e.Target, &e.Detail); err != nil {
			return nil, err
		}
		e.At = parseTime(at)
		out = append(out, e)
	}
	return out, rows.Err()
}
