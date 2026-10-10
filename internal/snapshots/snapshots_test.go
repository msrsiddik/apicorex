package snapshots

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeStore writes a small file, or fails.
type fakeStore struct{ err error }

func (f fakeStore) Backup(_ context.Context, dest string) error {
	if f.err != nil {
		os.WriteFile(dest, []byte("half"), 0o600) // as a crashed copy would
		return f.err
	}
	return os.WriteFile(dest, []byte("sqlite"), 0o600)
}

// fakeRemote is a directory of names, with an optional failure.
type fakeRemote struct {
	mu     sync.Mutex
	files  map[string]bool
	copied []string
	fail   error
}

func (r *fakeRemote) Copy(_ context.Context, local, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return r.fail
	}
	if _, err := os.Stat(local); err != nil {
		return err
	}
	r.files[name] = true
	r.copied = append(r.copied, name)
	return nil
}

func (r *fakeRemote) List(context.Context) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for n := range r.files {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

func (r *fakeRemote) Delete(_ context.Context, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.files, name)
	return nil
}

func (r *fakeRemote) String() string { return "fake:core-store" }

func manager(t *testing.T, store Backer, remote Remote, keep int) *Manager {
	t.Helper()
	m, err := New(Config{Dir: t.TempDir(), Keep: keep, RemoteDaily: 30, RemoteMonthly: 12}, store, remote)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// at pins the clock, so names — which carry the time to the second — differ.
func at(m *Manager, ts string) {
	tm, _ := time.Parse(time.RFC3339, ts)
	m.now = func() time.Time { return tm }
}

func TestTakeListOpen(t *testing.T) {
	m := manager(t, fakeStore{}, nil, 7)
	at(m, "2026-10-10T02:30:00Z")
	s, err := m.Take(context.Background())
	if err != nil || s.Name != "core-20261010T023000Z.db" || s.Size == 0 {
		t.Fatalf("%+v %v", s, err)
	}
	f, err := m.Open(s.Name)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	for _, bad := range []string{"../core.db", "core.db", "core-20261010T023000Z.db.uploaded", "x"} {
		if _, err := m.Open(bad); !errors.Is(err, ErrNotFound) {
			t.Errorf("%q opened: %v", bad, err)
		}
	}
	if st := m.Status(); st.LastSnapshot == nil || st.RemoteConfigured {
		t.Errorf("%+v", st)
	}
}

func TestFailedSnapshotLeavesNothing(t *testing.T) {
	m := manager(t, fakeStore{err: errors.New("disk full")}, nil, 7)
	if _, err := m.Take(context.Background()); err == nil {
		t.Fatal("no error")
	}
	entries, _ := os.ReadDir(m.cfg.Dir)
	if len(entries) != 0 {
		t.Fatalf("left behind: %v", entries)
	}
	if st := m.Status(); !strings.Contains(st.LastError, "disk full") {
		t.Errorf("error not shown: %+v", st)
	}
}

func TestNewRemovesPartials(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "core-20261010T023000Z.db.partial"), []byte("half"), 0o600)
	if _, err := New(Config{Dir: dir}, fakeStore{}, nil); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("partial kept: %v", entries)
	}
}

func TestKeepsNewestLocally(t *testing.T) {
	m := manager(t, fakeStore{}, nil, 2)
	for _, ts := range []string{"2026-10-08T02:30:00Z", "2026-10-09T02:30:00Z", "2026-10-10T02:30:00Z"} {
		at(m, ts)
		if _, err := m.Take(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	list, _ := m.List()
	if len(list) != 2 || list[0].Name != "core-20261010T023000Z.db" || list[1].Name != "core-20261009T023000Z.db" {
		t.Fatalf("%+v", list)
	}
}

func TestUploadOldestFirstRetriesAndMarks(t *testing.T) {
	r := &fakeRemote{files: map[string]bool{}, fail: errors.New("token expired")}
	m := manager(t, fakeStore{}, r, 7)
	ctx := context.Background()
	m.Take(ctx) // the background upload fails
	at(m, "2026-10-09T02:30:00Z")
	m.Take(ctx)
	at(m, "2026-10-10T02:30:00Z")
	m.Take(ctx)
	m.Upload(ctx) // runs after the background ones: the lock serialises them
	if st := m.Status(); !strings.Contains(st.LastUploadError, "token expired") {
		t.Fatalf("failure not shown: %+v", st)
	}

	r.mu.Lock()
	r.fail = nil
	r.mu.Unlock()
	m.Upload(ctx)
	list, _ := m.List()
	for _, s := range list {
		if !s.Uploaded || s.UploadedAt == nil {
			t.Errorf("%s not marked uploaded", s.Name)
		}
	}
	if st := m.Status(); st.LastUploadError != "" || st.LastUpload == nil {
		t.Errorf("recovered upload not shown: %+v", st)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !sort.StringsAreSorted(r.copied) {
		t.Errorf("not oldest first: %v", r.copied)
	}
}

func TestRemoteKeepsDailyAndMonthly(t *testing.T) {
	var names []string
	// Three per day for 40 days, then one a month back across a year.
	start := time.Date(2026, 10, 10, 2, 30, 0, 0, time.UTC)
	for d := 0; d < 40; d++ {
		for h := 0; h < 3; h++ {
			names = append(names, "core-"+start.AddDate(0, 0, -d).Add(time.Duration(h)*time.Hour).Format(stampFmt)+".db")
		}
	}
	for mo := 2; mo < 15; mo++ {
		names = append(names, "core-"+start.AddDate(0, -mo, 0).Format(stampFmt)+".db")
	}
	keep := keepRemote(names, 30, 12)
	days, months := map[string]bool{}, map[string]bool{}
	for n := range keep {
		ts := stampOf(n)
		days[ts.Format("2006-01-02")] = true
		months[ts.Format("2006-01")] = true
	}
	if len(keep) > 30+12 {
		t.Errorf("kept %d, more than one per day and month", len(keep))
	}
	if !keep["core-20261010T043000Z.db"] {
		t.Error("the newest was not kept")
	}
	if len(months) != 12 {
		t.Errorf("months kept: %d", len(months))
	}
}

func TestNoRemoteNoUpload(t *testing.T) {
	m := manager(t, fakeStore{}, nil, 7)
	m.Upload(context.Background()) // must not panic
	m.Take(context.Background())
	list, _ := m.List()
	if list[0].Uploaded {
		t.Fatal("marked uploaded with no remote")
	}
}
