package registry

import (
	"testing"
	"time"
)

// A maintenance window is the one thing here that can take a plugin down on
// purpose, so the ways it ends matter more than the way it starts.

func TestMaintenanceWindowApplies(t *testing.T) {
	r := New()
	r.SetMaintenance("schoolyze", "moving tables", time.Minute)
	w, ok := r.MaintenanceFor("schoolyze")
	if !ok || w.Reason != "moving tables" {
		t.Fatalf("window not open: %+v", w)
	}
	if _, ok := r.MaintenanceFor("accounting"); ok {
		t.Error("a window on one plugin closed another")
	}
}

func TestMaintenanceExpiresOnItsOwn(t *testing.T) {
	// The property the whole design rests on: a caller that opens a window and
	// then dies leaves a plugin unavailable for minutes, not forever.
	r := New()
	r.SetMaintenance("schoolyze", "crashed mid-migration", time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	if _, ok := r.MaintenanceFor("schoolyze"); ok {
		t.Fatal("window outlived its expiry")
	}
	if len(r.Maintenances()) != 0 {
		t.Error("expired window still listed")
	}
}

func TestMaintenanceIsCappedNotRefused(t *testing.T) {
	// The caller is a migration runner. Refusing its request would leave it
	// choosing between proceeding unprotected and not proceeding.
	r := New()
	w := r.SetMaintenance("schoolyze", "long job", 48*time.Hour)
	if time.Until(w.Until) > maxMaintenance+time.Second {
		t.Fatalf("window not capped: %s", time.Until(w.Until))
	}
	w = r.SetMaintenance("accounting", "no duration given", 0)
	if time.Until(w.Until) <= 0 {
		t.Fatalf("zero duration produced a closed window: %+v", w)
	}
}

func TestExtendingAWindowKeepsWhenItStarted(t *testing.T) {
	// So a dashboard shows how long this has really been going on, rather than
	// resetting the clock every time the runner extends it.
	r := New()
	first := r.SetMaintenance("schoolyze", "step 1", time.Minute)
	time.Sleep(2 * time.Millisecond)
	second := r.SetMaintenance("schoolyze", "step 2", time.Minute)
	if !second.Since.Equal(first.Since) {
		t.Errorf("Since moved from %s to %s", first.Since, second.Since)
	}
	if !second.Until.After(first.Until) {
		t.Error("extending did not push the expiry out")
	}
}

func TestClearEndsAWindowEarly(t *testing.T) {
	r := New()
	r.SetMaintenance("schoolyze", "moving tables", time.Hour)
	r.ClearMaintenance("schoolyze")
	if _, ok := r.MaintenanceFor("schoolyze"); ok {
		t.Fatal("window survived a clear")
	}
}
