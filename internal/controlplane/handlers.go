// Package controlplane implements the HTTP control plane: plugins register,
// heartbeat, and deregister here. Core pulls each plugin's manifest from
// GET {base_url}/_apicorex/manifest.
package controlplane

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/msrsiddik/apicorex/internal/dispatcher"
	"github.com/msrsiddik/apicorex/internal/manifest"
	"github.com/msrsiddik/apicorex/internal/openapi"
	"github.com/msrsiddik/apicorex/internal/protection"
	"github.com/msrsiddik/apicorex/internal/registry"
)

type Handlers struct {
	reg       *registry.Registry
	disp      *dispatcher.Dispatcher
	injector  *openapi.Injector
	apiKey    string
	allowlist map[string]bool // empty = allow any (dev)
	signer    *tokenSigner
	client    *http.Client
}

// New builds the control-plane handlers. allowlist is the set of plugin names
// permitted to register (empty slice = allow any, for dev). The signer secret
// should be a strong secret (reuse JWT_SECRET or a dedicated one).
func New(reg *registry.Registry, disp *dispatcher.Dispatcher, injector *openapi.Injector, apiKey string, allowlist []string, signerSecret string) *Handlers {
	al := make(map[string]bool, len(allowlist))
	for _, n := range allowlist {
		if n = strings.TrimSpace(n); n != "" {
			al[n] = true
		}
	}
	return &Handlers{
		reg:       reg,
		disp:      disp,
		injector:  injector,
		apiKey:    apiKey,
		allowlist: al,
		signer:    newTokenSigner(signerSecret, 24*time.Hour),
		client:    &http.Client{Timeout: 10 * time.Second},
	}
}

// Mount registers the /_core/* routes on the engine.
func (h *Handlers) Mount(engine *gin.Engine) {
	g := engine.Group("/_core")
	g.POST("/register", h.register)
	g.POST("/heartbeat", h.heartbeat)
	g.POST("/deregister", h.deregister)
	g.GET("/plugins/:name/manifest", h.pluginManifest)
}

type registerReq struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
}

func (h *Handlers) register(c *gin.Context) {
	var req registerReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if h.apiKey != "" && req.APIKey != h.apiKey {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid api key"})
		return
	}
	if req.BaseURL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "base_url required"})
		return
	}

	// pull the manifest from the plugin
	m, err := h.pullManifest(req.BaseURL)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("cannot pull manifest: %v", err)})
		return
	}

	// allowlist check (empty allowlist = allow any, for dev)
	if len(h.allowlist) > 0 && !h.allowlist[m.Name] {
		log.Printf("[controlplane] rejected unlisted plugin %q from %s", m.Name, req.BaseURL)
		c.JSON(http.StatusForbidden, gin.H{"error": fmt.Sprintf("plugin %q not in allowlist", m.Name)})
		return
	}

	// Evict any stale entries for this plugin name (e.g. a previous instance that
	// restarted without deregistering) so re-registration replaces instead of
	// accumulating duplicates.
	for _, oldID := range h.reg.IDsByName(m.Name) {
		h.reg.Deregister(oldID)
		h.disp.RemoveRoutes(oldID)
	}

	pluginID := fmt.Sprintf("%s-%s", m.Name, uuid.New().String()[:8])

	if err := h.reg.Register(pluginID, m.Name, req.BaseURL, m.Version, m.PluginType, m, h.disp.ProxyFor); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.disp.AddRoutes(pluginID, m.Name, m.PluginType, m.Routes)
	h.injector.AddRoutes(m.Name, m.Routes, m.OpenAPISpec)

	// issue a signed plugin token; plugin presents it on heartbeat/deregister
	token := h.signer.issue(pluginID)

	protection.PluginsRegistered.Set(float64(len(h.reg.List())))
	log.Printf("[controlplane] registered %s (%s) at %s — %d routes", m.Name, pluginID, req.BaseURL, len(m.Routes))
	c.JSON(http.StatusOK, gin.H{"plugin_id": pluginID, "plugin_token": token})
}

func (h *Handlers) heartbeat(c *gin.Context) {
	var req struct {
		PluginID    string `json:"plugin_id"`
		PluginToken string `json:"plugin_token"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if !h.verifyToken(req.PluginToken, req.PluginID) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid plugin token"})
		return
	}
	if err := h.reg.Heartbeat(req.PluginID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "plugin not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"acknowledged": true})
}

func (h *Handlers) deregister(c *gin.Context) {
	var req struct {
		PluginID    string `json:"plugin_id"`
		PluginToken string `json:"plugin_token"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if !h.verifyToken(req.PluginToken, req.PluginID) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid plugin token"})
		return
	}
	entry, ok := h.reg.Get(req.PluginID)
	if !ok {
		c.JSON(http.StatusOK, gin.H{"success": false})
		return
	}
	name := entry.Info.PluginName
	h.reg.Deregister(req.PluginID)
	h.disp.RemoveRoutes(req.PluginID)
	h.injector.RemoveRoutes(name)
	protection.PluginsRegistered.Set(float64(len(h.reg.List())))
	log.Printf("[controlplane] deregistered %s", req.PluginID)
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// verifyToken checks the signed plugin token and that it matches the claimed plugin ID.
func (h *Handlers) verifyToken(token, pluginID string) bool {
	tid, err := h.signer.verify(token)
	return err == nil && tid == pluginID
}

// pluginManifest returns a registered plugin's stored manifest (Identity pulls migrations from here).
func (h *Handlers) pluginManifest(c *gin.Context) {
	name := c.Param("name")
	m, ok := h.reg.GetManifest(name)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "plugin not found"})
		return
	}
	c.JSON(http.StatusOK, m)
}

func (h *Handlers) pullManifest(baseURL string) (manifest.Manifest, error) {
	var m manifest.Manifest
	resp, err := h.client.Get(baseURL + "/_apicorex/manifest")
	if err != nil {
		return m, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return m, fmt.Errorf("manifest endpoint returned %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return m, fmt.Errorf("decode manifest: %w", err)
	}
	if m.Name == "" {
		return m, fmt.Errorf("manifest missing name")
	}
	return m, nil
}
