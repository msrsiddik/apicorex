# ApiCoreX

A **stateless, multi-tenant API gateway** with a language-agnostic HTTP plugin
system. Core handles authentication, routing, streaming, and resilience; your
business logic lives in plugins written in **any language**.

- **Stateless Core** — no database; verifies JWTs, routes, and proxies. Scales horizontally.
- **Any-language plugins** — a plugin is just an HTTP server. No SDK required (Go, Python, Java, Node…). See [PLUGIN_GUIDE.md](./PLUGIN_GUIDE.md).
- **Streaming first** — file upload/download, SSE, and WebSocket all work (HTTP reverse proxy, not gRPC).
- **Multi-tenant** — JWT carries tenant context; Core injects it as trusted headers.
- **Production-ready** — Prometheus metrics, OpenTelemetry tracing, structured logs, rate limiting, circuit breaker, bulkhead, config-driven limits, plugin allowlist + signed tokens.

---

## Architecture

```
                       ┌──────────────────────────────────────────┐
  CONTROL PLANE        │              CORE (:8080)                 │   DATA PLANE
  ────────────         │                                          │   ──────────
  POST /_core/register │  per request:                            │
  POST /_core/heartbeat│   strip spoofed headers                  │
       (Core pulls      │   verify JWT (+ Redis denylist)          │
        the manifest)   │   inject X-ApiCoreX-* tenant headers     │
                       │   firewall → ratelimit → bulkhead → CB    │
  client ─HTTP/WS─────►│   httputil.ReverseProxy (streaming) ──────┼──► plugin (any language)
                       │   or WebSocket hijack proxy               │
                       │  /health /plugins /docs /metrics          │
                       └──────────────────────────────────────────┘
```

