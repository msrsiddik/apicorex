package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ErrDomainNotFound: no tenant has a verified claim on this hostname.
var ErrDomainNotFound = errors.New("domain not found")

// ResolvedDomain is what a custom-domain request needs routed: which tenant,
// and which of the plugin's surfaces (panel/portal/website for Schoolyze,
// opaque to Core) it's for.
type ResolvedDomain struct {
	TenantID   string `json:"tenant_id"`
	TenantSlug string `json:"tenant_slug"`
	SchemaName string `json:"schema_name"`
	BranchID   string `json:"branch_id"`
	Surface    string `json:"surface"`
}

type domainCacheEntry struct {
	res *ResolvedDomain
	err error
	exp time.Time
}

// DomainResolver resolves a Host header to a tenant by calling Identity's
// plugin-to-plugin /internal/resolve-domain endpoint, with the same small
// cache Introspector uses — a custom domain changes rarely, so a short TTL
// costs nothing and saves a round trip on every request that uses one.
type DomainResolver struct {
	reg       baseURLLookup
	pluginKey string
	client    *http.Client

	mu    sync.Mutex
	cache map[string]domainCacheEntry
}

// NewDomainResolver builds a DomainResolver. pluginKey is the shared
// PLUGIN_API_KEY that authenticates Core to Identity's internal endpoint —
// the same one Introspector uses.
func NewDomainResolver(reg baseURLLookup, pluginKey string) *DomainResolver {
	return &DomainResolver{
		reg:       reg,
		pluginKey: pluginKey,
		client:    &http.Client{Timeout: 5 * time.Second},
		cache:     map[string]domainCacheEntry{},
	}
}

type resolveDomainRequest struct {
	Hostname string `json:"hostname"`
}

// Resolve turns a Host header into the tenant that claimed it.
func (d *DomainResolver) Resolve(ctx context.Context, hostname string) (*ResolvedDomain, error) {
	hostname = strings.ToLower(strings.TrimSpace(hostname))

	d.mu.Lock()
	if e, ok := d.cache[hostname]; ok && time.Now().Before(e.exp) {
		d.mu.Unlock()
		return e.res, e.err
	}
	d.mu.Unlock()

	res, err := d.fetch(ctx, hostname)
	if !errors.Is(err, ErrUnavailable) {
		d.mu.Lock()
		now := time.Now()
		for k, e := range d.cache {
			if now.After(e.exp) {
				delete(d.cache, k)
			}
		}
		d.cache[hostname] = domainCacheEntry{res: res, err: err, exp: now.Add(cacheTTL)}
		d.mu.Unlock()
	}
	return res, err
}

func (d *DomainResolver) fetch(ctx context.Context, hostname string) (*ResolvedDomain, error) {
	baseURL, ok := d.reg.GetBaseURL("identity")
	if !ok {
		return nil, ErrUnavailable
	}
	body, _ := json.Marshal(resolveDomainRequest{Hostname: hostname})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(baseURL, "/")+"/internal/resolve-domain", bytes.NewReader(body))
	if err != nil {
		return nil, ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Plugin-Key", d.pluginKey)

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var res ResolvedDomain
		if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
			return nil, ErrUnavailable
		}
		return &res, nil
	case http.StatusNotFound:
		return nil, ErrDomainNotFound
	default:
		return nil, ErrUnavailable
	}
}
