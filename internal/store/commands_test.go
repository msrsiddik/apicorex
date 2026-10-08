package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCommandLifecycle(t *testing.T) {
	s := openTest(t, nil)
	ctx := context.Background()
	c, err := s.QueueCommand(ctx, "schoolyze", CommandReload, "dashboard")
	if err != nil || c.State != "pending" {
		t.Fatalf("%+v %v", c, err)
	}

	got, err := s.TakeCommand(ctx, "schoolyze")
	if err != nil || got == nil || got.ID != c.ID || got.State != "delivered" || got.DeliveredAt == nil {
		t.Fatalf("take: %+v %v", got, err)
	}
	// Delivered once: the next heartbeat must not run it again.
	if again, _ := s.TakeCommand(ctx, "schoolyze"); again != nil {
		t.Fatalf("delivered twice: %+v", again)
	}

	if _, err := s.FinishCommand(ctx, c.ID, "schoolyze", false, "dial tcp: connection refused"); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListCommands(ctx, "schoolyze", 10)
	if len(list) != 1 || list[0].State != "failed" || list[0].Result != "dial tcp: connection refused" || list[0].FinishedAt == nil {
		t.Fatalf("%+v", list)
	}
	// A result arrives once; a second report is not a second outcome.
	if _, err := s.FinishCommand(ctx, c.ID, "schoolyze", true, "ok"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("finished twice: %v", err)
	}
}

func TestNewCommandSupersedesWaitingOne(t *testing.T) {
	s := openTest(t, nil)
	ctx := context.Background()
	first, _ := s.QueueCommand(ctx, "schoolyze", CommandRestart, "dashboard")
	second, _ := s.QueueCommand(ctx, "schoolyze", CommandRestart, "dashboard")
	got, _ := s.TakeCommand(ctx, "schoolyze")
	if got == nil || got.ID != second.ID {
		t.Fatalf("took %+v, want %d", got, second.ID)
	}
	list, _ := s.ListCommands(ctx, "schoolyze", 10)
	for _, c := range list {
		if c.ID == first.ID && c.State != "superseded" {
			t.Fatalf("first command left as %s", c.State)
		}
	}
}

func TestCommandIsForItsPluginOnly(t *testing.T) {
	s := openTest(t, nil)
	ctx := context.Background()
	c, _ := s.QueueCommand(ctx, "accounting", CommandRestart, "dashboard")
	if got, _ := s.TakeCommand(ctx, "schoolyze"); got != nil {
		t.Fatalf("schoolyze took accounting's command: %+v", got)
	}
	s.TakeCommand(ctx, "accounting")
	if _, err := s.FinishCommand(ctx, c.ID, "schoolyze", true, "ok"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another plugin closed the command: %v", err)
	}
}

func TestStaleCommandExpires(t *testing.T) {
	// A plugin that was down for an hour must not restart the moment it comes
	// back because of a click nobody remembers.
	s := openTest(t, nil)
	ctx := context.Background()
	c, _ := s.QueueCommand(ctx, "schoolyze", CommandRestart, "dashboard")
	old := time.Now().Add(-commandTTL - time.Minute).UTC().Format(time.RFC3339Nano)
	if _, err := s.db.Exec(`UPDATE plugin_commands SET requested_at = ? WHERE id = ?`, old, c.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := s.TakeCommand(ctx, "schoolyze"); err != nil || got != nil {
		t.Fatalf("stale command delivered: %+v %v", got, err)
	}
	list, _ := s.ListCommands(ctx, "schoolyze", 10)
	if list[0].State != "expired" {
		t.Fatalf("state %s, want expired", list[0].State)
	}
}

func TestQueueValidation(t *testing.T) {
	s := openTest(t, nil)
	ctx := context.Background()
	if _, err := s.QueueCommand(ctx, "schoolyze", "shutdown", "dashboard"); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown kind: %v", err)
	}
	if _, err := s.QueueCommand(ctx, DefaultPlugin, CommandRestart, "dashboard"); !errors.Is(err, ErrInvalid) {
		t.Errorf("command to the default row: %v", err)
	}
}
