# ApiCoreX — Plugin Authoring Guide

Eta diye **je kono language** (Go, Node, Python, Rust, Java...) e ApiCoreX plugin banano jay। Plugin holo ekta normal HTTP server। Core ekta reverse proxy — bearer device token take Identity-r kache introspect kore verify kore, tenant context header e inject kore, request stream kore tomar plugin-e forward kore.

**Kono SDK nai** — niche ja ache shudhu sei HTTP contract follow korlei chole. Je kono framework (Gin, Echo, Flask, Express, Spring...) ba stdlib HTTP server cholbe.

---

## Plugin ki ki korte hobe (4 ta jinish)

1. Ekta HTTP server chalao (je kono port-e).
2. `GET /_apicorex/manifest` serve koro → plugin describe kora JSON.
3. `GET /_apicorex/health` serve koro → `{"status":"ok"}`.
4. Boot-e Core-ke ekbar bolo: `POST {CORE_URL}/_core/register`.
   (optional kintu recommended) proti ~15s e `POST {CORE_URL}/_core/heartbeat`.

Tarpor tomar actual routes (`/invoices`, `/hello`, jai houk) normal HTTP endpoint hisebe likho. Core sei route gulo proxy korbe.

---

## 1. Manifest — `GET /_apicorex/manifest`

Core registration er por ei endpoint **pull** kore plugin ke jane. Eta return koro:

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

| Field | Mane |
|-------|------|
| `name` | Unique plugin name. Route ownership ar registry-e plugin identify korte use hoy. |
| `version` | Plugin version (docs e dekhabe). |
| `plugin_type` | `"internal"` (1000 req/s) ba `"public"` (100 req/s rate limit)। |
| `routes[]` | Core ei route gulo-i proxy korbe. `:param` gin-style segment. |
| `routes[].public` | `true` hole oi route-e Core device-token auth **skip** korbe. |
| `public_paths[]` | `routes[].public: true` er bikolpo — path list diye public mark kora. |
| `openapi_spec` | (optional) Full OpenAPI 3 JSON object. Scalar UI te schema docs dekhanor jonno. Na dile route kaj korbe kintu docs e shudhu path dekhabe. |
| `migrations[]` | (optional) Tenant-scoped DB migration. Identity plugin install er somoy protita tenant schema-e run kore. |

**Important:** `routes[]`-e ja declare korbe, Core shudhu sei path gulo-i forward korbe। Manifest-e na thakle Core 404 dibe।

---

## 2. Health — `GET /_apicorex/health`

```json
{ "status": "ok" }
```

Core proti 30s e ei endpoint check kore। Non-200 ba unreachable hole plugin "unhealthy" mark hoy ar circuit breaker open hoy। Recover korle abar live.

---

## 3. Register — `POST {CORE_URL}/_core/register`

Plugin boot howar por (HTTP server ready holey) Core-ke ekbar call koro:

```json
POST http://localhost:8080/_core/register
Content-Type: application/json

{
  "base_url": "http://billing:8081",
  "api_key":  "your-shared-secret"
}
```

- `base_url` — Core ei URL diye tomar plugin-e pouchabe (manifest pull + request proxy)। Docker/k8s e eta service URL (bind address na — `:0` bind korle o advertised URL alada হতে পারে).
- `api_key` — `PLUGIN_API_KEY` env var er sathe match korte hobe (Core-e set kora). Match na hole 401।

Core respond kore:
```json
{ "plugin_id": "billing-3f2a9c11", "plugin_token": "eyJ...signed..." }
```

Ei `plugin_id` **ar `plugin_token`** duটোই save koro। `plugin_token` ekta signed credential — heartbeat ar deregister-e eta pathate hobe (Core verify kore)। Register er por Core nije `GET {base_url}/_apicorex/manifest` pull kore route gulo nibe.

> **Allowlist:** Core-e `PLUGIN_ALLOWLIST` set thakle (e.g. `identity,billing`), shudhu oi name-er plugin register korte parbe। Khali thakle (dev) sob allowed।

> **Important:** Register call korar age tomar HTTP server **fully ready** thakte hobe — karon Core sathe sathe `{base_url}/_apicorex/manifest` pull kore। Tai register-ke ekta **retry loop**-e rakho (server up howa porjonto)। Core register-er somoy manifest pull korte na parle `502` dibe।

---

## 4. Heartbeat (recommended) — `POST {CORE_URL}/_core/heartbeat`

```json
{ "plugin_id": "billing-3f2a9c11", "plugin_token": "eyJ...signed..." }
```

`plugin_token` register-e paওয়া token। ~15s interval-e pathao. Na pathaleo health-check (30s) tomake live rakhbe, kintu heartbeat faster. Token invalid hole Core `401` dibe.

