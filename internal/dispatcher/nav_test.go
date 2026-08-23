package dispatcher

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/msrsiddik/apicorex/internal/manifest"
	"github.com/msrsiddik/apicorex/internal/registry"
)

// Assembling the menu is where a mistake is quiet: an entry the user cannot
// open still renders, or the order changes between page loads, and neither
// fails anything.

func plugin(name string, alive bool, items ...manifest.NavItem) *registry.PluginEntry {
	return &registry.PluginEntry{
		Info:     registry.PluginInfo{PluginName: name},
		Manifest: manifest.Manifest{Name: name, Nav: items},
		Alive:    alive,
	}
}

func item(key, href string, opts ...func(*manifest.NavItem)) manifest.NavItem {
	n := manifest.NavItem{Key: key, Href: href, Labels: map[string]string{"en": key}}
	for _, o := range opts {
		o(&n)
	}
	return n
}

func perm(p string) func(*manifest.NavItem)  { return func(n *manifest.NavItem) { n.Permission = p } }
func feat(f string) func(*manifest.NavItem)  { return func(n *manifest.NavItem) { n.Feature = f } }
func group(g string) func(*manifest.NavItem) { return func(n *manifest.NavItem) { n.Group = g } }
func order(i int) func(*manifest.NavItem)    { return func(n *manifest.NavItem) { n.Sort = i } }

func keys(entries []navEntry) string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Plugin+"/"+e.Key)
	}
	return strings.Join(out, " ")
}

func TestBuildNavMergesEveryPlugin(t *testing.T) {
	got := buildNav([]*registry.PluginEntry{
		plugin("schoolyze", true, item("students", "/school/students")),
		plugin("accounting", true, item("accounts", "/accounting/accounts")),
	}, nil, nil)
	if len(got) != 2 {
		t.Fatalf("got %d entries: %s", len(got), keys(got))
	}
}

func TestBuildNavHidesWhatTheUserCannotOpen(t *testing.T) {
	// Hiding beats showing disabled: the panels already work that way for their
	// own menus, and an entry leading to a 403 helps nobody.
	all := []*registry.PluginEntry{
		plugin("schoolyze", true,
			item("students", "/school/students", perm("student:read")),
			item("fees", "/school/fees", perm("fee:read")),
		),
		plugin("accounting", true, item("accounts", "/accounting/accounts", perm("ledger:read"))),
	}
	got := buildNav(all, []string{"student:read"}, nil)
	if keys(got) != "schoolyze/students" {
		t.Fatalf("got %q", keys(got))
	}
}

func TestBuildNavHonoursWildcardPermissions(t *testing.T) {
	// "student:*" is the shape the RBAC vocabulary already uses, and a menu that
	// ignored it would hide entries from someone who plainly has them.
	all := []*registry.PluginEntry{plugin("schoolyze", true,
		item("students", "/school/students", perm("student:read")),
		item("fees", "/school/fees", perm("fee:read")),
	)}
	if got := keys(buildNav(all, []string{"student:*"}, nil)); got != "schoolyze/students" {
		t.Fatalf("wildcard: got %q", got)
	}
	if got := len(buildNav(all, []string{"*"}, nil)); got != 2 {
		t.Fatalf("full grant showed %d of 2", got)
	}
}

func TestBuildNavHidesModulesTheTenantHasNotBought(t *testing.T) {
	all := []*registry.PluginEntry{plugin("schoolyze", true,
		item("students", "/school/students"),
		item("cards", "/school/cards", feat("schoolyze:cards")),
	)}
	if got := keys(buildNav(all, nil, nil)); got != "schoolyze/students" {
		t.Fatalf("without the feature: got %q", got)
	}
	if got := len(buildNav(all, nil, []string{"schoolyze:cards"})); got != 2 {
		t.Fatalf("with the feature: got %d of 2", got)
	}
}

func TestBuildNavSkipsPluginsThatAreDown(t *testing.T) {
	// A link to a plugin that cannot answer is worse than no link: the user
	// clicks it and gets a 503 from something they were told about.
	got := buildNav([]*registry.PluginEntry{
		plugin("schoolyze", true, item("students", "/school/students")),
		plugin("accounting", false, item("accounts", "/accounting/accounts")),
	}, nil, nil)
	if keys(got) != "schoolyze/students" {
		t.Fatalf("got %q", keys(got))
	}
}

func TestBuildNavOrdersStably(t *testing.T) {
	// An unstable menu reorders itself between page loads, which reads as a bug
	// in whatever the user was doing.
	all := []*registry.PluginEntry{
		plugin("zebra", true, item("z1", "/z/1", group("b"), order(1))),
		plugin("accounting", true,
			item("journal", "/accounting/journal", group("a"), order(2)),
			item("accounts", "/accounting/accounts", group("a"), order(1)),
		),
	}
	want := "accounting/accounts accounting/journal zebra/z1"
	for i := 0; i < 5; i++ {
		if got := keys(buildNav(all, nil, nil)); got != want {
			t.Fatalf("run %d: got %q, want %q", i, got, want)
		}
	}
}

func TestBuildNavSkipsIncompleteEntries(t *testing.T) {
	got := buildNav([]*registry.PluginEntry{plugin("schoolyze", true,
		item("", "/school/nowhere"),
		manifest.NavItem{Key: "nohref"},
		item("students", "/school/students"),
	)}, nil, nil)
	if keys(got) != "schoolyze/students" {
		t.Fatalf("got %q", keys(got))
	}
}

func TestEncodeNavRoundTripsBanglaLabels(t *testing.T) {
	// Base64 exists because the labels are Bangla, and a header carrying raw
	// UTF-8 is asking for trouble at every proxy in between.
	entries := []navEntry{{
		Plugin: "accounting", Key: "accounts", Href: "/accounting/accounts",
		Labels: map[string]string{"bn": "হিসাব তালিকা", "en": "Accounts"},
	}}
	encoded := encodeNav(entries)
	if encoded == "" {
		t.Fatal("encoded to nothing")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("not valid base64: %v", err)
	}
	var back []navEntry
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if back[0].Labels["bn"] != "হিসাব তালিকা" {
		t.Fatalf("Bangla label came back as %q", back[0].Labels["bn"])
	}
}

func TestEncodeNavDropsAMenuTooBigForAHeader(t *testing.T) {
	// Truncating would hand someone a menu missing entries with nothing said.
	// Dropping it lets the plugin fall back to its own, which is what it had
	// before any of this.
	var many []navEntry
	for i := 0; i < 400; i++ {
		many = append(many, navEntry{
			Plugin: "schoolyze", Key: "k", Href: "/school/somewhere/quite/long",
			Labels: map[string]string{"bn": "অনেক লম্বা একটি লেবেল", "en": "a fairly long label"},
		})
	}
	if encodeNav(many) != "" {
		t.Fatal("an oversized menu was encoded anyway")
	}
}

func TestEncodeNavEmptyIsEmpty(t *testing.T) {
	if encodeNav(nil) != "" {
		t.Fatal("an empty menu produced a header")
	}
}
