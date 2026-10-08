package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Command kinds.
const (
	CommandRestart = "restart"
	CommandReload  = "reload"
)

// commandTTL is how long a command waits for its plugin. A plugin heartbeats
// every 15 seconds, so one that has not collected a command in this long is
// down; carrying the command out whenever it comes back — perhaps hours later,
// for reasons nobody remembers — would surprise whoever is looking then.
const commandTTL = 10 * time.Minute

// Command is one instruction to a plugin and what became of it.
type Command struct {
	ID          int64      `json:"id"`
	Plugin      string     `json:"plugin"`
	Kind        string     `json:"kind"`
	State       string     `json:"state"`
	Result      string     `json:"result"`
	RequestedAt time.Time  `json:"requested_at"`
	RequestedBy string     `json:"requested_by"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
}

func optTime(s sql.NullString) *time.Time {
	if !s.Valid {
		return nil
	}
	t := parseTime(s.String)
	return &t
}

const commandCols = `id, plugin, kind, state, result, requested_at, requested_by, delivered_at, finished_at`

func scanCommand(sc scanner) (Command, error) {
	var c Command
	var req string
	var del, fin sql.NullString
	if err := sc.Scan(&c.ID, &c.Plugin, &c.Kind, &c.State, &c.Result, &req, &c.RequestedBy, &del, &fin); err != nil {
		return c, err
	}
	c.RequestedAt = parseTime(req)
	c.DeliveredAt = optTime(del)
	c.FinishedAt = optTime(fin)
	return c, nil
}

// QueueCommand records a command for plugin's next heartbeat, superseding any
// command still waiting for it: two restarts queued by a double click should
// be one restart.
func (s *Store) QueueCommand(ctx context.Context, plugin, kind, actor string) (Command, error) {
	if err := ValidatePluginName(plugin); err != nil || plugin == DefaultPlugin {
		return Command{}, fmt.Errorf("%w: plugin name %q", ErrInvalid, plugin)
	}
	if kind != CommandRestart && kind != CommandReload {
		return Command{}, fmt.Errorf("%w: command must be restart or reload", ErrInvalid)
	}
	var id int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		at := now()
		if _, err := tx.ExecContext(ctx, `UPDATE plugin_commands SET state = 'superseded', finished_at = ?
			WHERE plugin = ? AND state = 'pending'`, at, plugin); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO plugin_commands (plugin, kind, state, requested_at, requested_by)
			VALUES (?, ?, 'pending', ?, ?)`, plugin, kind, at, actor)
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		return writeAudit(ctx, tx, actor, "command."+kind, plugin, fmt.Sprintf("command %d queued", id))
	})
	if err != nil {
		return Command{}, err
	}
	return s.getCommand(ctx, id)
}

func (s *Store) getCommand(ctx context.Context, id int64) (Command, error) {
	c, err := scanCommand(s.db.QueryRowContext(ctx, `SELECT `+commandCols+` FROM plugin_commands WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Command{}, ErrNotFound
	}
	return c, err
}

// TakeCommand hands plugin its waiting command, if any, and marks it
// delivered. Called on every heartbeat, so it must be cheap when there is
// nothing: one indexed lookup.
func (s *Store) TakeCommand(ctx context.Context, plugin string) (*Command, error) {
	var out *Command
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		c, err := scanCommand(tx.QueryRowContext(ctx, `SELECT `+commandCols+` FROM plugin_commands
			WHERE plugin = ? AND state = 'pending' ORDER BY id DESC LIMIT 1`, plugin))
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		at := now()
		if time.Since(c.RequestedAt) > commandTTL {
			_, err := tx.ExecContext(ctx, `UPDATE plugin_commands SET state = 'expired', finished_at = ? WHERE id = ?`, at, c.ID)
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE plugin_commands SET state = 'delivered', delivered_at = ? WHERE id = ?`, at, c.ID); err != nil {
			return err
		}
		c.State = "delivered"
		t := parseTime(at)
		c.DeliveredAt = &t
		out = &c
		return nil
	})
	return out, err
}

// FinishCommand records how a delivered command went, as the plugin reports
// it. plugin must be the one the command was for: a plugin cannot close
// another's command.
func (s *Store) FinishCommand(ctx context.Context, id int64, plugin string, ok bool, message string) (Command, error) {
	if len(message) > 2000 {
		message = message[:2000]
	}
	state := "done"
	if !ok {
		state = "failed"
	}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE plugin_commands SET state = ?, result = ?, finished_at = ?
			WHERE id = ? AND plugin = ? AND state = 'delivered'`, state, message, now(), id, plugin)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return writeAudit(ctx, tx, "plugin:"+plugin, "command."+state, plugin, fmt.Sprintf("command %d: %s", id, message))
	})
	if err != nil {
		return Command{}, err
	}
	return s.getCommand(ctx, id)
}

// ListCommands returns plugin's recent commands, newest first.
func (s *Store) ListCommands(ctx context.Context, plugin string, limit int) ([]Command, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+commandCols+` FROM plugin_commands
		WHERE plugin = ? ORDER BY id DESC LIMIT ?`, plugin, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Command{}
	for rows.Next() {
		c, err := scanCommand(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
