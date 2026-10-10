package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func openTest(t *testing.T, key []byte) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "core.db"), key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenCreatesDirectoryAndMigrates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "core.db")
	s, err := Open(context.Background(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var v int
	if err := s.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != len(migrations) {
		t.Fatalf("schema at %d, want %d", v, len(migrations))
	}
}

func TestReopenIsIdempotent(t *testing.T) {
	// Every Core restart opens the store again; a migration that ran twice
	// would fail on its CREATE TABLE and stop the gateway from starting.
	path := filepath.Join(t.TempDir(), "core.db")
	ctx := context.Background()
	s, err := Open(ctx, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Audit(ctx, "dashboard", "test", "", ""); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = Open(ctx, path, nil)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer s.Close()
	entries, err := s.ListAudit(ctx, 10, 0)
	if err != nil || len(entries) != 1 {
		t.Fatalf("data did not survive reopen: %v %v", entries, err)
	}
}

func TestOpenRefusesNewerSchema(t *testing.T) {
	// A Core binary rolled back past a migration must not write rows in a
	// shape the newer code does not expect.
	path := filepath.Join(t.TempDir(), "core.db")
	ctx := context.Background()
	s, err := Open(ctx, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, len(migrations)+1, now()); err != nil {
		t.Fatal(err)
	}
	s.Close()

	if _, err := Open(ctx, path, nil); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("opened a store from a newer build: %v", err)
	}
}

func TestAuditNewestFirstAndPaging(t *testing.T) {
	s := openTest(t, nil)
	ctx := context.Background()
	for _, a := range []string{"one", "two", "three"} {
		if err := s.Audit(ctx, "dashboard", a, "schoolyze", ""); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.ListAudit(ctx, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || page[0].Action != "three" || page[1].Action != "two" {
		t.Fatalf("first page: %+v", page)
	}
	if page[0].At.IsZero() {
		t.Error("timestamp did not round-trip")
	}
	rest, err := s.ListAudit(ctx, 2, page[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 1 || rest[0].Action != "one" {
		t.Fatalf("second page: %+v", rest)
	}
}

func TestBackupIsAWorkingCopy(t *testing.T) {
	s := openTest(t, nil)
	ctx := context.Background()
	if err := s.Audit(ctx, "dashboard", "before-backup", "", ""); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "copy.db")
	if err := s.Backup(ctx, dest); err != nil {
		t.Fatal(err)
	}

	c, err := Open(ctx, dest, nil)
	if err != nil {
		t.Fatalf("backup does not open: %v", err)
	}
	defer c.Close()
	entries, err := c.ListAudit(ctx, 10, 0)
	if err != nil || len(entries) != 1 || entries[0].Action != "before-backup" {
		t.Fatalf("backup lost data: %+v %v", entries, err)
	}

	// Never overwrite: a backup run twice to the same name must not replace
	// the good copy with a later, possibly bad, one.
	if err := s.Backup(ctx, dest); err == nil {
		t.Fatal("backup overwrote an existing file")
	}
}

func TestSecretRoundTrip(t *testing.T) {
	s := openTest(t, testKey(t))
	sealed, err := s.sealSecret("postgres://u:p@h/db", "db_config:schoolyze:dsn")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "u:p@h") {
		t.Fatal("plaintext visible in sealed value")
	}
	got, err := s.openSecret(sealed, "db_config:schoolyze:dsn")
	if err != nil || got != "postgres://u:p@h/db" {
		t.Fatalf("round trip: %q %v", got, err)
	}
}

func TestSecretBoundToItsRow(t *testing.T) {
	// A sealed DSN copied into another plugin's row must not open there.
	s := openTest(t, testKey(t))
	sealed, err := s.sealSecret("secret", "db_config:accounting:dsn")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.openSecret(sealed, "db_config:schoolyze:dsn"); err == nil {
		t.Fatal("secret opened under another row's binding")
	}
}

func TestSecretUnderDifferentKey(t *testing.T) {
	a := openTest(t, testKey(t))
	b := openTest(t, testKey(t))
	sealed, err := a.sealSecret("secret", "x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.openSecret(sealed, "x"); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("want ErrWrongKey, got %v", err)
	}
}

func TestNoMasterKey(t *testing.T) {
	s := openTest(t, nil)
	if s.HasMasterKey() {
		t.Fatal("reports a key it was not given")
	}
	if _, err := s.sealSecret("x", "y"); !errors.Is(err, ErrNoMasterKey) {
		t.Fatalf("seal: %v", err)
	}
	if _, err := s.openSecret("v1.abc.def", "y"); !errors.Is(err, ErrNoMasterKey) {
		t.Fatalf("open: %v", err)
	}
}

func TestParseMasterKey(t *testing.T) {
	k := make([]byte, 32)
	if _, err := ParseMasterKey(base64.StdEncoding.EncodeToString(k) + "\n"); err != nil {
		t.Errorf("standard base64 with trailing newline: %v", err)
	}
	if _, err := ParseMasterKey(base64.RawURLEncoding.EncodeToString(k)); err != nil {
		t.Errorf("url-safe base64: %v", err)
	}
	if _, err := ParseMasterKey(base64.StdEncoding.EncodeToString(k[:16])); err == nil {
		t.Error("accepted a 16-byte key")
	}
	if _, err := ParseMasterKey("correct horse battery staple"); err == nil {
		t.Error("accepted a passphrase")
	}
}

func TestInTxRollsBackOnError(t *testing.T) {
	s := openTest(t, nil)
	ctx := context.Background()
	boom := errors.New("boom")
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := writeAudit(ctx, tx, "dashboard", "half-done", "", ""); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	entries, _ := s.ListAudit(ctx, 10, 0)
	if len(entries) != 0 {
		t.Fatalf("rolled-back write survived: %+v", entries)
	}
}
