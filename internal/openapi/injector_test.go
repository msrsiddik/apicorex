package openapi

import (
	"encoding/json"
	"testing"
)

// The merged spec must carry a root "security" requirement so Scalar's
// "Authorize" button applies globally — otherwise users have to hand-type the
// Authorization header for every request.
func TestMergedSpec_CarriesRootSecurity(t *testing.T) {
	base := []byte(`{
		"paths": {"/health": {}},
		"components": {"securitySchemes": {"bearerAuth": {"type": "http", "scheme": "Bearer"}}},
		"security": [{"bearerAuth": []}]
	}`)
	pluginSpec := []byte(`{
		"paths": {"/me": {}},
		"components": {"securitySchemes": {"bearerAuth": {"type": "http", "scheme": "Bearer"}}},
		"security": [{"bearerAuth": []}]
	}`)

	inj := NewInjector()
	inj.AddRoutes("identity", nil, pluginSpec)

	merged := inj.MergedSpec(base)

	var doc struct {
		Paths    map[string]interface{} `json:"paths"`
		Security []map[string][]string  `json:"security"`
	}
	if err := json.Unmarshal(merged, &doc); err != nil {
		t.Fatalf("unmarshal merged spec: %v (%s)", err, merged)
	}

	if _, ok := doc.Paths["/me"]; !ok {
		t.Error("plugin path /me missing from merged spec")
	}
	if len(doc.Security) != 1 {
		t.Fatalf("security = %v, want exactly 1 requirement (deduped)", doc.Security)
	}
	if _, ok := doc.Security[0]["bearerAuth"]; !ok {
		t.Errorf("security = %v, want to reference bearerAuth", doc.Security)
	}
}

// If only a plugin (not the base spec) declares a security requirement, the
// merged spec still ends up with it.
func TestMergedSpec_AdoptsPluginOnlySecurity(t *testing.T) {
	base := []byte(`{"paths": {"/health": {}}}`)
	pluginSpec := []byte(`{
		"paths": {"/me": {}},
		"security": [{"bearerAuth": []}]
	}`)

	inj := NewInjector()
	inj.AddRoutes("identity", nil, pluginSpec)
	merged := inj.MergedSpec(base)

	var doc struct {
		Security []map[string][]string `json:"security"`
	}
	json.Unmarshal(merged, &doc)
	if len(doc.Security) != 1 {
		t.Fatalf("security = %v, want the plugin's requirement adopted", doc.Security)
	}
}

func TestMergeSecurityRequirements_Dedupes(t *testing.T) {
	a := []interface{}{map[string]interface{}{"bearerAuth": []interface{}{}}}
	b := []interface{}{
		map[string]interface{}{"bearerAuth": []interface{}{}}, // duplicate
		map[string]interface{}{"apiKey": []interface{}{}},     // distinct
	}
	got := mergeSecurityRequirements(a, b)
	if len(got) != 2 {
		t.Fatalf("merged = %v, want 2 distinct requirements", got)
	}
}
