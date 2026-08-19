# ApiCoreX

A **stateless, multi-tenant API gateway** with a language-agnostic HTTP plugin
system. Core handles authentication, routing, streaming, and resilience; your
business logic lives in plugins written in **any language**.

- **Stateless Core** — no database; resolves device tokens (via Identity), routes, and proxies. Scales horizontally.
- **Any-language plugins** — a plugin is just an HTTP server. No SDK required (Go, Python, Java, Node…). See [PLUGIN_GUIDE.md](./PLUGIN_GUIDE.md).
- **Streaming first** — file upload/download, SSE, and WebSocket all work (HTTP reverse proxy, not gRPC).
- **Multi-tenant** — an opaque bearer device token is introspected against Identity per request; Core injects the resolved tenant/branch/user context as trusted headers.
- **Production-ready** — Prometheus metrics, OpenTelemetry tracing, structured logs, rate limiting, circuit breaker, bulkhead, config-driven limits, plugin allowlist + signed tokens.

---

## Architecture

```
                       ┌──────────────────────────────────────────┐
  CONTROL PLANE        │              CORE (:8080)                 │   DATA PLANE
  ────────────         │                                          │   ──────────
  POST /_core/register │  per request:                            │
  POST /_core/heartbeat│   strip spoofed headers                  │
       (Core pulls      │   resolve device token (introspect via   │
        the manifest)   │   Identity) → inject X-ApiCoreX-* headers│
                       │   firewall → ratelimit → bulkhead → CB    │
  client ─HTTP/WS─────►│   httputil.ReverseProxy (streaming) ──────┼──► plugin (any language)
                       │   or WebSocket hijack proxy               │
                       │  /health /plugins /docs /metrics          │
                       └──────────────────────────────────────────┘
```

