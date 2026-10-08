package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/msrsiddik/apicorex/internal/store"
)

// openStore opens the config store at STORE_PATH, sealing secrets with
// CORE_MASTER_KEY when it is set.
//
// A missing key is a warning, not a failure: routing needs no secrets, and a
// gateway that refused to start over it would turn a config gap into an
// outage. A key that is present but malformed is fatal — that is a typo, and
// running on would quietly disable every secret the operator meant to keep.
func openStore(ctx context.Context) *store.Store {
	path := envOr("STORE_PATH", filepath.Join("data", "core.db"))

	var key []byte
	if raw := os.Getenv("CORE_MASTER_KEY"); raw != "" {
		k, err := store.ParseMasterKey(raw)
		if err != nil {
			log.Fatalf("CORE_MASTER_KEY: %v", err)
		}
		key = k
	} else {
		log.Println("[warn] CORE_MASTER_KEY not set — the dashboard cannot save secrets such as database connections")
	}

	st, err := store.Open(ctx, path, key)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	log.Printf("[store] %s", path)
	return st
}

// seedDefaultDBConfig fills the default database connection from
// SEED_DATABASE_URL on a store that has none yet, so a first deploy does not
// start with every plugin unconfigured. It never overwrites: once the default
// exists, the dashboard owns it, and a seed value left in the environment
// cannot quietly undo a change made there.
func seedDefaultDBConfig(ctx context.Context, st *store.Store) {
	dsn := os.Getenv("SEED_DATABASE_URL")
	if dsn == "" {
		return
	}
	if _, err := st.GetDBConfig(ctx, store.DefaultPlugin); err == nil {
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		log.Printf("[store] seed: %v", err)
		return
	}
	_, err := st.SaveDBConfig(ctx, store.DefaultPlugin, store.DBConfigInput{
		DSNAction: store.DSNSet, DSN: dsn, Note: "seeded from SEED_DATABASE_URL",
	}, "seed")
	if err != nil {
		// Not fatal: the gateway routes without it, and the dashboard can set
		// the default by hand. Loud, because a seed that silently failed looks
		// exactly like one that was never configured.
		log.Printf("[warn] SEED_DATABASE_URL not applied: %v", err)
		return
	}
	log.Printf("[store] default database connection seeded from SEED_DATABASE_URL")
}

// snapshotSettings reads how often the store snapshots itself and how many
// snapshots it keeps. Daily and a week's worth unless told otherwise; an
// interval of 0 turns snapshots off.
func snapshotSettings() (dir string, interval time.Duration, keep int) {
	path := envOr("STORE_PATH", filepath.Join("data", "core.db"))
	dir = filepath.Join(filepath.Dir(path), "snapshots")

	interval = 24 * time.Hour
	if v := os.Getenv("STORE_SNAPSHOT_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 0 {
			log.Printf("[warn] STORE_SNAPSHOT_INTERVAL=%q is not a duration; using %s", v, interval)
		} else {
			interval = d
		}
	}

	keep = 7
	if v := os.Getenv("STORE_SNAPSHOT_KEEP"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			log.Printf("[warn] STORE_SNAPSHOT_KEEP=%q is not a positive number; keeping %d", v, keep)
		} else {
			keep = n
		}
	}
	return dir, interval, keep
}

// runStoreCommand handles `apicorex store <subcommand>`, for an operator at a
// shell — typically `docker compose exec core /app/apicorex store backup …`.
// It returns the process exit code.
func runStoreCommand(args []string) int {
	usage := func() int {
		fmt.Fprintln(os.Stderr, "usage: apicorex store backup <path>")
		return 2
	}
	if len(args) == 0 {
		return usage()
	}
	switch args[0] {
	case "backup":
		if len(args) != 2 {
			return usage()
		}
		ctx := context.Background()
		st := openStore(ctx)
		defer st.Close()
		if err := st.Backup(ctx, args[1]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		_ = st.Audit(ctx, "cli", "store.backup", "", args[1])
		fmt.Println("backup written to", args[1])
		return 0
	default:
		return usage()
	}
}
