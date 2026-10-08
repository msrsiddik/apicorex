package store

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Backup writes a consistent copy of the store to dest, which must not exist.
//
// VACUUM INTO reads a single snapshot of the database, so the copy is whole
// even while Core keeps writing — unlike copying the file, which can catch the
// main file and its WAL at different moments. Secrets stay sealed in the copy;
// restoring it needs the same CORE_MASTER_KEY.
func (s *Store) Backup(ctx context.Context, dest string) error {
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("store: backup target %s already exists", dest)
	}
	if dir := filepath.Dir(dest); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("store: create %s: %w", dir, err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, dest); err != nil {
		return fmt.Errorf("store: backup to %s: %w", dest, err)
	}
	return nil
}

const snapshotPrefix = "core-"

// RunSnapshots takes a backup into dir every interval and keeps the newest
// keep of them, until ctx ends. A failed snapshot is logged and retried at the
// next tick: a full disk should not stop the gateway.
//
// Snapshots on the same volume as the store guard against a bad change or a
// corrupt file, not against losing the volume — that needs a copy elsewhere
// (see the README's backup section).
func (s *Store) RunSnapshots(ctx context.Context, dir string, interval time.Duration, keep int) {
	if interval <= 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.snapshot(ctx, dir, keep); err != nil {
				log.Printf("[store] snapshot failed: %v", err)
			}
		}
	}
}

func (s *Store) snapshot(ctx context.Context, dir string, keep int) error {
	name := snapshotPrefix + time.Now().UTC().Format("20060102T150405Z") + ".db"
	if err := s.Backup(ctx, filepath.Join(dir, name)); err != nil {
		return err
	}
	return pruneSnapshots(dir, keep)
}

// pruneSnapshots deletes all but the newest keep snapshots in dir. The names
// carry a UTC timestamp, so sorting them as text sorts them by age.
func pruneSnapshots(dir string, keep int) error {
	if keep <= 0 {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), snapshotPrefix) && strings.HasSuffix(e.Name(), ".db") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for len(names) > keep {
		if err := os.Remove(filepath.Join(dir, names[0])); err != nil {
			return err
		}
		names = names[1:]
	}
	return nil
}