**Deregister (graceful shutdown):**
```json
POST {CORE_URL}/_core/deregister
{ "plugin_id": "billing-3f2a9c11", "plugin_token": "eyJ...signed..." }
```

---

## Tenant context — injected headers

Core device token resolve korar por (Identity-r `/internal/introspect` call kore) **trusted headers** inject kore tomar plugin-e। Client ei header spoof korte parbe na — Core protita request-e client-supplied `X-ApiCoreX-*` strip kore, tarpor introspection result theke real value boshay.

| Header | Mane |
|--------|------|
| `X-ApiCoreX-Tenant-ID` | Tenant ID (e.g. `t_acme`) |
| `X-ApiCoreX-Tenant-Slug` | Tenant slug (e.g. `acme`) |
| `X-ApiCoreX-Schema` | Tenant Postgres schema (e.g. `tenant_acme`) |
| `X-ApiCoreX-User-ID` | Authenticated user ID |
| `X-ApiCoreX-User-Type` | `platform` \| `customer` \| `both` |
| `X-ApiCoreX-Roles` | Comma-separated roles (e.g. `owner,admin`) |
| `X-ApiCoreX-Request-ID` | Per-request trace ID |

Public route-e (auth skip) ei header gulo thakbe na — tomar handler-e empty check koro.

---

## Streaming, file upload/download, WebSocket

Core streaming reverse proxy — kichu extra korte hobe na:
- **File upload/download** — body stream hoy, Core buffer kore na (GB-scale o cholbe).
- **SSE** — `Content-Type: text/event-stream` + flush korle Core immediately flush kore.
- **WebSocket** — `Connection: Upgrade` detect kore Core hijack-proxy kore (full-duplex)।

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

`python app.py` → Core-e register hoye jabe, `GET http://localhost:8080/invoices` (with a valid device token) → proxied to plugin with tenant headers.

---

## Full example — Java (JDK built-in HttpServer), NO SDK

Kono framework (Spring/Quarkus) lagbe na — JDK-er `com.sun.net.httpserver.HttpServer` diyei hobe. JSON manually string-e likha hoyeche (zero dependency rakhte)।

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

`java Plugin.java` → Core-e register, `GET http://localhost:8080/invoices` (valid device token soho) → proxied to Java plugin with tenant headers.

> Production-e Spring Boot / Quarkus use korle aro shoja — sei framework-er JSON serialization + HTTP client diye manifest serve + register koro. Contract eki: `/_apicorex/manifest`, `/_apicorex/health`, `POST /_core/register`।

---

## Full example — Go (pure Gin, NO SDK)

Kono ApiCoreX SDK nai — shudhu Gin + stdlib. Contract eki: manifest, health, register.

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

`go run main.go` → Core-e register, `GET http://localhost:8080/invoices` (valid device token soho) → proxied with tenant headers. Streaming/upload/WebSocket native Gin diyei chole.

> **OpenAPI docs:** Rich Scalar UI schema chaile manifest-er `openapi_spec` field-e ekta OpenAPI 3 JSON dao. Gin-e [oaswrap](https://github.com/oaswrap/spec) library use kore route theke auto-generate kora jay (reference: `apicorex-identity/internal/plugin/plugin.go` — Identity exactly eta kore)। Eta optional — na dile route list-i Scalar-e dekhabe.

---

## Plugin install + migrations (multi-tenant)

Plugin register howa = route live। Kintu tenant-scoped DB table create korte hole **install** korte hobe:

```
POST /plugins/install   (Identity plugin route, auth required)
{ "tenant_id": "t_acme", "plugin_name": "billing" }
```

Identity Core theke tomar manifest pull kore (`GET /_core/plugins/billing/manifest`), `migrations[]` niye `tenant_acme` schema-e run kore। Notun tenant register holeo installed plugin-er migration automatic chole।

**Uninstall** — `drop_data` flag diye control:
```
POST /plugins/uninstall   (auth required)
{ "tenant_id": "t_acme", "plugin_name": "billing", "drop_data": false }
```
- `drop_data: false` → shudhu install record muche, **tenant-er table/data thake** (abar install korle data ফেরত)। Temporary disable / re-subscribe-er jonno.
- `drop_data: true` → plugin-er `down_sql` chaলায় (DROP TABLE), **data permanently muche**। Tenant offboarding / GDPR delete-er jonno.

---

## Checklist

- [ ] HTTP server chalu
- [ ] `GET /_apicorex/manifest` → valid JSON (name, routes)
- [ ] `GET /_apicorex/health` → `{"status":"ok"}`
- [ ] Boot-e `POST /_core/register` (sahi `api_key` soho)
- [ ] (optional) heartbeat loop
- [ ] Routes manifest-er `routes[]`-er sathe match kore
- [ ] Tenant context `X-ApiCoreX-*` header theke poro
- [ ] (optional) `openapi_spec` dao → Scalar UI te full docs
```
