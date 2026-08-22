# Working in this repo

ApiCoreX Core — the gateway. It routes to plugins, enforces auth and protection,
and knows no domain. It has no database. Keep it that way: the moment Core knows
what a fee or a patient is, every deployment carries every product's vocabulary.

## This repository is PUBLIC

It is MIT licensed and meant to be read by outside plugin authors. That changes
what may be committed here.

**Belongs here:** `PLUGIN_GUIDE.md` (the normative plugin contract), `README.md`,
and engineering-level entries in `TODO.md`.

**Does not belong here:** product roadmaps, which modules exist or do not exist
yet, sibling product names, commercial positioning, or anything describing where
a product is weak. That material lives in the private
`apicorex-identity-private/docs/platform/` — see that repo for the ERP platform
plan and the cross-plugin data contract.

Before adding a document to this repo, ask whether a competitor would be pleased
to find it. Note that deleting a file later does **not** remove it from public
git history — the check has to happen before the commit, not after.

## Running locally

`make dev` runs the service under [air](https://github.com/air-verse/air): it
rebuilds and restarts on every save, loading `.env.dev` first. Install air once
with `go install github.com/air-verse/air@latest`.

**The dev stack is a second stack, not a replacement.** The deployed containers
keep Core on `:9999`, Identity on `:50051` and Schoolyze on `:50053`; the dev
stack runs beside them on `:19999`, `:50151` and `:50153`, against its own
database.

They cannot be mixed. Core evicts any existing registration for a plugin name
(`internal/controlplane/handlers.go`), which is what makes hot reload clean — a
restart replaces its own entry instead of accumulating duplicates. It also means
a dev plugin pointed at the deployed Core evicts the deployed instance, whose
heartbeat then 404s and re-registers, evicting the dev one, forever. So run a dev
plugin against the dev Core only.

`.env.dev` is gitignored and holds a real database password; `.env.dev.example`
is the committed copy. The values in `.env` are for the container build and use
`host.docker.internal`, which does not resolve on the host — do not reuse them.

`make dev` needs no database — Core has none.

## Branching and release flow

```
  feature branch ──▶ develop ──▶ staging ──▶ main
                     (work)      (verify)    (released)
```

**`develop` is where all work happens.** Branch from it, merge back into it.
Never commit directly to `main`, and never start a feature branch from `main` —
`main` reflects what is released, which is behind what is being built.

**`staging` is the verification step, not a formality.** Changes reach `main`
only by way of `staging`; a change that has never been on `staging` has never
been verified against a deployed environment.

**`main` is the released line.** It only ever receives merges from `staging`.

### The one exception, and its obligation

A hotfix may land on `main` directly when production is broken. When that
happens, **merge `main` back into `develop` in the same sitting.** Not later,
not "next time someone notices".

### Why this is written down

On 2026-08-19 work landed straight on `main` in this repo and in Identity, and
never came back. Three days later `develop` was missing a changed deploy
configuration and, in Identity, an entire replaced login model — so anyone
starting work on `develop` would have been building on a login path that had
already been deleted. `staging` had been skipped entirely and still pointed at
2026-08-16. All three repos were re-synced on 2026-08-22.

The drift was silent: every branch existed, every build passed, and nothing
reported that the branches disagreed. That is why the rule needs to be a written
rule rather than a habit.

### Checks worth running before you start

```bash
git rev-list --count develop..main
```

Not `0` means `main` has work `develop` never received — merge `main` into
`develop` before doing anything else, or your work will be built on a stale
base.

`develop` should track `origin/develop`. If `git pull` reports *"no tracking
information"*, set it once with
`git branch --set-upstream-to=origin/develop develop`.

### Committing

Do not run `git commit` or `git push` unless the person you are working with
asks for it in that moment. Editing files freely is fine; publishing is theirs
to decide. This applies to AI agents in particular — an unasked-for push to a
shared branch is not undoable for everyone else.
