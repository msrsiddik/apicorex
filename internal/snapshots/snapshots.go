// Package snapshots keeps copies of Core's config store: taken on a schedule
// and on demand, kept on the store's own volume, listed and downloadable from
// the dashboard, and copied off the server through rclone when a remote is
// configured.
//
// The store is small — kilobytes — so every snapshot is a whole copy. What it
// holds that matters is sealed under CORE_MASTER_KEY: a snapshot restores
// only with that key, which must therefore never be kept beside the copies.
package snapshots

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Backer writes a consistent copy of the store to a path that does not exist.
// *store.Store is one.
type Backer interface {
	Backup(ctx context.Context, dest string) error
}

// Remote is where snapshots are copied off the server.
type Remote interface {
	Copy(ctx context.Context, localPath, name string) error
	List(ctx context.Context) ([]string, error)
	Delete(ctx context.Context, name string) error
	String() string
}

// Config is how the manager is set up.
type Config struct {
	Dir      string
	Interval time.Duration // 0 turns scheduled snapshots off
	Keep     int           // newest kept on the server
	// RemoteDaily and RemoteMonthly are kept on the remote: the newest of
	// each of the last that-many days and months that have one.
	RemoteDaily, RemoteMonthly int
}

// Snapshot is one stored copy.
type Snapshot struct {
	Name       string     `json:"name"`
	Size       int64      `json:"size"`
	CreatedAt  time.Time  `json:"created_at"`
	Uploaded   bool       `json:"uploaded"`
	UploadedAt *time.Time `json:"uploaded_at,omitempty"`
}

// Status is what the dashboard shows above the list.
type Status struct {
	LastSnapshot     *time.Time `json:"last_snapshot,omitempty"`
	LastError        string     `json:"last_error,omitempty"`
	LastUpload       *time.Time `json:"last_upload,omitempty"`
	LastUploadError  string     `json:"last_upload_error,omitempty"`
	RemoteConfigured bool       `json:"remote_configured"`
	Remote           string     `json:"remote,omitempty"`
	Interval         string     `json:"interval"`
	Keep             int        `json:"keep"`
}

// ErrNotFound is returned for a name that is not a stored snapshot.
var ErrNotFound = errors.New("no such snapshot")

const (
	prefix    = "core-"
	suffix    = ".db"
	stampFmt  = "20060102T150405Z"
	markerExt = ".uploaded"
)

var nameRE = regexp.MustCompile(`^core-\d{8}T\d{6}Z\.db$`)

// Manager takes and keeps the snapshots.
type Manager struct {
	cfg    Config
	store  Backer
	remote Remote // nil when no remote is configured
	now    func() time.Time

	take   sync.Mutex // one snapshot at a time
	upload sync.Mutex // one upload pass at a time
	mu     sync.Mutex
	status Status
}

// New builds a manager. remote may be nil.
func New(cfg Config, store Backer, remote Remote) (*Manager, error) {
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("snapshots: create %s: %w", cfg.Dir, err)
	}
	// A partial file is one a crash interrupted; it is not a snapshot.
	partials, _ := filepath.Glob(filepath.Join(cfg.Dir, "*.partial"))
	for _, p := range partials {
		os.Remove(p)
	}
	m := &Manager{cfg: cfg, store: store, remote: remote, now: time.Now}
	m.status.RemoteConfigured = remote != nil
	if remote != nil {
		m.status.Remote = remote.String()
	}
	m.status.Interval = cfg.Interval.String()
	m.status.Keep = cfg.Keep
	return m, nil
}

// Take writes a snapshot now, prunes the local ones, and starts copying to
// the remote in the background — the snapshot is safe on disk whether or not
// the remote answers, and a dashboard click should not wait on Drive.
func (m *Manager) Take(ctx context.Context) (Snapshot, error) {
	m.take.Lock()
	defer m.take.Unlock()

	at := m.now().UTC()
	name := prefix + at.Format(stampFmt) + suffix
	final := filepath.Join(m.cfg.Dir, name)
	if _, err := os.Stat(final); err == nil {
		// Two in the same second: the one there is as good as a new one.
		return m.describe(name)
	}
	// Written under another name and renamed, so a crash mid-copy never
	// leaves a file that looks like a snapshot.
	partial := final + ".partial"
	err := m.store.Backup(ctx, partial)
	if err == nil {
		err = os.Rename(partial, final)
	}
	if err != nil {
		os.Remove(partial)
		m.mu.Lock()
		m.status.LastError = err.Error()
		m.mu.Unlock()
		return Snapshot{}, err
	}
	m.mu.Lock()
	m.status.LastSnapshot = &at
	m.status.LastError = ""
	m.mu.Unlock()

	if err := m.pruneLocal(); err != nil {
		log.Printf("[snapshots] prune: %v", err)
	}
	go func() {
		uctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		m.Upload(uctx)
	}()
	return m.describe(name)
}

func (m *Manager) describe(name string) (Snapshot, error) {
	list, err := m.List()
	if err != nil {
		return Snapshot{}, err
	}
	for _, s := range list {
		if s.Name == name {
			return s, nil
		}
	}
	return Snapshot{}, ErrNotFound
}

