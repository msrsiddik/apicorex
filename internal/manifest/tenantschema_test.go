package manifest

import "testing"

// The cases here are duplicated verbatim in apicorex-identity's
// internal/tenantschema tests. They are the contract between the two
// implementations: if one side changes, this test and that one both fail, which
// is the only warning either repo gets before a plugin starts reading an empty
// schema.
func TestTenantSchemaFor(t *testing.T) {
	cases := []struct {
		name         string
		base, plugin string
		mode         string
		want         string
	}{
		{"own schema by default", "tenant_acme", "schoolyze", "", "tenant_acme__schoolyze"},
		{"own schema, stated", "tenant_acme", "schoolyze", "own", "tenant_acme__schoolyze"},
		{"shared gets the base", "tenant_acme", "identity", TenantSchemaShared, "tenant_acme"},
		{"slug with underscores", "tenant_ji_school", "schoolyze", "", "tenant_ji_school__schoolyze"},
		{"no tenant, no schema", "", "schoolyze", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := TenantSchemaFor(c.base, c.plugin, c.mode); got != c.want {
				t.Fatalf("TenantSchemaFor(%q,%q,%q) = %q, want %q", c.base, c.plugin, c.mode, got, c.want)
			}
		})
	}
}

func TestTenantSchemaForTruncatesDeterministically(t *testing.T) {
	base := "tenant_" + string(make([]byte, 0)) + "a_very_long_institution_slug_that_goes_on_and_on_forever"
	got := TenantSchemaFor(base, "schoolyze", "")
	if len(got) > maxIdentifier {
		t.Fatalf("got %d bytes (%q), Postgres truncates past %d", len(got), got, maxIdentifier)
	}
	if again := TenantSchemaFor(base, "schoolyze", ""); again != got {
		t.Fatalf("not deterministic: %q then %q", got, again)
	}
	// Distinct plugins must not collapse onto one schema once truncated.
	other := TenantSchemaFor(base, "accounting", "")
	if other == got {
		t.Fatalf("two plugins collapsed onto %q", got)
	}
}
