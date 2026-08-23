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

// TenantSchemaShared is the TenantSchema value that asks Core for the tenant's
// base schema instead of a schema of the plugin's own.
const TenantSchemaShared = "shared"

// NavItem is one entry in the suite's sidebar.
type NavItem struct {
	// Key identifies the entry within its plugin. Plugin name plus key is what
	// makes it unique across the suite, so two plugins may both have "reports".
	Key string `json:"key"`

	// Labels is the entry's text per locale ("bn", "en"), already translated.
	//
	// Not a translation key, which is what this first looked like it should be.
	// A key is only meaningful to the catalogue that defines it, and a plugin
	// cannot read another plugin's catalogue — Schoolyze asked to render
	// "nav.accounts" would show the raw key. Nor can Core translate: it holds no
	// catalogue and learning to would be Core learning what a label is.
	//
	// So the translated strings travel, and the plugin rendering the menu picks
	// the locale it is rendering in. The cost is that a plugin must ship its nav
	// labels in every locale the suite supports, and adding a locale means every
	// plugin declaring it — which is the honest price of nobody owning everyone
	// else's words.
	Labels map[string]string `json:"labels"`

	// Icon is a Lucide icon name, resolved by whatever renders the menu.
	Icon string `json:"icon,omitempty"`

	// Href is the path Core proxies, including the plugin's own prefix.
	Href string `json:"href"`

	// Permission hides the entry from a user who could not open it anyway.
	// Empty means any authenticated user of the tenant.
	Permission string `json:"permission,omitempty"`

	// Feature hides the entry when the tenant's plan does not include the
	// module. Empty means always shown.
	Feature string `json:"feature,omitempty"`

	// Group and Sort order the union. Entries sort by Group, then Sort, then
	// plugin name — so a plugin controls where its own entries sit relative to
	// each other, and cannot reorder anyone else's.
	//
	// Not a perfect answer to "who decides the order of the sections", and
	// better than a configuration file nobody maintains.
	Group string `json:"group,omitempty"`
	Sort  int    `json:"sort,omitempty"`

	// GroupLabels names the group per locale, for the heading a merged sidebar
	// puts above the run. Same reason the entry labels travel: nothing
	// downstream holds this plugin's catalogue, and Core holds none at all.
	GroupLabels map[string]string `json:"group_labels,omitempty"`

	// GroupSort orders this group against other plugins'. Lower first.
	//
	// Without it groups fall in alphabetical order, which is an accident:
	// "accounting" precedes "schoolyze", so the ledger's three entries sat
	// above the school's dashboard for no reason a user could infer.
	GroupSort int `json:"group_sort,omitempty"`
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

	// TenantSchema says which Postgres schema this plugin's tenant-scoped tables
	// live in, and so which schema Core names in X-ApiCoreX-Schema when it
	// proxies a request.
	//
	//   "own" (default, and what an omitted field means) — the plugin owns
	//     tenant_<slug>__<name>, a schema nothing else can reach. This is what
	//     stops one domain plugin reading another's tables: not a rule in a
	//     document, but a schema its database role has no grant on.
	//
	//   "shared" — the plugin is handed the tenant's base schema, tenant_<slug>.
	//     Reserved for the platform itself: Identity owns that schema and the
	//     tenant record that names it. A domain plugin declaring this is asking
	//     for the very thing the split exists to prevent.
	//
	// Core neither knows nor cares which plugin is which — it reads this field.
	// That is the point: the alternative was a plugin name hardcoded in Core,
	// which is how a gateway starts learning domain.
	TenantSchema string `json:"tenant_schema,omitempty"`

	// Nav is this plugin's sidebar entries, for the merged menu.
	//
	// Core collects them from every plugin a tenant has, filters by what the
	// user may actually open, and injects the union as a header — so each
	// plugin renders one menu covering the whole suite instead of its own
	// island. Core reads the fields it filters and orders by and interprets
	// nothing else.
	Nav []NavItem `json:"nav,omitempty"`

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
