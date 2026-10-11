package dispatcher

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/msrsiddik/apicorex/internal/auth"
	"github.com/msrsiddik/apicorex/internal/config"
	"github.com/msrsiddik/apicorex/internal/manifest"
	"github.com/msrsiddik/apicorex/internal/middleware"
	"github.com/msrsiddik/apicorex/internal/protection"
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

func groupOrder(i int) func(*manifest.NavItem) {
	return func(n *manifest.NavItem) { n.GroupSort = i }
}

func groupLabel(l string) func(*manifest.NavItem) {
	return func(n *manifest.NavItem) { n.GroupLabels = map[string]string{"en": l} }
}

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
	// The shapes the RBAC vocabulary actually uses. "*:*" is the owner's grant,
	// and a matcher of our own missed it — the merged menu came back with one
	// entry for a user who can open everything, which is how this was found.
	all := []*registry.PluginEntry{plugin("schoolyze", true,
		item("students", "/school/students", perm("student:read")),
		item("fees", "/school/fees", perm("fee:read")),
	)}
	if got := keys(buildNav(all, []string{"student:*"}, nil)); got != "schoolyze/students" {
		t.Fatalf("resource wildcard: got %q", got)
	}
	if got := len(buildNav(all, []string{"*:*"}, nil)); got != 2 {
		t.Fatalf("owner grant showed %d of 2", got)
	}
}

func TestBuildNavHidesModulesTheTenantHasNotBought(t *testing.T) {
	all := []*registry.PluginEntry{plugin("schoolyze", true,
		item("students", "/school/students"),
		item("cards", "/school/cards", feat("cards")),
	)}
	if got := keys(buildNav(all, nil, nil)); got != "schoolyze/students" {
		t.Fatalf("without the feature: got %q", got)
	}
	if got := len(buildNav(all, nil, []string{"schoolyze:cards"})); got != 2 {
		t.Fatalf("with the feature: got %d of 2", got)
	}
}

func TestBuildNavQualifiesFeatureKeysByPlugin(t *testing.T) {
	// A plugin writing "attendance" is the natural thing, and the features
	// header carries "schoolyze:attendance". Core scopes the key rather than
	// asking every plugin to remember — eight of eleven entries disappeared from
	// the merged menu for a tenant that had every one of those modules.
	unqualified := []*registry.PluginEntry{plugin("schoolyze", true,
		item("attendance", "/school/attendance", feat("attendance")))}
	if got := len(buildNav(unqualified, nil, []string{"schoolyze:attendance"})); got != 1 {
		t.Fatalf("unqualified key matched %d entries, want 1", got)
	}
	// A plugin that qualifies its own is redundant rather than wrong.
	qualified := []*registry.PluginEntry{plugin("schoolyze", true,
		item("attendance", "/school/attendance", feat("schoolyze:attendance")))}
	if got := len(buildNav(qualified, nil, []string{"schoolyze:attendance"})); got != 1 {
		t.Fatalf("qualified key matched %d entries, want 1", got)
	}
	// And another plugin's module does not unlock this one's entry.
	if got := len(buildNav(unqualified, nil, []string{"accounting:attendance"})); got != 0 {
		t.Fatalf("another plugin's feature matched %d entries", got)
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

func TestBuildNavPutsGroupsInTheirDeclaredOrder(t *testing.T) {
	// The case this exists for: alphabetically "accounting" precedes
	// "schoolyze", so the ledger's entries sat above the school's dashboard —
	// an order nobody chose and no user could infer.
	all := []*registry.PluginEntry{
		plugin("accounting", true,
			item("accounts", "/accounting/accounts", group("accounting"), groupOrder(10)),
		),
		plugin("schoolyze", true,
			item("dashboard", "/school/", group("schoolyze"), groupOrder(0)),
		),
	}
	want := "schoolyze/dashboard accounting/accounts"
	for i := 0; i < 5; i++ {
		if got := keys(buildNav(all, nil, nil)); got != want {
			t.Fatalf("run %d: got %q, want %q", i, got, want)
		}
	}
}

func TestBuildNavCarriesGroupLabels(t *testing.T) {
	// The renderer draws the heading and cannot translate it — Core holds no
	// catalogue — so the words have to travel with the entry.
	got := buildNav([]*registry.PluginEntry{
		plugin("accounting", true,
			item("accounts", "/accounting/accounts", group("accounting"), groupLabel("Accounting")),
		),
	}, nil, nil)
	if len(got) != 1 || got[0].GroupLabels["en"] != "Accounting" {
		t.Fatalf("got %+v", got)
	}
	var decoded []navEntry
	raw, err := base64.StdEncoding.DecodeString(encodeNav(got))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded[0].GroupLabels["en"] != "Accounting" {
		t.Fatalf("label did not survive the header: %+v", decoded[0])
	}
}

// Someone who signs in at /login with nowhere to return to is sent to "/".
// Nothing owns it, so that used to be {"error":"no plugin handles this route"}
// after a sign-in that had worked. A signed-in browser now goes to the first
// page of its menu; anyone or anything else still gets the 404.
func TestRootSendsASignedInBrowserToItsFirstPage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	reg := registry.New()
	d := New(reg, protection.NewCircuitBreaker(5, time.Second), protection.NewBulkhead(10), config.Defaults())
	noProxy := func(u *url.URL) *httputil.ReverseProxy { return &httputil.ReverseProxy{} }
	if err := reg.Register("acc-1", "accounting", "http://acc", "1", "internal", manifest.Manifest{
		Name: "accounting",
		Nav:  []manifest.NavItem{item("ledger", "/accounting/", perm("ledger:read"), groupOrder(20))},
	}, noProxy); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register("sch-1", "schoolyze", "http://sch", "1", "internal", manifest.Manifest{
		Name: "schoolyze",
		Nav:  []manifest.NavItem{item("dashboard", "/school/", groupOrder(10))},
	}, noProxy); err != nil {
		t.Fatal(err)
	}

	serve := func(path, accept string, id *auth.Identity) *httptest.ResponseRecorder {
		e := gin.New()
		e.Use(func(c *gin.Context) {
			if id != nil {
				middleware.SetIdentity(c, id)
			}
		})
		e.NoRoute(d.Dispatch)
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Accept", accept)
		w := httptest.NewRecorder()
		e.ServeHTTP(w, r)
		return w
	}
	const browser = "text/html,application/xhtml+xml"
	clerk := &auth.Identity{UserType: "tenant_user", Permissions: []string{"student:read"}}

	if w := serve("/", browser, clerk); w.Code != http.StatusFound || w.Header().Get("Location") != "/school/" {
		t.Errorf("signed-in browser at /: %d %q, want 302 to /school/", w.Code, w.Header().Get("Location"))
	}
	accountant := &auth.Identity{UserType: "tenant_user", Permissions: []string{"ledger:read"}}
	if w := serve("/", browser, accountant); w.Header().Get("Location") != "/school/" {
		t.Errorf("menu order: sent to %q, want the first group's /school/", w.Header().Get("Location"))
	}
	for name, w := range map[string]*httptest.ResponseRecorder{
		"not signed in": serve("/", browser, nil),
		"an API client": serve("/", "application/json", clerk),
		"another path":  serve("/nothing-here", browser, clerk),
	} {
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want the 404", name, w.Code)
		}
	}
}
