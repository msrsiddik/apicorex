package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
