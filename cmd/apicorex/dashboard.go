package main

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/gin-gonic/gin"
)

// dashboardFS is Core's own gateway dashboard: a Next.js app statically
// exported to admin/out and embedded into the binary. It's a self-contained,
// read-only SPA that calls Core's own JSON endpoints (/plugins) directly —
// same origin, no token handling.
//
// The app is built with basePath "/dashboard", so every asset it references
// lives under /dashboard/_next/.... serveDashboard maps those request paths
// onto files inside admin/out.
//
//go:embed all:admin/out
var dashboardFS embed.FS

// dashboardOut is dashboardFS rooted at admin/out, so a path like
// "_next/static/x" resolves directly.
var dashboardOut = mustSubDashboard(dashboardFS, "admin/out")

func mustSubDashboard(f embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic("dashboard: embed sub " + dir + ": " + err.Error())
	}
	return sub
}

// serveDashboard serves the exported Next.js app under /dashboard. Core
// registers this on the wildcard route /dashboard/*filepath, so filepath is
// the asset path relative to /dashboard — "/" for the app entry, or e.g.
// "/_next/static/chunks/main.js" for an asset.
func serveDashboard(c *gin.Context) {
	rel := strings.TrimPrefix(c.Param("filepath"), "/")
	if rel == "" {
		rel = "index.html"
	}

	data, ctype, ok := readDashboardFile(rel)
	if !ok {
		// Unknown path — hand back the SPA entry so client-side routing can take
		// over, matching how a static host with a catch-all would behave.
		data, ctype, ok = readDashboardFile("index.html")
		if !ok {
			c.String(http.StatusInternalServerError, "dashboard UI missing")
			return
		}
	}
	c.Data(http.StatusOK, ctype, data)
}

// readDashboardFile reads an embedded asset and picks a content type from its
// extension. A bare path with no file resolves to that directory's
// index.html (Next.js emits foo/index.html for trailingSlash routes).
func readDashboardFile(rel string) (data []byte, contentType string, ok bool) {
	b, err := fs.ReadFile(dashboardOut, rel)
	if err != nil {
		if path.Ext(rel) == "" {
			if b2, err2 := fs.ReadFile(dashboardOut, path.Join(rel, "index.html")); err2 == nil {
				return b2, "text/html; charset=utf-8", true
			}
		}
		return nil, "", false
	}
	return b, contentTypeForDashboard(rel), true
}

func contentTypeForDashboard(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".txt":
		return "text/plain; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".ico":
		return "image/x-icon"
	case ".png":
		return "image/png"
	case ".woff2":
		return "font/woff2"
	case ".woff":
		return "font/woff"
	default:
		return "application/octet-stream"
	}
}
