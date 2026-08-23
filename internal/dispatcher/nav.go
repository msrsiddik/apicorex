package dispatcher

import (
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"

	"github.com/msrsiddik/apicorex/internal/registry"
)

// Assembling the suite's sidebar.
//
// Core is the only party that can do this: it holds every registered plugin's
// manifest and the caller's permissions and features, and no plugin knows the
// others exist. What it must not do is render anything — it has no templates and
// no design language, and giving it one would make every plugin's look depend on
// a Core release. So it assembles the list and hands it over; the shell stays in
// the UI kit, where the design is.

// maxNavHeaderBytes caps the injected header.
//
// Headers are not a generous transport: servers commonly refuse a request whose
// headers exceed 8 KB in total, and this one shares that budget with the roles,
// permissions and features already going the same way. Two plugins produce
// roughly 2 KB encoded; eight would not fit, and the honest answer then is an
// endpoint the panel fetches once rather than a header on every request.
//
// Truncating silently would hand someone a menu missing entries with nothing
// said, so this drops the header entirely and lets the plugin fall back to its
// own menu — a plugin's own island, which is what it had before any of this.
const maxNavHeaderBytes = 6 * 1024

// navEntry is the wire shape of one merged entry.
//
// Plugin travels with it so the renderer can mark which section of the suite the
// user is in, and so two plugins can both have a "reports" without colliding.
type navEntry struct {
	Plugin string            `json:"plugin"`
	Key    string            `json:"key"`
	Labels map[string]string `json:"labels"`
	Icon   string            `json:"icon,omitempty"`
	Href   string            `json:"href"`
	Group  string            `json:"group,omitempty"`
}

// buildNav collects the entries this user may open, across every plugin.
//
// perms and features are the caller's, already resolved by Identity. An entry
// the user could not open is left out rather than shown disabled: hiding what
// someone cannot use beats advertising it, which is the rule the panels already
// follow for their own menus.
func buildNav(plugins []*registry.PluginEntry, perms, features []string) []navEntry {
	permSet := setOf(perms)
	featureSet := setOf(features)

	var out []navEntry
	for _, p := range plugins {
		if p == nil || !p.Alive {
			// A plugin that is down cannot serve the page behind its link, and
			// a menu entry leading to a 503 is worse than one that is absent.
			continue
		}
		for _, item := range p.Manifest.Nav {
			if item.Href == "" || item.Key == "" {
				continue
			}
			if item.Permission != "" && !permitted(permSet, item.Permission) {
				continue
			}
			if item.Feature != "" && !featureSet[item.Feature] {
				continue
			}
			out = append(out, navEntry{
				Plugin: p.Info.PluginName,
				Key:    item.Key,
				Labels: item.Labels,
				Icon:   item.Icon,
				Href:   item.Href,
				Group:  item.Group,
			})
		}
	}

	// Group, then the plugin's own ordering within it, then plugin name so the
	// result is stable — an unstable menu reorders itself between page loads.
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Group != b.Group {
			return a.Group < b.Group
		}
		ai, bi := sortOf(plugins, a), sortOf(plugins, b)
		if ai != bi {
			return ai < bi
		}
		return a.Plugin < b.Plugin
	})
	return out
}

// sortOf finds an entry's declared Sort. Looked up rather than carried on the
// wire because it orders the list and then stops being interesting.
func sortOf(plugins []*registry.PluginEntry, e navEntry) int {
	for _, p := range plugins {
		if p == nil || p.Info.PluginName != e.Plugin {
			continue
		}
		for _, item := range p.Manifest.Nav {
			if item.Key == e.Key {
				return item.Sort
			}
		}
	}
	return 0
}

// encodeNav renders the menu for a header.
//
// Base64 because labels are Bangla and a header carrying raw UTF-8 is asking
// for trouble at every proxy between here and the plugin. Returns empty when
// there is nothing to send or when it would not fit — see maxNavHeaderBytes.
func encodeNav(entries []navEntry) string {
	if len(entries) == 0 {
		return ""
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		return ""
	}
	encoded := base64.StdEncoding.EncodeToString(raw)
	if len(encoded) > maxNavHeaderBytes {
		return ""
	}
	return encoded
}

// permitted reports whether a permission is granted, honouring the "resource:*"
// wildcards the RBAC vocabulary already uses.
func permitted(granted map[string]bool, want string) bool {
	if granted[want] || granted["*"] {
		return true
	}
	if i := strings.Index(want, ":"); i > 0 {
		return granted[want[:i]+":*"]
	}
	return false
}

func setOf(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, v := range values {
		if v != "" {
			out[v] = true
		}
	}
	return out
}
