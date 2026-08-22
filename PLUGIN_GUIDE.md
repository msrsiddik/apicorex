# ApiCoreX — Plugin Authoring Guide

An ApiCoreX plugin can be written in **any language** (Go, Node, Python, Rust, Java...). A plugin is an ordinary HTTP server. Core is a reverse proxy: it verifies the bearer device token by introspecting it with Identity, injects tenant context as headers, and streams the request through to your plugin.

**There is no SDK** — following the HTTP contract below is all it takes. Any framework (Gin, Echo, Flask, Express, Spring...) or a stdlib HTTP server will do.

---

## What a plugin must do (four things)

1. Run an HTTP server (on any port).
2. Serve `GET /_apicorex/manifest` → JSON describing the plugin.
3. Serve `GET /_apicorex/health` → `{"status":"ok"}`.
4. Tell Core once at boot: `POST {CORE_URL}/_core/register`.
   (Optional but recommended) `POST {CORE_URL}/_core/heartbeat` every ~15s.

Then write your actual routes (`/invoices`, `/hello`, whatever) as ordinary HTTP endpoints. Core proxies them.

---

## 1. Manifest — `GET /_apicorex/manifest`

After registration Core **pulls** this endpoint to learn about the plugin. Return this:

```json
{
  "name": "billing",
  "version": "1.0.0",
  "description": "Billing & invoices",
  "plugin_type": "internal",
  "routes": [
    { "method": "POST", "path": "/invoices",        "public": false, "summary": "Create invoice", "tags": ["billing"] },
    { "method": "GET",  "path": "/invoices/:id",     "public": false },
    { "method": "POST", "path": "/webhooks/stripe",  "public": true  }
  ],
  "public_paths": ["/webhooks/stripe"],
  "openapi_spec": { },
  "features": [
    { "key": "invoicing", "label": "Invoicing", "group": "Finance" }
  ],
  "migrations": [
    {
      "version": "20260101_001",
      "name": "create invoices",
      "up_sql":   "CREATE TABLE IF NOT EXISTS invoices (id SERIAL PRIMARY KEY, amount INT)",
      "down_sql": "DROP TABLE IF EXISTS invoices"
    }
  ]
}
```

| Field | Meaning |
|-------|---------|
| `name` | Unique plugin name. Used for route ownership and to identify the plugin in the registry. |
| `version` | Plugin version (shown in the docs). |
| `plugin_type` | `"internal"` (1000 req/s) or `"public"` (100 req/s rate limit). |
| `routes[]` | Only these routes are proxied by Core. `:param` is a gin-style segment. |
| `routes[].public` | `true` makes Core **skip** device-token auth on that route. |
| `public_paths[]` | An alternative to `routes[].public: true` — mark paths public by listing them. |
| `openapi_spec` | (optional) A full OpenAPI 3 JSON object, for schema docs in the Scalar UI. Without it routes still work, but the docs show paths only. |
| `migrations[]` | (optional) Tenant-scoped DB migrations. Identity runs them in every tenant schema when the plugin is installed. |
| `permissions[]` | (optional) The permissions this plugin enforces. Identity offers them in the role editor's picker. See [Declaring permissions and roles](#declaring-permissions-and-roles). |
| `roles[]` | (optional) Default role templates, seeded as custom roles in each tenant when the plugin is installed. |
| `features[]` | (optional) This plugin's user-visible modules, so they can be sold in a plan. **Not** permissions — see [Declaring modules (features)](#declaring-modules-features--so-they-can-be-sold-in-a-plan). |

**Important:** Core forwards only the paths declared in `routes[]`. Anything not in the manifest gets a 404 from Core.

---

## 2. Health — `GET /_apicorex/health`

```json
{ "status": "ok" }
```

Core checks this endpoint every 30s. A non-200 or an unreachable plugin is marked "unhealthy" and its circuit breaker opens. It goes live again once it recovers.

---

## 3. Register — `POST {CORE_URL}/_core/register`

Once the plugin has booted and its HTTP server is ready, call Core once:

```json
POST http://localhost:8080/_core/register
Content-Type: application/json

{
  "base_url": "http://billing:8081",
  "api_key":  "your-shared-secret"
}
```

