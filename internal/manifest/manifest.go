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

// DomainSurface declares one of a plugin's URLs as reachable by a tenant's own
// custom domain (e.g. Schoolyze's panel/portal/website — see
// docs/multi-tenant-plan.md in schoolyze-server). Surface is a plugin-defined
// name, opaque to Core; PathPrefix is where Core rewrites a resolved request
// to before proxying — see dispatcher.resolveByHost.
type DomainSurface struct {
	Surface    string `json:"surface"`
	PathPrefix string `json:"path_prefix"`
}

// Manifest is the document Core pulls from a plugin's /_apicorex/manifest.
type Manifest struct {
	Name           string          `json:"name"`
	Version        string          `json:"version"`
	Description    string          `json:"description,omitempty"`
	PluginType     string          `json:"plugin_type"`
	Routes         []Route         `json:"routes"`
	PublicPaths    []string        `json:"public_paths,omitempty"`
	DomainSurfaces []DomainSurface `json:"domain_surfaces,omitempty"`
	Migrations     []Migration     `json:"migrations,omitempty"`
	OpenAPISpec    json.RawMessage `json:"openapi_spec,omitempty"`

	// Permissions and Roles are the plugin's declared RBAC vocabulary. Core does
	// not interpret them — it enforces the per-route `permission` above and
	// nothing more. They are carried verbatim so Identity, which pulls this
	// manifest from Core's control plane, can offer them in its role editor and
	// seed the roles per tenant. Kept as raw JSON for the same reason
	// migrations are a Core concern only in transit: the meaning belongs to
	// Identity, and Core should not need a release when that shape changes.
	Permissions json.RawMessage `json:"permissions,omitempty"`
	Roles       json.RawMessage `json:"roles,omitempty"`

	// Features are the plugin's user-visible modules, carried verbatim for the
	// same reason and by the same route as Permissions above: Identity packages
	// them into plans and resolves them per tenant, and Core stays ignorant of
	// what any of them mean. Note the asymmetry with the per-route `permission`
	// field — Core enforces permissions at the gateway, but never features. A
	// feature governs what a plugin's own UI offers, which only the plugin can
	// police; Core would have to be told which routes belong to which module,
	// and that is exactly the domain knowledge this design keeps out of it.
	Features json.RawMessage `json:"features,omitempty"`
}
