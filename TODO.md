# ApiCoreX Ecosystem — TODO

> Distilled from the 2026-07-22 review docs (`apicorex-review-and-todo.md`,
> `apicorex-power-up-suggestions.md`, `apicorex-INDEX.md`, `apicorex-db-architecture.md`,
> `apicorex-core-dashboard-analysis.md`), cross-checked against the actual code
> as of 2026-07-24. Several review claims were stale by the time of this
> cross-check — noted inline where relevant. Covers both `apicorex` (Core) and
> `apicorex-identity` repos.

## Do now (cheap, real value)

- [x] **Document the "why" behind the device-token switch.** Both READMEs
      described *what* changed (JWT → device token) but not *why* — added a
      short note explaining the multi-driver/shared-device (POS-style) reuse
      problem this closes, plus the fact that introspection is cached (30s TTL
      in Core's `Introspector`, not a network hop on every request).
- [x] **Remove the dead `redis/go-redis` dependency** from `apicorex-identity/go.mod`
      — unused in code since the device-token migration (logout is DB-based via
      `device_tokens.revoked_at`), just leftover weight.

## Consider later (real, not urgent — revisit when the trigger below happens)

- [ ] **Audit logging** for Identity's admin actions (role change, tenant
      suspend/activate, plugin install/reconcile, grant/revoke platform_admin).
      Trigger: once more than one person operates the platform admin console.
      (The other trigger — shipping a Core dashboard write feature — has now
      happened: Core's dashboard got `log.Printf`-based audit lines for its two
      write actions, reset-breaker and force-deregister, in
      `internal/controlplane/handlers.go`. That's a reasonable minimum since
      Core has no database by design; this item is about Identity's
      Postgres-backed actions specifically and is still open.)
- [ ] **Schema migration backfill** for `tenant_users` NOT NULL columns
      (`branch_id`, `role_id`) instead of relying on `scripts/reset-db.sql`.
      Trigger: the moment there's a real production database with data that
      can't be dropped. Already flagged honestly in Identity's README — not
      hidden debt.
- [ ] **Move `sale:void`/`sale:refund` out of the generic RBAC seed catalog**
      into a tenant-custom-role or plugin-scoped permission mechanism.
      Trigger: when a first real domain plugin (POS/sales) actually exists in
      this ecosystem — right now it's a smell, not a bug.
- [ ] **Core dashboard: optional per-plugin console link** (e.g. an "Open
      console" button next to Identity in the Plugins panel, deep-linking to
      its `/console` admin UI instead of duplicating it). Needs a new
      `console_path` (or similar) field on `manifest.Manifest`, added in
      *both* `apicorex/internal/manifest/manifest.go` and
      `apicorex-identity`'s manifest struct/registration payload — a
      cross-repo change, not Core-only. Deferred 2026-07-24: the generic
      Plugins panel already shows every registered plugin's health
      (Identity included), which was the actual point of the dashboard's
      "Phase 4 / connected services" idea — this is just the remaining
      nice-to-have (a direct link out), not blocking. Revisit when actually
      wanted.
- [x] ~~**Narrow, read-only Core dashboard** (plugin registry, route table,
      breaker/bulkhead state)~~ — built 2026-07-24 at `cmd/apicorex/admin`
      (embedded Next.js SPA under `/dashboard`, same stack as Identity's
      console). Phase 1: plugin/route overview + heartbeat freshness. Phase 2:
      per-plugin circuit breaker/bulkhead/rate-limiter state + a parsed
      `/metrics` traffic snapshot. Phase 3: two gated write actions
      (reset-breaker, force-deregister) behind `X-Api-Key` (`PLUGIN_API_KEY`).

## Skip / explicitly deferred (premature at this stage)

- [ ] Vault/KMS secrets management for `PLUGIN_API_KEY` — no compliance/security
      audit driving this yet.
- [ ] First-class API keys for third-party/M2M integrations — no third-party
      consumer exists yet.
- [ ] Webhook/event system, usage-metering/billing hooks — no second/third
      tenant or billing requirement yet.
- [ ] Plugin sandboxing/resource limits beyond the existing bulkhead — only
      matters once plugins are third-party-authored.
- [ ] CLI scaffolding tool for new plugins — `PLUGIN_GUIDE.md` is enough at
      current plugin count (2).
- [ ] CONTRIBUTING.md / CHANGELOG.md / CI badges / tagged releases — only
      matters once there are external contributors or public releases.
- [ ] Plugin marketplace/discovery, API versioning (`Route.Version`),
      multi-region/HA registry — explicitly long-horizon in the source docs
      themselves; needs real multi-author/multi-region demand first.
- [ ] Fully separate DB per tenant / per-plugin schema isolation — the
      `tenantclient.go` "future-proofing seam" comment already marks where this
      would plug in; no action needed until an enterprise/compliance tenant
      actually requires it.

## Not actionable / already resolved (no work needed)

- [x] ~~"Per-request introspection risks coupling Core's uptime to Identity's"~~
      — already answered: `internal/auth/introspect.go` in Core caches results
      for 30s (in-memory, keyed by token hash + acting user), so it's not a
      raw per-request network hop.
- [x] ~~"Duplicate commit in Identity history"~~ — checked `252f1d5` vs
      `a1a89bb`: same commit *message*, entirely different diffs (the second is
      the device-token migration, 5200+ lines). Not a real duplicate, no
      action needed.
- [x] ~~"Google SSO account-linking edge cases undocumented"~~ — already
      implemented and explained in code comments in `internal/auth/google.go`
      (match by `google_sub` → fallback match by email → bind sub). Only
      missing from the README, not from the actual logic.
