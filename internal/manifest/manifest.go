// Package manifest defines the plugin manifest types Core pulls over HTTP.
// These mirror the JSON a plugin serves at GET /_apicorex/manifest.
package manifest

import "encoding/json"

type Route struct {
	Method     string   `json:"method"`
	Path       string   `json:"path"`
	Public     bool     `json:"public"`
	Permission string   `json:"permission,omitempty"` // required permission to call; "" = any authenticated
	Summary    string   `json:"summary,omitempty"`
	Tags       []string `json:"tags,omitempty"`
}

type Migration struct {
	Version string `json:"version"`
	Name    string `json:"name"`
	UpSQL   string `json:"up_sql"`
	DownSQL string `json:"down_sql"`
}

// Manifest is the document Core pulls from a plugin's /_apicorex/manifest.
type Manifest struct {
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	Description string          `json:"description,omitempty"`
	PluginType  string          `json:"plugin_type"`
	Routes      []Route         `json:"routes"`
	PublicPaths []string        `json:"public_paths,omitempty"`
	Migrations  []Migration     `json:"migrations,omitempty"`
	OpenAPISpec json.RawMessage `json:"openapi_spec,omitempty"`

	// Permissions and Roles are the plugin's declared RBAC vocabulary. Core does
	// not interpret them — it enforces the per-route `permission` above and
	// nothing more. They are carried verbatim so Identity, which pulls this
	// manifest from Core's control plane, can offer them in its role editor and
	// seed the roles per tenant. Kept as raw JSON for the same reason
	// migrations are a Core concern only in transit: the meaning belongs to
	// Identity, and Core should not need a release when that shape changes.
	Permissions json.RawMessage `json:"permissions,omitempty"`
	Roles       json.RawMessage `json:"roles,omitempty"`
}
