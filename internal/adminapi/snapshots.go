package adminapi

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/msrsiddik/apicorex/internal/snapshots"
)

// Snapshots is what the store backups screen needs. *snapshots.Manager is one.
type Snapshots interface {
	List() ([]snapshots.Snapshot, error)
	Status() snapshots.Status
	Take(ctx context.Context) (snapshots.Snapshot, error)
	Open(name string) (*os.File, error)
}

// SetSnapshots connects the store's snapshots. Without it the routes say
// snapshots are off.
func (h *Handlers) SetSnapshots(s Snapshots) { h.snaps = s }

func (h *Handlers) requireSnapshots(c *gin.Context) bool {
	if h.snaps == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "store snapshots are off on this deployment — see Core's log"})
		return false
	}
	return true
}

func (h *Handlers) listSnapshots(c *gin.Context) {
	if !h.requireSnapshots(c) {
		return
	}
	list, err := h.snaps.List()
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": h.snaps.Status(), "snapshots": list})
}

func (h *Handlers) takeSnapshot(c *gin.Context) {
	if !h.requireSnapshots(c) {
		return
	}
	s, err := h.snaps.Take(c.Request.Context())
	if err != nil {
		fail(c, err)
		return
	}
	if err := h.store.Audit(c.Request.Context(), actor(c), "store.snapshot", "", s.Name); err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, s)
}

// downloadSnapshot sends one snapshot. Every DSN and secret in it is sealed
// and opens only with CORE_MASTER_KEY, but taking a copy of the whole store
// off the server is still worth a line in the audit trail.
func (h *Handlers) downloadSnapshot(c *gin.Context) {
	if !h.requireSnapshots(c) {
		return
	}
	name := c.Param("name")
	f, err := h.snaps.Open(name)
	if errors.Is(err, snapshots.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "no such snapshot"})
		return
	}
	if err != nil {
		fail(c, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		fail(c, err)
		return
	}
	if err := h.store.Audit(c.Request.Context(), actor(c), "store.snapshot.download", "", name); err != nil {
		fail(c, err)
		return
	}
	c.Header("Content-Type", "application/vnd.sqlite3")
	c.Header("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(name, `"`, "")+`"`)
	http.ServeContent(c.Writer, c.Request, name, info.ModTime(), f)
}