- **Control plane** — plugins register over HTTP; Core pulls each plugin's manifest from `GET {base_url}/_apicorex/manifest`.
- **Data plane** — the client sends an opaque bearer device token (`zdt_...`); Core hashes it and calls Identity's `POST /internal/introspect` (cached ~30s in-memory per token, so not a network hop on every request) to resolve tenant/branch/user/role/permissions, injects that as `X-ApiCoreX-*` headers, and streams the request to the plugin. Core never parses, signs, or stores tokens.
- **Identity** — authentication, tenant registration, and device-token issuing live in a separate plugin: [apicorex-identity](https://github.com/msrsiddik/apicorex-identity). Core only *introspects* tokens against it.

> **Why device tokens instead of JWTs?** The primary use case is a shared
> device (e.g. one POS terminal with several staff clocking in/out on it). A
> user-bound JWT stays locally valid until expiry even after a shift change —
> risking a stale token reused as the wrong user. A device token is bound to
> the device; Identity resolves the *acting* user fresh on every introspect
> call, so removing/suspending a user locks them out immediately, independent
> of the token's own lifetime. See [apicorex-identity's README](https://github.com/msrsiddik/apicorex-identity#readme) for detail.

---

## Official plugins

Core ships no business logic — these standalone plugins provide it. Each is its
own repo with its own database, migrations, and lifecycle:

| Plugin | Repo | What it does |
|--------|------|--------------|
| **Identity** | [apicorex-identity](https://github.com/msrsiddik/apicorex-identity) | Authentication, multi-tenant registration, device-token issuing, per-tenant plugin install/migrations |
| **Sync** | [apicorex-sync](https://github.com/msrsiddik/apicorex-sync) | Offline-first data sync (push/pull, last-write-wins, tombstones) for any app |
| **Zumo POS** | [zumo-pos](https://github.com/msrsiddik/zumo-pos) | POS domain plugin for offline-first point-of-sale (Bangladesh micro-business: grocery, pharmacy, cosmetics, clothing, hardware) |
| **Medicine Catalog** | [medicine-catalog](https://github.com/msrsiddik/medicine-catalog) | Read-only Bangladesh medicine/drug catalog (shipped SQLite reference data) for pre-filling product-create forms |

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
PLUGIN_API_KEY=dev-key go run ./cmd/apicorex
# Core listens on :8080

# 2. Start the Identity plugin (separate repo; needs DATABASE_URL)
cd ../apicorex-identity
DATABASE_URL=postgres://... PLUGIN_API_KEY=dev-key \
  CORE_URL=http://localhost:8080 PLUGIN_BASE_URL=http://localhost:50051 \
  go run ./cmd/identity

# 3. Register a tenant, log in, call an authenticated route
curl -XPOST localhost:8080/auth/register -H 'Content-Type: application/json' \
  -d '{"slug":"acme","name":"Acme","plan":"starter","email":"o@acme.com","password":"secret123"}'

TOK=$(curl -s -XPOST localhost:8080/auth/login -H 'Content-Type: application/json' \
  -d '{"slug":"acme","email":"o@acme.com","password":"secret123"}' | jq -r .token)

curl localhost:8080/me -H "Authorization: Bearer $TOK"
```

Open **http://localhost:8080/docs** for the Scalar UI (Core + all plugin routes).

> For hot reload during development, run `air` in each repo (config in `.air.toml`).

### With Docker

This repo's `docker-compose.yml` brings up Core + a shared Postgres (the
plugins' database — Core itself has no DB). Each plugin repo ships its own
standalone `docker-compose.yml` that points at this same shared Postgres
(exposed on the host at `15432`) and a running Core to register with:

```
GolandProjects/
├── apicorex/            ← run `docker compose up --build` here first
├── apicorex-identity/   ← then `docker compose up --build` here
└── apicorex-sync/       ← and here
```

```bash
docker compose up --build         # Core + Postgres
# in each plugin repo:
docker compose up --build         # plugin + connects to the shared Postgres above
```

Core listens on `:9999` in this compose stack (`HTTP_PORT` is overridden from
the `:8080` binary default — see [Configuration](#configuration)).

---

## Writing a plugin

A plugin is an HTTP server that serves a manifest + health endpoint and registers
with Core. No SDK, any language. Full guide with Go / Python / Java examples:
**[PLUGIN_GUIDE.md](./PLUGIN_GUIDE.md)**.

Minimal contract:
- `GET /_apicorex/manifest` → JSON describing routes, public paths, migrations, OpenAPI spec
- `GET /_apicorex/health` → `{"status":"ok"}`
- `POST {CORE_URL}/_core/register` on boot (with retry); then heartbeat

Inside a handler, read the context Core injected after resolving the device
token (via Identity's introspection endpoint):
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
| `PLUGIN_API_KEY` | — | Shared key plugins present on register; also authenticates Core to Identity's `/internal/introspect` (unset disables auth — dev only) |
| `PLUGIN_ALLOWLIST` | empty | Comma-separated plugin names allowed to register (empty = allow any, dev) |
| `APICOREX_SECRET` | empty | Login key for the embedded gateway dashboard (`/dashboard`) and `/docs`; unset disables the login form (dev only — open access) |
| `CORS_ALLOWED_ORIGINS` | empty | Comma-separated browser origins allowed to call Core; empty = any origin (dev only) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | empty | Enables OpenTelemetry tracing (e.g. Jaeger) |
| `CONFIG_FILE` | empty | YAML for per-plugin rate/limit overrides — see [config.example.yaml](./config.example.yaml) |

Per-plugin limits (rate, bulkhead, circuit breaker, timeouts, health-check
interval) can also be tuned globally via env vars: `RATE_PER_SEC`,
`BULKHEAD_MAX`, `CB_THRESHOLD`, `CB_RESET_TIMEOUT`, `REQUEST_TIMEOUT`,
`HEALTH_INTERVAL`. The per-tenant rate sub-limit (`tenant_rate_per_sec`,
`tenant_rate_burst` — caps one tenant's share of a plugin's overall budget)
has no env-var form and can only be set via `CONFIG_FILE`.

See [docker-compose.example.yml](./docker-compose.example.yml) for every var
with its default spelled out. `docker-compose.yml` itself reads each one as
`${VAR:-default}`, so it runs unchanged with nothing set — export a var, or
put it in a `.env` file next to the compose file, to override just that one.
The Jenkins pipeline exposes the same vars as build parameters (blank =
compose default) for changing a deploy (e.g. the port) without touching code.

---

## Endpoints

| Path | Auth | Description |
|------|------|-------------|
| `GET /health` | no | Liveness |
| `GET /plugins` | no | Registered plugins |
| `GET /dashboard/*` | dashboard session | Embedded gateway dashboard (plugin registry, route table, breaker/bulkhead state, two gated operator actions) |
| `GET /plugin` | dashboard session | Dashboard entry point (same SPA as `/dashboard`) |
| `GET /docs` | dashboard session | Scalar UI — log in via `/dashboard` first |
| `GET /docs/openapi.json` | dashboard session | Merged OpenAPI (Core + plugins) |
| `GET /metrics` | no | Prometheus metrics |
| `* /_core/*` | api key (register/heartbeat) or dashboard session (`/_core/admin/*`) | Control plane (register/heartbeat/deregister, dashboard login + operator actions) |
| everything else | device token* | Proxied to the owning plugin (*unless the route is public) |

> `/docs` and the dashboard share one login: `POST /_core/admin/login` with
> `APICOREX_SECRET` issues a signed session (cookie for browser navigation,
> bearer token for the SPA's own API calls). Unset `APICOREX_SECRET` and both
> are open — the default for local dev.

---

## Project structure

```
cmd/apicorex/      entrypoint + embedded dashboard
  admin/           gateway dashboard: Next.js SPA, statically exported and
                    embedded into the binary (served at /dashboard, /plugin)
internal/
  auth/            device-token introspection client (calls Identity)
  config/          config-driven protection limits
  controlplane/    HTTP register/heartbeat + signed plugin tokens + dashboard login
  dispatcher/      reverse proxy + WebSocket + tracing + metrics (data plane)
  manifest/        plugin manifest types
  middleware/      auth + CORS + tenant-header injection (anti-spoofing)
  openapi/         OpenAPI spec merge for Scalar UI
  protection/      firewall, rate limit, bulkhead, circuit breaker, health, metrics, logs
  registry/        in-memory plugin store
  tracing/         OpenTelemetry setup
server/            HTTP server wiring
```

Browse the API docs with `go doc ./internal/<pkg>`.
```
