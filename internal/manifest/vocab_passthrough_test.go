package manifest

import (
	"encoding/json"
	"testing"
)

// Core parses a plugin's manifest into Manifest and re-serves *that struct* on
// /_core/plugins/{name}/manifest, which is where Identity reads it from. Any
// field missing from the struct is therefore silently dropped in transit — so
// the plugin's declared RBAC vocabulary must survive the round trip even
// though Core itself never interprets it.
func TestManifest_PassesThroughPluginVocabulary(t *testing.T) {
	const fromPlugin = `{
      "name": "schoolyze",
      "version": "0.1.0",
      "plugin_type": "internal",
      "routes": [],
      "permissions": [
        {"permission": "student:read", "description": "View students"},
        {"permission": "fee:collect", "resource_group": "Fees"}
      ],
      "roles": [
        {"slug": "teacher", "name": "Teacher",
         "permissions": ["student:read", "attendance:write"]}
      ]
    }`

	var m Manifest
	if err := json.Unmarshal([]byte(fromPlugin), &m); err != nil {
		t.Fatalf("core decode: %v", err)
	}
	reserved, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("core re-marshal: %v", err)
	}

	// Decoded the way Identity decodes it (internal/pluginmgr.manifestJSON).
	var got struct {
		Permissions []struct {
			Permission    string `json:"permission"`
			Description   string `json:"description"`
			ResourceGroup string `json:"resource_group"`
		} `json:"permissions"`
		Roles []struct {
			Slug        string   `json:"slug"`
			Permissions []string `json:"permissions"`
		} `json:"roles"`
	}
	if err := json.Unmarshal(reserved, &got); err != nil {
		t.Fatalf("identity decode: %v", err)
	}

	if len(got.Permissions) != 2 {
		t.Fatalf("permissions = %d, want 2 (Core dropped them): %s", len(got.Permissions), reserved)
	}
	if got.Permissions[0].Description != "View students" {
		t.Errorf("description lost in transit: %+v", got.Permissions[0])
	}
	if got.Permissions[1].ResourceGroup != "Fees" {
		t.Errorf("resource_group lost in transit: %+v", got.Permissions[1])
	}
	if len(got.Roles) != 1 || got.Roles[0].Slug != "teacher" {
		t.Fatalf("roles lost in transit: %+v", got.Roles)
	}
	if len(got.Roles[0].Permissions) != 2 {
		t.Errorf("role permissions lost in transit: %+v", got.Roles[0])
	}
}

// A plugin that declares no vocabulary — every plugin before this feature —
// must round-trip unchanged, with no empty keys appearing in Core's output.
func TestManifest_NoVocabularyIsOmitted(t *testing.T) {
	var m Manifest
	if err := json.Unmarshal([]byte(`{"name":"plain","version":"1","routes":[]}`), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	for _, key := range []string{"permissions", "roles"} {
		if _, present := got[key]; present {
			t.Errorf("%q present for a plugin that declared none: %s", key, out)
		}
	}
}
