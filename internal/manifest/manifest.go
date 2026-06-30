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
}
