package protection

import (
	"log/slog"
	"os"
	"time"
)

var logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

// RequestLog is the structured record emitted for each proxied request.
type RequestLog struct {
	PluginID  string
	TenantID  string
	Path      string
	Method    string
	Status    int
	Duration  time.Duration
	RequestID string
	Err       error
}

// LogRequest emits a structured JSON log line for a proxied request.
func LogRequest(r RequestLog) {
	attrs := []any{
		"plugin", r.PluginID,
		"tenant", r.TenantID,
		"method", r.Method,
		"path", r.Path,
		"status", r.Status,
		"duration_ms", r.Duration.Milliseconds(),
		"request_id", r.RequestID,
	}
	if r.Err != nil {
		logger.Error("proxied request failed", append(attrs, "error", r.Err.Error())...)
		return
	}
	logger.Info("proxied request", attrs...)
}