- `base_url` — the URL Core uses to reach your plugin (to pull the manifest and to proxy requests). In Docker/k8s this is the service URL, not the bind address — binding to `:0` still needs a resolvable advertised URL.
- `api_key` — must match Core's `PLUGIN_API_KEY` env var. A mismatch is a 401.

Core responds:
```json
{ "plugin_id": "billing-3f2a9c11", "plugin_token": "eyJ...signed..." }
```

Save **both** the `plugin_id` and the `plugin_token`. The token is a signed credential you must send with every heartbeat and with deregistration, and Core verifies it. Right after registration Core pulls `GET {base_url}/_apicorex/manifest` itself to learn your routes.

> **Allowlist:** if Core has `PLUGIN_ALLOWLIST` set (e.g. `identity,billing`), only plugins with those names may register. Empty (the dev default) allows everything.

> **Important:** your HTTP server must be **fully ready before** you call register, because Core immediately pulls `{base_url}/_apicorex/manifest`. Put registration in a **retry loop** until the server is up. If Core cannot pull the manifest during registration it returns `502`.

---

## 4. Heartbeat (recommended) — `POST {CORE_URL}/_core/heartbeat`

```json
{ "plugin_id": "billing-3f2a9c11", "plugin_token": "eyJ...signed..." }
```

`plugin_token` is the token you got at registration. Send this every ~15s. Skipping it is survivable — the 30s health check keeps you live — but the heartbeat is faster. An invalid token gets a `401`.

**Deregister (graceful shutdown):**
```json
POST {CORE_URL}/_core/deregister
{ "plugin_id": "billing-3f2a9c11", "plugin_token": "eyJ...signed..." }
```

---

## Tenant context — injected headers