// Run takes a snapshot every interval until ctx ends. A failed one is logged
// and shown on the dashboard; the next tick tries again.
func (m *Manager) Run(ctx context.Context) {
	if m.cfg.Interval <= 0 {
		return
	}
	// Copies that never reached the remote — Drive was down, the process
	// restarted — go up now rather than at the next snapshot.
	go m.Upload(ctx)
	t := time.NewTicker(m.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := m.Take(ctx); err != nil {
				log.Printf("[snapshots] snapshot failed: %v", err)
			}
		}
	}
}

// List returns the stored snapshots, newest first.
func (m *Manager) List() ([]Snapshot, error) {
	entries, err := os.ReadDir(m.cfg.Dir)
	if err != nil {
		return nil, err
	}
	out := []Snapshot{}
	for _, e := range entries {
		if e.IsDir() || !nameRE.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		s := Snapshot{Name: e.Name(), Size: info.Size(), CreatedAt: stampOf(e.Name())}
		if mk, err := os.ReadFile(filepath.Join(m.cfg.Dir, e.Name()+markerExt)); err == nil {
			if t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(mk))); err == nil {
				s.Uploaded, s.UploadedAt = true, &t
			}
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func stampOf(name string) time.Time {
	t, _ := time.Parse(stampFmt, strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix))
	return t
}

// Open returns a stored snapshot for download. Only names the manager writes
// are accepted, so nothing else in the directory — or outside it — can be
// asked for.
func (m *Manager) Open(name string) (*os.File, error) {
	if !nameRE.MatchString(name) {
		return nil, ErrNotFound
	}
	f, err := os.Open(filepath.Join(m.cfg.Dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return f, err
}

// Status reports the last snapshot and upload. After a restart the in-memory
// record is empty, so the newest file stands in for the last snapshot.
func (m *Manager) Status() Status {
	m.mu.Lock()
	st := m.status
	m.mu.Unlock()
	if st.LastSnapshot == nil {
		if list, err := m.List(); err == nil && len(list) > 0 {
			t := list[0].CreatedAt
			st.LastSnapshot = &t
		}
	}
	return st
}

func (m *Manager) pruneLocal() error {
	if m.cfg.Keep <= 0 {
		return nil
	}
	list, err := m.List()
	if err != nil {
		return err
	}
	for i, s := range list {
		if i < m.cfg.Keep {
			continue
		}
		if err := os.Remove(filepath.Join(m.cfg.Dir, s.Name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		os.Remove(filepath.Join(m.cfg.Dir, s.Name+markerExt))
	}
	return nil
}

// Upload copies every stored snapshot not yet on the remote, oldest first so
// an interrupted pass leaves no gap, then prunes the remote by its own policy.
func (m *Manager) Upload(ctx context.Context) {
	if m.remote == nil {
		return
	}
	m.upload.Lock()
	defer m.upload.Unlock()

	list, err := m.List()
	if err != nil {
		m.setUploadError(err)
		return
	}
	for i := len(list) - 1; i >= 0; i-- {
		s := list[i]
		if s.Uploaded {
			continue
		}
		if err := m.remote.Copy(ctx, filepath.Join(m.cfg.Dir, s.Name), s.Name); err != nil {
			m.setUploadError(fmt.Errorf("copy %s to %s: %w", s.Name, m.remote, err))
			return
		}
		done := m.now().UTC()
		if err := os.WriteFile(filepath.Join(m.cfg.Dir, s.Name+markerExt), []byte(done.Format(time.RFC3339)), 0o600); err != nil {
			m.setUploadError(err)
			return
		}
		m.mu.Lock()
		m.status.LastUpload, m.status.LastUploadError = &done, ""
		m.mu.Unlock()
	}

	remote, err := m.remote.List(ctx)
	if err != nil {
		m.setUploadError(fmt.Errorf("list %s: %w", m.remote, err))
		return
	}
	var names []string
	for _, n := range remote {
		if nameRE.MatchString(n) {
			names = append(names, n)
		}
	}
	keep := keepRemote(names, m.cfg.RemoteDaily, m.cfg.RemoteMonthly)
	for _, n := range names {
		if !keep[n] {
			if err := m.remote.Delete(ctx, n); err != nil {
				m.setUploadError(fmt.Errorf("prune %s on %s: %w", n, m.remote, err))
				return
			}
		}
	}
}

func (m *Manager) setUploadError(err error) {
	log.Printf("[snapshots] upload: %v", err)
	m.mu.Lock()
	m.status.LastUploadError = err.Error()
	m.mu.Unlock()
}

// keepRemote keeps the newest snapshot of each of the last daily days and
// monthly months that have one. Counting days that have a copy, not calendar
// days, means a server off for a month does not come back to find every copy
// outside the window.
func keepRemote(names []string, daily, monthly int) map[string]bool {
	sorted := append([]string(nil), names...)
	sort.Sort(sort.Reverse(sort.StringSlice(sorted))) // names sort by time
	keep := map[string]bool{}
	pick := func(limit int, period func(time.Time) string) {
		seen := map[string]bool{}
		for _, n := range sorted {
			if len(seen) >= limit {
				return
			}
			if k := period(stampOf(n)); !seen[k] {
				seen[k] = true
				keep[n] = true
			}
		}
	}
	pick(daily, func(t time.Time) string { return t.Format("2006-01-02") })
	pick(monthly, func(t time.Time) string { return t.Format("2006-01") })
	return keep
}
