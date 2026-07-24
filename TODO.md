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
      Trigger: before shipping any Core dashboard *write* feature, or once more
      than one person operates the platform admin console.
- [ ] **Schema migration backfill** for `tenant_users` NOT NULL columns
      (`branch_id`, `role_id`) instead of relying on `scripts/reset-db.sql`.
      Trigger: the moment there's a real production database with data that
      can't be dropped. Already flagged honestly in Identity's README — not
      hidden debt.
- [ ] **Move `sale:void`/`sale:refund` out of the generic RBAC seed catalog**
      into a tenant-custom-role or plugin-scoped permission mechanism.
      Trigger: when a first real domain plugin (POS/sales) actually exists in
      this ecosystem — right now it's a smell, not a bug.
- [ ] **Narrow, read-only Core dashboard** (plugin registry, route table,
      breaker/bulkhead state) — see `apicorex-core-dashboard-analysis.md` for
      full spec if picked up. Trigger: manual ops (breaker resets, checking
      plugin health) becomes a recurring need beyond `/plugins` + `/metrics` +
      Grafana.

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