After resolving the device token (by calling Identity's `/internal/introspect`), Core injects **trusted headers** into the request. A client cannot spoof them: Core strips every client-supplied `X-ApiCoreX-*` header on every request, then sets the real values from the introspection result.

| Header | Meaning |
|--------|---------|
| `X-ApiCoreX-Tenant-ID` | Tenant ID (e.g. `t_acme`) |
| `X-ApiCoreX-Tenant-Slug` | Tenant slug (e.g. `acme`) |
| `X-ApiCoreX-Schema` | **Your plugin's** Postgres schema for this tenant (e.g. `tenant_acme__billing`) — see below |
| `X-ApiCoreX-User-ID` | Authenticated user ID |
| `X-ApiCoreX-User-Type` | `platform` \| `customer` \| `both` |
| `X-ApiCoreX-Branch-ID` | Branch the device token is scoped to |
| `X-ApiCoreX-Branch-Slug` | Branch slug (e.g. `main`) |
| `X-ApiCoreX-Roles` | Comma-separated roles (e.g. `owner,admin`) |
| `X-ApiCoreX-Permissions` | Comma-separated permissions of the acting **user** |
| `X-ApiCoreX-Features` | Comma-separated modules the **tenant** has, as `plugin:key` |
| `X-ApiCoreX-Request-ID` | Per-request trace ID |

Do not confuse the last two — the whole module system rests on the difference:

- **Permissions** = what *this user* may do. Per user.
- **Features** = whether *this institution* has the module at all. Per tenant —
  identical for every user of that tenant.

On a public route (auth skipped) these headers are absent — check for empty values in your handler.

### `X-ApiCoreX-Schema` is yours alone

Set your `search_path` from this header and never construct a schema name
yourself. It is not the tenant's schema — it is *your* schema for that tenant,
`tenant_<slug>__<your plugin name>`, and every plugin proxied the same request
receives a different one.

Two things follow, and the second is the point:

- **Your tables are yours.** Your migrations run there; nothing else is in it.
- **Another plugin's tables are unreachable.** Not discouraged — unreachable.
  A deployment that has created the per-plugin database roles gives your
  connection no `USAGE` on any other schema, so a query naming one is refused by
  Postgres. There is no arrangement under which you read another plugin's tables;
  if you need its data, that is a contract it has to expose, or a sign the module
  boundary is in the wrong place.

Deriving the name yourself will appear to work in a single-plugin deployment and
break in the next one. Read the header.

---

## Streaming, file upload/download, WebSocket

Core is a streaming reverse proxy, so none of this needs extra work:
- **File upload/download** — bodies stream through; Core does not buffer them (GB-scale is fine).
- **SSE** — send `Content-Type: text/event-stream` and flush, and Core flushes immediately.
- **WebSocket** — Core detects `Connection: Upgrade` and hijack-proxies it (full duplex).

---

## Full example — Python (Flask), NO SDK

```python
from flask import Flask, request, jsonify
import requests, threading, time

app = Flask(__name__)
CORE_URL = "http://localhost:8080"
BASE_URL = "http://localhost:6000"
API_KEY  = "identity-plugin-secret"   # = Core's PLUGIN_API_KEY
plugin_id = None

@app.get("/_apicorex/manifest")
def manifest():
    return jsonify({
        "name": "py-billing",
        "version": "1.0.0",
        "plugin_type": "internal",
        "routes": [
            {"method": "GET",  "path": "/invoices", "public": False},
            {"method": "POST", "path": "/invoices", "public": False},
        ],
        "public_paths": [],
        "migrations": [{
            "version": "20260101_001",
            "name": "create invoices",
            "up_sql":   "CREATE TABLE IF NOT EXISTS invoices (id SERIAL PRIMARY KEY, amount INT)",
            "down_sql": "DROP TABLE IF EXISTS invoices",
        }],
    })

@app.get("/_apicorex/health")
def health():
    return jsonify({"status": "ok"})

@app.get("/invoices")
def list_invoices():
    tenant = request.headers.get("X-ApiCoreX-Tenant-ID")
    user   = request.headers.get("X-ApiCoreX-User-ID")
    return jsonify({"tenant": tenant, "user": user, "invoices": []})

@app.post("/invoices")
def create_invoice():
    return jsonify({"created": True, "tenant": request.headers.get("X-ApiCoreX-Tenant-ID")})

def register():
    global plugin_id
    # retry until BOTH our server is up (Core can pull manifest) and Core is reachable
    for _ in range(15):
        try:
            r = requests.post(f"{CORE_URL}/_core/register",
                              json={"base_url": BASE_URL, "api_key": API_KEY})
            if r.status_code == 200:
                plugin_id = r.json()["plugin_id"]
                print("registered:", plugin_id)
                break
        except Exception:
            pass
        time.sleep(1)
    while plugin_id:
        time.sleep(15)
        try:
            requests.post(f"{CORE_URL}/_core/heartbeat", json={"plugin_id": plugin_id})
        except Exception:
            pass

threading.Thread(target=register, daemon=True).start()
app.run(port=6000)
```

`python app.py` registers with Core; `GET http://localhost:8080/invoices` (with a valid device token) is then proxied to the plugin with the tenant headers.

---

## Full example — Java (JDK built-in HttpServer), NO SDK

No framework (Spring/Quarkus) is needed — the JDK's `com.sun.net.httpserver.HttpServer` is enough. The JSON is written out as strings by hand, to keep this dependency-free.

```java
// Plugin.java — run: java Plugin.java   (JDK 11+, single-file source)
import com.sun.net.httpserver.*;
import java.io.*;
import java.net.*;
import java.net.http.*;
import java.nio.charset.StandardCharsets;

public class Plugin {
    static final String CORE_URL = "http://localhost:8080";
    static final String BASE_URL = "http://localhost:7000";
    static final String API_KEY  = "identity-plugin-secret"; // = Core's PLUGIN_API_KEY

    static final String MANIFEST = """
        {
          "name": "java-billing",
          "version": "1.0.0",
          "plugin_type": "internal",
          "routes": [
            {"method":"GET","path":"/invoices","public":false}
          ],
          "public_paths": [],
          "migrations": [
            {"version":"20260101_001","name":"create invoices",
             "up_sql":"CREATE TABLE IF NOT EXISTS invoices (id SERIAL PRIMARY KEY, amount INT)",
             "down_sql":"DROP TABLE IF EXISTS invoices"}
          ]
        }""";

    public static void main(String[] args) throws IOException {
        HttpServer server = HttpServer.create(new InetSocketAddress(7000), 0);

        server.createContext("/_apicorex/manifest", ex -> respond(ex, 200, MANIFEST));
        server.createContext("/_apicorex/health",   ex -> respond(ex, 200, "{\\"status\\":\\"ok\\"}"));

        // business route — read tenant context from injected headers
        server.createContext("/invoices", ex -> {
            String tenant = ex.getRequestHeaders().getFirst("X-ApiCoreX-Tenant-ID");
            String user   = ex.getRequestHeaders().getFirst("X-ApiCoreX-User-ID");
            respond(ex, 200, "{\\"tenant\\":\\"" + tenant + "\\",\\"user\\":\\"" + user + "\\",\\"invoices\\":[]}");
        });

        server.setExecutor(null);
        server.start();
        System.out.println("java-billing listening on :7000");

        new Thread(Plugin::register).start();
    }

    static void respond(HttpExchange ex, int code, String body) throws IOException {
        byte[] b = body.getBytes(StandardCharsets.UTF_8);
        ex.getResponseHeaders().set("Content-Type", "application/json");
        ex.sendResponseHeaders(code, b.length);
        try (OutputStream os = ex.getResponseBody()) { os.write(b); }
    }

    // retry until our server is up AND Core is reachable
    static void register() {
        HttpClient client = HttpClient.newHttpClient();
        String body = "{\\"base_url\\":\\"" + BASE_URL + "\\",\\"api_key\\":\\"" + API_KEY + "\\"}";
        String pluginId = null;
        for (int i = 0; i < 15 && pluginId == null; i++) {
            try {
                HttpResponse<String> r = client.send(
                    HttpRequest.newBuilder(URI.create(CORE_URL + "/_core/register"))
                        .header("Content-Type", "application/json")
                        .POST(HttpRequest.BodyPublishers.ofString(body)).build(),
                    HttpResponse.BodyHandlers.ofString());
                if (r.statusCode() == 200) {
                    pluginId = r.body(); // {"plugin_id":"..."}
                    System.out.println("registered: " + pluginId);
                }
            } catch (Exception ignored) {}
            if (pluginId == null) try { Thread.sleep(1000); } catch (InterruptedException ignored) {}
        }
    }
}
```

`java Plugin.java` registers with Core; `GET http://localhost:8080/invoices` (with a valid device token) is proxied to the Java plugin with the tenant headers.

> In production with Spring Boot or Quarkus this gets easier — use the framework's JSON serialization and HTTP client to serve the manifest and register. The contract is the same: `/_apicorex/manifest`, `/_apicorex/health`, `POST /_core/register`.

---

## Full example — Go (pure Gin, NO SDK)

There is no ApiCoreX SDK — just Gin and the stdlib. Same contract: manifest, health, register.

```go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	coreURL = "http://localhost:8080"
	baseURL = "http://localhost:8081"
	apiKey  = "identity-plugin-secret" // = Core's PLUGIN_API_KEY
)

var manifest = gin.H{
	"name": "go-billing", "version": "1.0.0", "plugin_type": "internal",
	"routes": []gin.H{
		{"method": "GET", "path": "/invoices", "public": false},
	},
	"public_paths": []string{},
	"migrations": []gin.H{{
		"version": "20260101_001", "name": "create invoices",
		"up_sql":   "CREATE TABLE IF NOT EXISTS invoices (id SERIAL PRIMARY KEY, amount INT)",
		"down_sql": "DROP TABLE IF EXISTS invoices",
	}},
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	r := gin.New()
	r.GET("/_apicorex/manifest", func(c *gin.Context) { c.JSON(200, manifest) })
	r.GET("/_apicorex/health", func(c *gin.Context) { c.JSON(200, gin.H{"status": "ok"}) })

	// business route — read tenant context from injected headers
	r.GET("/invoices", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"tenant":   c.GetHeader("X-ApiCoreX-Tenant-ID"),
			"user":     c.GetHeader("X-ApiCoreX-User-ID"),
			"invoices": []any{},
		})
	})

	go register()
	srv := &http.Server{Addr: ":8081", Handler: r}
	go srv.ListenAndServe()
	<-ctx.Done()
	sc, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(sc)
}

func register() {
	body, _ := json.Marshal(gin.H{"base_url": baseURL, "api_key": apiKey})
	for i := 0; i < 15; i++ {
		resp, err := http.Post(coreURL+"/_core/register", "application/json", bytes.NewReader(body))
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			return
		}
		time.Sleep(time.Second)
	}
}
```

`go run main.go` registers with Core; `GET http://localhost:8080/invoices` (with a valid device token) is proxied with the tenant headers. Streaming, uploads and WebSockets work through plain Gin.

> **OpenAPI docs:** for a rich Scalar UI schema, put an OpenAPI 3 JSON document in the manifest's `openapi_spec` field. In Gin it can be generated from the routes with [oaswrap](https://github.com/oaswrap/spec) — see `apicorex-identity/internal/plugin/plugin.go`, which does exactly that. Optional: without it Scalar shows the route list only.

---

## Plugin install + migrations (multi-tenant)

Registering makes a plugin's routes live. Creating its tenant-scoped DB tables needs an **install**:

```
POST /plugins/install   (Identity plugin route, auth required)
{ "tenant_id": "t_acme", "plugin_name": "billing" }
```

Identity pulls your manifest from Core (`GET /_core/plugins/billing/manifest`), takes `migrations[]` and runs them in **your** schema for that tenant — `tenant_acme__billing`, created on first install. When a new tenant registers, the migrations of every installed plugin run automatically, each into its own schema.

Write your DDL unqualified (`CREATE TABLE students (...)`, not
`CREATE TABLE tenant_acme.students (...)`). Identity sets `search_path` for the
transaction; a schema name written into your SQL targets the wrong one.

### `tenant_schema` — do not set it

The manifest field `tenant_schema` selects which schema you are given. Omit it,
and you get your own, which is what every plugin wants. The only other value is
`"shared"`, which asks for the tenant's base schema; it exists for Identity,
which owns that schema and the tenant record naming it. A domain plugin setting
it is asking for exactly what the separation prevents.

**Uninstall** — controlled by the `drop_data` flag:
```
POST /plugins/uninstall   (auth required)
{ "tenant_id": "t_acme", "plugin_name": "billing", "drop_data": false }
```
- `drop_data: false` → removes the install record only; **the tenant's tables and data stay** (reinstalling brings the data back). For a temporary disable or a re-subscribe.
- `drop_data: true` → runs the plugin's `down_sql` (DROP TABLE) and **deletes the data permanently**. For tenant offboarding or a GDPR delete.

---

## Declaring permissions and roles

Identity's own permission vocabulary covers only what Identity governs — users,
branches, plugin installs, billing, tenant settings. The permissions of your
domain (`student:write`, `patient:read`, `sale:void`) belong to **your plugin**,
so you declare them in the manifest.

```json
{
  "name": "schoolyze",
  "permissions": [
    { "permission": "student:read",  "description": "View students", "resource_group": "Students" },
    { "permission": "student:write", "description": "Add or edit students", "resource_group": "Students" },
    { "permission": "fee:collect",   "description": "Collect fee payments", "resource_group": "Fees" }
  ],
  "roles": [
    { "slug": "teacher", "name": "Teacher",
      "permissions": ["student:read", "attendance:write"] },
    { "slug": "accountant", "name": "Accountant",
      "permissions": ["fee:*", "student:read"] }
  ]
}
```

How it works:

- Identity pulls your manifest from Core (after registration, and then
  periodically) and stores `permissions[]`. `GET /permissions` then merges
  Identity's built-in vocabulary with every registered plugin's declarations, so
  your permissions appear in the role editor's picker.
- `roles[]` are templates. In each tenant the plugin is installed for, they are
  seeded as **ordinary custom roles** the tenant can edit or delete. A slug the
  tenant already has is skipped: if an institution narrowed one of its own roles,
  a reinstall or a redeploy will not undo that.

Why the manifest rather than Identity's code: one Identity binary serves every
product. A school deployment offers school permissions, a clinic deployment
clinic ones — no build tags, no per-deployment config to keep in sync. A
permission lives next to the code that enforces it.

**Keep in mind:**

- Wildcards are not allowed in `permissions[]` (`student:*`) — the picker holds
  concrete permissions. Wildcards *are* allowed in `roles[].permissions`.
- Declaring is **not** enforcing. To enforce, set the route's `permission` field
  (Core returns 403 at the gateway) and re-check in the handler with
  `HasPermission`.
- Removing a permission from the manifest removes it from the picker, but does
  not break the access of anyone who already holds it, nor a tenant's seeded
  roles.

Go plugins carrying the `internal/plugin` runtime have helpers:

```go
p.DeclarePermissions(
    plugin.DeclaredPermission{Permission: "student:read", Description: "View students"},
    plugin.DeclaredPermission{Permission: "fee:collect", ResourceGroup: "Fees"},
)
p.DeclareRoles(plugin.DeclaredRole{
    Slug: "teacher", Name: "Teacher",
    Permissions: []string{"student:read", "attendance:write"},
})
```

In any other language, just add the two fields to the manifest JSON.

---

## Declaring modules (features) — so they can be sold in a plan

A permission answers *may this user act*. A feature answers *does this
institution have the module at all*. Two different things, and keeping them
apart is **mandatory**:

| | Permission | Feature (module) |
|---|---|---|
| Asks | may this **user** act? | does this **institution** have it? |
| Scope | per user | per tenant |
| Decided in | the tenant's own role editor | the Identity console (platform admin) |
| Decided by | the institution's own owner | you (sales / plan) |
| Enforced by | Core gateway **and** plugin | **plugin only** |
| Absent means | "ask your administrator" | "not in your plan" |

What you lose by merging them: "this school never bought the fees module" and
"this accountant may not collect money" are completely different situations,
fixed by different people, and need different words on screen.

### 1. Declare in the manifest

```json
{
  "name": "billing",
  "features": [
    { "key": "invoicing",  "label": "Invoicing",  "group": "Finance" },
    { "key": "reports",    "label": "Reports",    "group": "Finance" },
    { "key": "sms_alerts", "label": "SMS alerts", "group": "Comms",
      "default_enabled": false }
  ]
}
```

| Field | Meaning |
|---|---|
| `key` | Your plugin's own key. Another plugin may use the same one — Identity qualifies every key as `plugin:key` |
| `label` | What the console shows. A human sits down to build a price list; `sms_alerts` is not something to put in front of them |
| `group` | Display grouping in the console (`Finance`, `Academics`) |
| `default_enabled` | What applies **while no plan lists this feature**. **Omitted = true** |

`default_enabled` defaulting to true is deliberate: declaring features must not
change anything for institutions already running. Set it `false` only for
modules that cost real money per use (SMS, payment gateway fees).

### 2. Enforce on your routes

**Core does not enforce features** — unlike permissions. Enforcing them there
would mean telling Core which routes belong to which module, and that is exactly
the domain knowledge this architecture keeps out of Core. So this part is
**yours**.

**Go plugins that carry the runtime copy:** copy `internal/plugin/feature.go`
from schoolyze-server — it has `DeclaredFeature`, `DeclareFeatures`,
`HasFeature` and `RequireFeature`. But **that file alone is not enough**; two
small changes in `plugin.go` go with it:

```go
// 1) a field on the Plugin struct
type Plugin struct {
    // ...
    declaredFeatures []DeclaredFeature
}

// 2) emit it in buildManifest() — an empty slice, never nil, because Identity
//    decodes it as an array
declaredFeatures := p.declaredFeatures
if declaredFeatures == nil {
    declaredFeatures = []DeclaredFeature{}
}
return map[string]any{
    // ...
    "features": declaredFeatures,
}
```

Then use it:

```go
p.DeclareFeatures(
    plugin.DeclaredFeature{Key: "invoicing", Label: "Invoicing", Group: "Finance"},
)

// inside a handler
if !p.HasFeature(c, "invoicing") { /* 404 */ }

// or as middleware, so one module's routes are guarded in one place
inv := r.Group("/invoices", p.RequireFeature("invoicing"))
```

In any other language, read the header and qualify with your own plugin name:

```python
def has_feature(req, key):
    feats = req.headers.get("X-ApiCoreX-Features", "").split(",")
    return f"{PLUGIN_NAME}:{key}" in feats   # without qualifying, another
                                             # plugin's module reads as yours
```

**Hiding a menu entry is not enforcement.** Removing an item from the UI stops
nobody from typing the URL. Do both: filter the menu (a courtesy) and guard the
routes (the actual enforcement).

### 3. Status codes

- **JSON API → `404`.** A module an institution does not have should look like it
  does not exist. A `403` tells anyone probing exactly which modules sit behind
  the door.
- **Your own panel UI → a page** saying the module is not part of their plan and
  who to contact. These are the institution's own staff; told "not found" they
  will report a broken link to support.

### When a feature actually becomes gated — misread this and you will get it wrong

Identity resolves in this order:

```
1. does the tenant have an override?      → use it
2. otherwise, does ANY plan list this feature?
      yes → is it in this tenant's own live plan?
3. otherwise                              → the plugin's declared default
```

Step 2 is the important one: **a feature is not gated at all until some plan
lists it** — until then the declared default applies. Which means:

- Declaring features and deploying today **changes nothing**. Every institution
  sees exactly what it saw before.
- A deployment with no billing (a school on its own server) keeps working.
- Gating begins when you add the module to a plan in the console.

Without that condition, the day you turned this on every module would switch off
for every tenant — because no plan lists anything yet.

### Who configures it

**Platform admins only**, from the Identity console:

- `Plans → [plan] → Modules` — what the plan includes (the price list)
- `Tenants → [tenant] → Modules` — what this institution actually sees **and
  why** (plan / override / default). Set an exception here when you need one (a
  pilot, a concession), with a reason.

An institution cannot edit its own entitlements — if it could, the price list
would mean nothing.

A toggle takes about **30 seconds** to take effect: Core caches the introspection
result. That is the cache, not a bug.

### Turning a feature off ≠ deleting data

Switching a module off hides the screens and touches nothing in the database.
Switch it back on and everything is where it was. Preserve this property —
turning a module off is a reversible commercial decision, not a destructive
operation.

### Checklist for a new plugin

- [ ] Add `features[]` to the manifest (`key`, `label`, `group`)
- [ ] `default_enabled: false` for metered modules (SMS, gateway fees)
- [ ] Leave `default_enabled` off everything else (= true), so running
      deployments do not break
- [ ] Go: copy `internal/plugin/feature.go` from schoolyze-server **and** add the
      `declaredFeatures` field plus the `"features"` manifest key in `plugin.go`
- [ ] Qualify keys with your own plugin name — otherwise you read another
      plugin's modules as your own
- [ ] Guard every module's routes (the menu filter is separate, and is not
      enforcement)