- **Control plane** — plugins register over HTTP; Core pulls each plugin's manifest from `GET {base_url}/_apicorex/manifest`.
- **Data plane** — Core verifies the JWT, injects tenant context as `X-ApiCoreX-*` headers, and streams the request to the plugin.
- **Identity** — authentication, tenant registration, and JWT issuing live in a separate plugin: [apicorex-identity](https://github.com/msrsiddik/apicorex-identity). Core only *verifies* tokens.

---

## Official plugins

Core ships no business logic — these standalone plugins provide it. Each is its
own repo with its own database, migrations, and lifecycle:

| Plugin | Repo | What it does |
|--------|------|--------------|
| **Identity** | [apicorex-identity](https://github.com/msrsiddik/apicorex-identity) | Authentication, multi-tenant registration, JWT issuing, per-tenant plugin install/migrations |
| **Sync** | [apicorex-sync](https://github.com/msrsiddik/apicorex-sync) | Offline-first data sync (push/pull, last-write-wins, tombstones) for any app |

Want your own? A plugin is just an HTTP server in any language — see
[PLUGIN_GUIDE.md](./PLUGIN_GUIDE.md).

---

## Quickstart

Requires Go 1.25+. (Identity additionally needs PostgreSQL.) Clone Core and the
plugins you want as siblings:

```bash
git clone https://github.com/msrsiddik/apicorex.git
git clone https://github.com/msrsiddik/apicorex-identity.git
git clone https://github.com/msrsiddik/apicorex-sync.git
```

```bash
# 1. Start Core
cd apicorex
JWT_SECRET=dev-secret PLUGIN_API_KEY=dev-key go run ./cmd/apicorex
# Core listens on :8080

# 2. Start the Identity plugin (separate repo; needs DATABASE_URL)
cd ../apicorex-identity
DATABASE_URL=postgres://... JWT_SECRET=dev-secret PLUGIN_API_KEY=dev-key \
  CORE_URL=http://localhost:8080 PLUGIN_BASE_URL=http://localhost:50051 \
  go run ./cmd/identity

# 3. Register a tenant, log in, call an authenticated route
curl -XPOST localhost:8080/auth/register -H 'Content-Type: application/json' \
  -d '{"slug":"acme","name":"Acme","plan":"starter","email":"o@acme.com","password":"secret123"}'

TOK=$(curl -s -XPOST localhost:8080/auth/login -H 'Content-Type: application/json' \
  -d '{"slug":"acme","email":"o@acme.com","password":"secret123"}' | jq -r .access_token)

curl localhost:8080/me -H "Authorization: Bearer $TOK"
```

Open **http://localhost:8080/docs** for the Scalar UI (Core + all plugin routes).

> For hot reload during development, run `air` in each repo (config in `.air.toml`).

### With Docker

This repo's `docker-compose.yml` brings up Core + Postgres + Redis. The plugin
services (`identity`, `sync`) are commented out by default — uncomment them to
run the **full stack from a single file** (it builds them from sibling repos, so
clone `apicorex-identity` and `apicorex-sync` next to this repo):

```
GolandProjects/
├── apicorex/            ← run `docker compose up --build` here
├── apicorex-identity/
└── apicorex-sync/
```

```bash
docker compose up --build         # Core + Postgres + Redis
# then uncomment identity/sync in docker-compose.yml for the full stack
```

Each plugin repo also ships its own standalone `docker-compose.yml` if you'd
rather run them separately.

---

## Writing a plugin

A plugin is an HTTP server that serves a manifest + health endpoint and registers
with Core. No SDK, any language. Full guide with Go / Python / Java examples:
**[PLUGIN_GUIDE.md](./PLUGIN_GUIDE.md)**.

Minimal contract:
- `GET /_apicorex/manifest` → JSON describing routes, public paths, migrations, OpenAPI spec
- `GET /_apicorex/health` → `{"status":"ok"}`
- `POST {CORE_URL}/_core/register` on boot (with retry); then heartbeat

Inside a handler, read the context Core injected after verifying the JWT:
`X-ApiCoreX-Tenant-ID`, `X-ApiCoreX-Tenant-Slug`, `X-ApiCoreX-Schema`,
`X-ApiCoreX-Branch-ID`, `X-ApiCoreX-Branch-Slug`, `X-ApiCoreX-User-ID`,
`X-ApiCoreX-User-Type`, `X-ApiCoreX-Roles`, `X-ApiCoreX-Permissions`.

**Authorization.** A manifest route may declare a `permission`
(`"resource:action"`, with `*` wildcards). Before proxying, Core checks the
caller's `permissions` claim against it (wildcard-aware) and returns `403` if it
is missing — so a plugin gets authorization at the gateway for free, and can
re-check the header for defense-in-depth.

---

## Configuration

All via environment variables (secrets never hardcoded):

| Var | Default | Purpose |
|-----|---------|---------|
| `HTTP_PORT` | `:8080` | HTTP listen address |
| `JWT_SECRET` | — | HS256 secret to verify access tokens (shared with Identity) |
| `PLUGIN_API_KEY` | — | Shared key plugins present on register |
| `PLUGIN_ALLOWLIST` | empty | Comma-separated plugin names allowed to register (empty = allow any, dev) |
| `REDIS_URL` | empty | Enables logout denylist (revoke access tokens immediately) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | empty | Enables OpenTelemetry tracing (e.g. Jaeger) |
| `CONFIG_FILE` | empty | YAML for per-plugin rate/limit overrides — see [config.example.yaml](./config.example.yaml) |

Per-plugin limits (rate, bulkhead, circuit breaker, timeouts) can also be tuned
globally via `RATE_PER_SEC`, `BULKHEAD_MAX`, `CB_THRESHOLD`, etc.

---

## Endpoints

| Path | Auth | Description |
|------|------|-------------|
| `GET /health` | no | Liveness |
| `GET /plugins` | no | Registered plugins |
| `GET /docs` | no | Scalar UI |
| `GET /docs/openapi.json` | no | Merged OpenAPI (Core + plugins) |
| `GET /metrics` | no | Prometheus metrics |
| `* /_core/*` | api key | Control plane (register/heartbeat/deregister) |
| everything else | JWT* | Proxied to the owning plugin (*unless the route is public) |

---

## Project structure

```
cmd/apicorex/      entrypoint
internal/
  auth/            JWT verify + Redis logout denylist
  config/          config-driven protection limits
  controlplane/    HTTP register/heartbeat + signed plugin tokens
  dispatcher/      reverse proxy + WebSocket + tracing + metrics (data plane)
  manifest/        plugin manifest types
  middleware/      auth + tenant-header injection (anti-spoofing)
  openapi/         OpenAPI spec merge for Scalar UI
  protection/      firewall, rate limit, bulkhead, circuit breaker, health, metrics, logs
  registry/        in-memory plugin store
  tracing/         OpenTelemetry setup
server/            HTTP server wiring
```

Browse the API docs with `go doc ./internal/<pkg>`.
```
