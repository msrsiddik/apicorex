package store

import (
	"context"
	"strings"
	"testing"
)

func sp(s string) *string { return &s }

func TestSettingsSaveListClear(t *testing.T) {
	s := openTest(t, nil)
	ctx := context.Background()

	v1, err := s.SaveSettings(ctx, "schoolyze", map[string]*string{
		"PDF_MAX_CONCURRENT": sp("5"),
		"PLATFORM_DOMAIN":    sp("schoolyze.com"),
	}, "more printing", "dashboard")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.ListSettings(ctx, "schoolyze")
	if got["PDF_MAX_CONCURRENT"].Value != "5" || got["PLATFORM_DOMAIN"].Value != "schoolyze.com" {
		t.Fatalf("%+v", got)
	}

	// Saving the same values again writes nothing, and the version stays.
	v2, _ := s.SaveSettings(ctx, "schoolyze", map[string]*string{"PDF_MAX_CONCURRENT": sp("5")}, "", "dashboard")
	if v2 != v1 {
		t.Fatalf("an unchanged save moved the version %d → %d", v1, v2)
	}

	// Clearing a key moves the version even though it leaves no row: the
	// plugin must notice it is back on its environment or default.
	v3, _ := s.SaveSettings(ctx, "schoolyze", map[string]*string{"PDF_MAX_CONCURRENT": nil}, "", "dashboard")
	if v3 <= v1 {
		t.Fatalf("clearing did not move the version (%d → %d)", v1, v3)
	}
	got, _ = s.ListSettings(ctx, "schoolyze")
	if _, still := got["PDF_MAX_CONCURRENT"]; still || got["PLATFORM_DOMAIN"].Value != "schoolyze.com" {
		t.Fatalf("%+v", got)
	}

	hist, _ := s.SettingsHistory(ctx, "schoolyze", 10)
	if len(hist) != 3 || hist[0].Key != "PDF_MAX_CONCURRENT" || hist[0].Value != nil {
		t.Fatalf("history %+v", hist)
	}
	audit, _ := s.ListAudit(ctx, 10, 0)
	if len(audit) != 2 || !strings.Contains(audit[1].Detail, "PDF_MAX_CONCURRENT=5") || !strings.Contains(audit[1].Detail, "more printing") {
		t.Fatalf("audit %+v", audit)
	}
}

func TestSettingsArePerPlugin(t *testing.T) {
	s := openTest(t, nil)
	ctx := context.Background()
	s.SaveSettings(ctx, "schoolyze", map[string]*string{"X": sp("1")}, "", "dashboard")
	other, _ := s.ListSettings(ctx, "accounting")
	if len(other) != 0 {
		t.Fatalf("accounting sees schoolyze's settings: %+v", other)
	}
	if v, _ := s.SettingsVersion(ctx, "accounting"); v != 0 {
		t.Fatalf("accounting's version moved with schoolyze's: %d", v)
	}
}

func TestSettingsRejectDefaultRow(t *testing.T) {
	s := openTest(t, nil)
	if _, err := s.SaveSettings(context.Background(), DefaultPlugin, map[string]*string{"X": sp("1")}, "", "dashboard"); err == nil {
		t.Fatal("saved settings for the default row")
	}
}
