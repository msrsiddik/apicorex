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
	}

	out, err := json.Marshal(merged)
	if err != nil {
		return baseSpec
	}
	return out
}
