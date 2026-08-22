package manifest

import (
	"crypto/sha256"
	"encoding/hex"
)

// maxIdentifier is Postgres's identifier limit. A longer name is not rejected —
// it is silently truncated to 63 bytes, which is how two tenants with similar
// long slugs would end up sharing one schema without anyone being told.
const maxIdentifier = 63

// TenantSchemaFor returns the Postgres schema a plugin's tenant-scoped tables
// live in.
//
// base is the tenant's own schema (tenant_<slug>). A plugin declaring
// TenantSchema "shared" is handed it unchanged; every other plugin gets
// base + "__" + plugin, a schema of its own that no other plugin's database
// role has USAGE on.
//
// Identity derives the same name independently — it has to, since it creates the
// schema and runs migrations into it before Core ever names it in a header, and
// the two do not share a module. The rule is duplicated in
// apicorex-identity/internal/tenantschema, and the two must agree exactly: a
// disagreement means a plugin migrating into one schema and reading from
// another, with no error anywhere, just empty tables. Both sides have tests
// pinning the same cases.
func TenantSchemaFor(base, plugin, mode string) string {
	if base == "" || mode == TenantSchemaShared {
		return base
	}
	name := base + "__" + plugin
	if len(name) <= maxIdentifier {
		return name
	}
	// Too long to keep whole. Truncating alone would let two different plugins
	// or tenants collapse onto one schema, so the tail carries a digest of the
	// full name: deterministic, and distinct wherever the full names differ.
	sum := sha256.Sum256([]byte(name))
	suffix := "_" + hex.EncodeToString(sum[:])[:8]
	return name[:maxIdentifier-len(suffix)] + suffix
}
