// Package auth resolves the opaque device tokens issued by the Identity plugin.
// Core never sees claims — every request's bearer is hashed and introspected
// against Identity (with a short cache), which returns the tenant scope and the
// ACTING user's fresh role/permissions. Core only forwards trusted headers; it
// never issues or parses tokens itself.
package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ErrInvalidToken: unknown or revoked device token — the device must log in again.
var ErrInvalidToken = errors.New("invalid token")

// ErrMembershipRevoked: the token is fine but the acting user (or the token's
// owner) has been removed/suspended from the tenant.
var ErrMembershipRevoked = errors.New("membership revoked")

// ErrUnavailable: Identity could not be reached — the caller gets a 503, not a
// 401, so an identity outage doesn't wipe app sessions.
var ErrUnavailable = errors.New("auth unavailable")

// cacheTTL bounds how stale an introspection result may be. Revocations and
// role changes take effect within this window.
const cacheTTL = 30 * time.Second

// Identity is the resolved request identity Core injects as X-ApiCoreX-*
// headers: tenant/branch scope from the device token, user/roles/permissions
// from the ACTING user (fresh from Identity's DB).
type Identity struct {
	TenantID    string   `json:"tenant_id"`
	TenantSlug  string   `json:"tenant_slug"`
	SchemaName  string   `json:"schema_name"`
	BranchID    string   `json:"branch_id,omitempty"`
	BranchSlug  string   `json:"branch_slug,omitempty"`
	UserID      string   `json:"user_id"`
	UserType    string   `json:"user_type"`
	Roles       []string `json:"roles"`
	Permissions []string `json:"permissions"`
	// TokenHash is the sha256 of the bearer — injected so Identity's logout /
	// branch-switch can act on the exact token row.
	TokenHash string `json:"-"`
}

// HashToken returns the sha256 hex digest of a raw device token — the only form
// that ever leaves Core.
func HashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// baseURLLookup is how the Introspector finds the live Identity plugin —
// satisfied by *registry.Registry.
type baseURLLookup interface {
	GetBaseURL(name string) (string, bool)
}

type cacheEntry struct {
	id  *Identity
	err error
	exp time.Time
}

// Introspector resolves device tokens by calling Identity's plugin-to-plugin
// /internal/introspect endpoint, with a small in-memory cache keyed by
// (token hash, acting user).
type Introspector struct {
	reg       baseURLLookup
	pluginKey string
	client    *http.Client

	mu    sync.Mutex
	cache map[string]cacheEntry
}

// NewIntrospector builds an Introspector. pluginKey is the shared PLUGIN_API_KEY
// that authenticates Core to Identity's internal endpoint.
func NewIntrospector(reg baseURLLookup, pluginKey string) *Introspector {
	return &Introspector{
		reg:       reg,
		pluginKey: pluginKey,
		client:    &http.Client{Timeout: 5 * time.Second},
		cache:     map[string]cacheEntry{},
	}
}

type introspectRequest struct {
	TokenHash    string `json:"token_hash"`
	ActingUserID string `json:"acting_user_id"`
}

// Resolve turns a token hash + optional acting user into the request Identity.
// Definitive auth answers (valid, invalid, revoked) are cached for cacheTTL;
// transport failures are never cached.
func (i *Introspector) Resolve(ctx context.Context, tokenHash, actingUserID string) (*Identity, error) {
	key := tokenHash + "|" + actingUserID

	i.mu.Lock()
	if e, ok := i.cache[key]; ok && time.Now().Before(e.exp) {
		i.mu.Unlock()
		return e.id, e.err
	}
	i.mu.Unlock()

	id, err := i.fetch(ctx, tokenHash, actingUserID)
	if !errors.Is(err, ErrUnavailable) {
		i.mu.Lock()
		// opportunistic sweep so dead entries don't accumulate forever
		now := time.Now()
		for k, e := range i.cache {
			if now.After(e.exp) {
				delete(i.cache, k)
			}
		}
		i.cache[key] = cacheEntry{id: id, err: err, exp: now.Add(cacheTTL)}
		i.mu.Unlock()
	}
	return id, err
}

func (i *Introspector) fetch(ctx context.Context, tokenHash, actingUserID string) (*Identity, error) {
	baseURL, ok := i.reg.GetBaseURL("identity")
	if !ok {
		return nil, ErrUnavailable
	}
	body, _ := json.Marshal(introspectRequest{TokenHash: tokenHash, ActingUserID: actingUserID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(baseURL, "/")+"/internal/introspect", bytes.NewReader(body))
	if err != nil {
		return nil, ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Plugin-Key", i.pluginKey)

	resp, err := i.client.Do(req)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var id Identity
		if err := json.NewDecoder(resp.Body).Decode(&id); err != nil {
			return nil, ErrUnavailable
		}
		id.TokenHash = tokenHash
		return &id, nil
	case http.StatusUnauthorized:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if strings.Contains(string(b), "membership revoked") {
			return nil, ErrMembershipRevoked
		}
		return nil, ErrInvalidToken
	default:
		return nil, ErrUnavailable
	}
}
