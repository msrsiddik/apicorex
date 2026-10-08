package adminapi

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// ProbeResult is what one test connection learned about a Postgres server.
type ProbeResult struct {
	CurrentUser    string `json:"current_user"`
	ServerVersion  string `json:"server_version"`
	MaxConnections int    `json:"max_connections"`
	// ReservedConnections are kept for superusers; ordinary roles can open
	// MaxConnections minus these.
	ReservedConnections int   `json:"reserved_connections"`
	InUse               int   `json:"in_use"`
	LatencyMS           int64 `json:"latency_ms"`
}

// Prober opens one connection to check a DSN works and read the server's
// connection limits. An interface so the handlers can be tested without a
// Postgres.
type Prober interface {
	Probe(ctx context.Context, dsn string) (ProbeResult, error)
}

// PgxProber is the real Prober.
type PgxProber struct{}

func (PgxProber) Probe(ctx context.Context, dsn string) (ProbeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	start := time.Now()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return ProbeResult{}, err
	}
	defer conn.Close(context.Background())

	var r ProbeResult
	err = conn.QueryRow(ctx, `SELECT current_user,
		current_setting('server_version'),
		current_setting('max_connections')::int,
		current_setting('superuser_reserved_connections')::int,
		(SELECT count(*) FROM pg_stat_activity)::int`).
		Scan(&r.CurrentUser, &r.ServerVersion, &r.MaxConnections, &r.ReservedConnections, &r.InUse)
	if err != nil {
		return ProbeResult{}, err
	}
	r.LatencyMS = time.Since(start).Milliseconds()
	return r, nil
}
