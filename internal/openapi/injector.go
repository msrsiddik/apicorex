// Package openapi collects each plugin's OpenAPI spec and merges them with
// Core's base spec into a single document served at /docs/openapi.json and
// rendered by the Scalar UI at /docs.
package openapi

import (
	"encoding/json"
	"sync"

	"github.com/msrsiddik/apicorex/internal/manifest"
)

// Injector holds each plugin's OpenAPI spec and merges them for Scalar UI. It is
// safe for concurrent use.
type Injector struct {
	mu    sync.RWMutex
	specs map[string][]byte // keyed by plugin name — raw OpenAPI JSON
}

// NewInjector returns an empty Injector.
func NewInjector() *Injector {
	return &Injector{specs: make(map[string][]byte)}
}

// AddRoutes stores a plugin's OpenAPI spec. routes is accepted for API symmetry
// with the registry but only the spec is needed for the merged docs.
func (inj *Injector) AddRoutes(pluginName string, _ []manifest.Route, openapiSpec []byte) {
	if len(openapiSpec) == 0 {
		return
	}
	inj.mu.Lock()
	inj.specs[pluginName] = openapiSpec
	inj.mu.Unlock()
}

// RemoveRoutes drops a plugin's stored spec (called on deregister).
func (inj *Injector) RemoveRoutes(pluginName string) {
	inj.mu.Lock()
	delete(inj.specs, pluginName)
	inj.mu.Unlock()
}

// MergedSpec merges all plugin OpenAPI specs into a single JSON document.
// The first plugin's spec becomes the base; subsequent plugins' paths and
// components are merged into it.
func (inj *Injector) MergedSpec(baseSpec []byte) []byte {
	inj.mu.RLock()
	pluginSpecs := make([][]byte, 0, len(inj.specs))
	for _, s := range inj.specs {
		pluginSpecs = append(pluginSpecs, s)
	}
	inj.mu.RUnlock()

	if len(pluginSpecs) == 0 {
		return baseSpec
	}

	// parse base spec
	var merged map[string]interface{}
	if err := json.Unmarshal(baseSpec, &merged); err != nil {
		return baseSpec
	}

	basePaths, _ := merged["paths"].(map[string]interface{})
	if basePaths == nil {
		basePaths = make(map[string]interface{})
		merged["paths"] = basePaths
	}
	baseComponents, _ := merged["components"].(map[string]interface{})
	if baseComponents == nil {
		baseComponents = make(map[string]interface{})
		merged["components"] = baseComponents
	}
	baseSecurity, _ := merged["security"].([]interface{})

	for _, raw := range pluginSpecs {
		var doc map[string]interface{}
		if err := json.Unmarshal(raw, &doc); err != nil {
			continue
		}

		// merge paths
		if paths, ok := doc["paths"].(map[string]interface{}); ok {
			for path, ops := range paths {
				basePaths[path] = ops
			}
		}

		// merge components (schemas, securitySchemes, etc.)
		if components, ok := doc["components"].(map[string]interface{}); ok {
			for section, defs := range components {
				existing, ok := baseComponents[section].(map[string]interface{})
				if !ok {
					existing = make(map[string]interface{})
				}
				if defsMap, ok := defs.(map[string]interface{}); ok {
					for k, v := range defsMap {
						existing[k] = v
					}
				}
				baseComponents[section] = existing
			}
		}

		// merge the root security requirement (so Scalar's "Authorize" button
		// applies globally even if the base spec didn't declare one itself —
		// e.g. a plugin's spec carries it but Core's base spec doesn't).
		if sec, ok := doc["security"].([]interface{}); ok {
			baseSecurity = mergeSecurityRequirements(baseSecurity, sec)
		}
	}
	if len(baseSecurity) > 0 {
		merged["security"] = baseSecurity
	}

	out, err := json.Marshal(merged)
	if err != nil {
		return baseSpec
	}
	return out
}

// mergeSecurityRequirements unions two OpenAPI "security" arrays (each element
// is a requirement object: scheme name -> scopes), de-duplicating identical
// requirement objects so the same scheme declared by both Core and a plugin
// only appears once.
func mergeSecurityRequirements(base, add []interface{}) []interface{} {
	seen := make(map[string]bool, len(base)+len(add))
	out := make([]interface{}, 0, len(base)+len(add))
	for _, reqs := range [][]interface{}{base, add} {
		for _, req := range reqs {
			key, err := json.Marshal(req)
			if err != nil {
				continue
			}
			if seen[string(key)] {
				continue
			}
			seen[string(key)] = true
			out = append(out, req)
		}
	}
	return out
}