- [ ] Do **not** declare the product's **core** — the parts without which the
      product does not work (student list, dashboard, settings). Making those
      sellable only creates a way to sell something nobody can use
- [ ] Keep the keys in **one place** (constants); a typo otherwise loses a screen

### Two silent mistakes — catch them with tests

Neither of these shows up anywhere at runtime:

1. **A guard naming a key nobody declared** → that screen is closed for **every
   institution, forever**. Nothing is logged.
2. **A declared key nothing enforces** → the console offers it as a sellable
   module, and selling it does nothing.

So keep the keys in one place, and write a test that holds the "declared" and
"enforced" lists against each other. Reference:
`schoolyze-server/cmd/schoolyze/features_test.go`.

### Reference implementation

| What | Where |
|---|---|
| Runtime helper (copy this) | `schoolyze-server/internal/plugin/feature.go` |
| Declaration | `schoolyze-server/cmd/schoolyze/features.go` |
| Panel guard + menu filter | `schoolyze-server/internal/web/features.go` |
| Identity's resolution logic | `apicorex-identity/internal/features/` |
| Identity's design doc | `apicorex-identity/docs/feature-packaging-design.md` |

---

## Checklist

- [ ] HTTP server running
- [ ] `GET /_apicorex/manifest` → valid JSON (name, routes)
- [ ] `GET /_apicorex/health` → `{"status":"ok"}`
- [ ] `POST /_core/register` at boot (with the right `api_key`)
- [ ] (optional) heartbeat loop
- [ ] Routes match the manifest's `routes[]`
- [ ] Tenant context read from the `X-ApiCoreX-*` headers
- [ ] (optional) `openapi_spec` → full docs in the Scalar UI
- [ ] (optional) declare `permissions[]` + `roles[]` → they appear in the role editor
- [ ] (optional) declare `features[]` → sellable in a plan; **do not forget the
      route guards**, or declaring them means nothing
```
