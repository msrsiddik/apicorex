package store

import (
	"context"
	"errors"
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

func TestSecretSettingIsSealedAndHidden(t *testing.T) {
	s := openTest(t, testKey(t))
	ctx := context.Background()
	_, err := s.SaveSettingsWithSecrets(ctx, "schoolyze", map[string]*string{
		"PAYMENT_CRED_KEY":   sp("hunter2-payment-key"),
		"PDF_MAX_CONCURRENT": sp("4"),
	}, map[string]bool{"PAYMENT_CRED_KEY": true}, "", "dashboard")
	if err != nil {
		t.Fatal(err)
	}

	// On disk: sealed.
	var raw string
	s.db.QueryRow(`SELECT value FROM plugin_settings WHERE key = 'PAYMENT_CRED_KEY'`).Scan(&raw)
	if strings.Contains(raw, "hunter2") {
		t.Fatal("secret stored in the clear")
	}
	var histRaw string
	s.db.QueryRow(`SELECT value FROM plugin_settings_history WHERE key = 'PAYMENT_CRED_KEY'`).Scan(&histRaw)
	if strings.Contains(histRaw, "hunter2") {
		t.Fatal("secret in history in the clear")
	}

	// The dashboard's views: never the value.
	list, _ := s.ListSettings(ctx, "schoolyze")
	if list["PAYMENT_CRED_KEY"].Value != "" || !list["PAYMENT_CRED_KEY"].Secret {
		t.Fatalf("listing: %+v", list["PAYMENT_CRED_KEY"])
	}
	hist, _ := s.SettingsHistory(ctx, "schoolyze", 10)
	for _, c := range hist {
		if c.Key == "PAYMENT_CRED_KEY" && (c.Value != nil || c.Cleared || !c.Secret) {
			t.Fatalf("history: %+v", c)
		}
	}
	audit, _ := s.ListAudit(ctx, 10, 0)
	for _, a := range audit {
		if strings.Contains(a.Detail, "hunter2") {
			t.Fatalf("secret in the audit trail: %s", a.Detail)
		}
	}

	// The plugin with its own key gets it; anyone else does not.
	with, withheld, err := s.SettingsForPlugin(ctx, "schoolyze", true)
	if err != nil || with["PAYMENT_CRED_KEY"] != "hunter2-payment-key" || withheld != 0 {
		t.Fatalf("own key: %v %d %v", with, withheld, err)
	}
	without, withheld, _ := s.SettingsForPlugin(ctx, "schoolyze", false)
	if _, has := without["PAYMENT_CRED_KEY"]; has || withheld != 1 || without["PDF_MAX_CONCURRENT"] != "4" {
		t.Fatalf("shared key: %v %d", without, withheld)
	}
}

func TestSecretNeedsMasterKey(t *testing.T) {
	s := openTest(t, nil)
	_, err := s.SaveSettingsWithSecrets(context.Background(), "schoolyze", map[string]*string{"X": sp("v")}, map[string]bool{"X": true}, "", "dashboard")
	if !errors.Is(err, ErrNoMasterKey) {
		t.Fatalf("want ErrNoMasterKey, got %v", err)
	}
}

func TestRestoreSecretFromHistory(t *testing.T) {
	// The way back from replacing a set-once key by mistake.
	s := openTest(t, testKey(t))
	ctx := context.Background()
	sec := map[string]bool{"PORTAL_TOKEN_KEY": true}
	s.SaveSettingsWithSecrets(ctx, "schoolyze", map[string]*string{"PORTAL_TOKEN_KEY": sp("original")}, sec, "", "dashboard")
	hist, _ := s.SettingsHistory(ctx, "schoolyze", 10)
	original := hist[0].Version
	s.SaveSettingsWithSecrets(ctx, "schoolyze", map[string]*string{"PORTAL_TOKEN_KEY": sp("oops")}, sec, "", "dashboard")

	v, err := s.RestoreSetting(ctx, "schoolyze", original, "dashboard")
	if err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.SettingsForPlugin(ctx, "schoolyze", true)
	if got["PORTAL_TOKEN_KEY"] != "original" {
		t.Fatalf("restored %q", got["PORTAL_TOKEN_KEY"])
	}
	if v <= original {
		t.Error("restore did not move the version")
	}
	if _, err := s.RestoreSetting(ctx, "accounting", original, "dashboard"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("restored another plugin's version: %v", err)
	}
}

func TestRestoreAClear(t *testing.T) {
	s := openTest(t, nil)
	ctx := context.Background()
	s.SaveSettings(ctx, "schoolyze", map[string]*string{"PDF_MAX_CONCURRENT": sp("5")}, "", "dashboard")
	s.SaveSettings(ctx, "schoolyze", map[string]*string{"PDF_MAX_CONCURRENT": nil}, "", "dashboard")
	hist, _ := s.SettingsHistory(ctx, "schoolyze", 10)
	if !hist[0].Cleared {
		t.Fatalf("clear not marked: %+v", hist[0])
	}
	s.RestoreSetting(ctx, "schoolyze", hist[1].Version, "dashboard")
	got, _, _ := s.SettingsForPlugin(ctx, "schoolyze", false)
	if got["PDF_MAX_CONCURRENT"] != "5" {
		t.Fatalf("%v", got)
	}
}
