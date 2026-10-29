package registry

import (
	"os"
	"sync"
	"time"

	"github.com/PowerDNS/lmdb-go/lmdb"
)

// Registry keeps the settings and the daily stats in memory, and writes each change through
// to LMDB. Stats are written at most once per statsFlushInterval.
type Registry struct {
	env      *lmdb.Env
	settings lmdb.DBI
	stats    lmdb.DBI

	mu     sync.Mutex
	config storedSettings

	statsMu   sync.Mutex
	stored    map[string]int
	unflushed map[string]bool
	flushedAt time.Time
}

func Open(path string) (*Registry, error) {
	if err := os.MkdirAll(path, 0755); err != nil {
		return nil, err
	}

	env, err := lmdb.NewEnv()
	if err != nil {
		return nil, err
	}
	env.SetMaxDBs(2)
	env.SetMapSize(1 << 30)
	if err := env.Open(path, lmdb.NoTLS, 0644); err != nil {
		env.Close()
		return nil, err
	}

	r := &Registry{env: env, stored: make(map[string]int), unflushed: make(map[string]bool)}
	if err := env.Update(func(txn *lmdb.Txn) (err error) {
		if r.settings, err = txn.OpenDBI("settings", lmdb.Create); err != nil {
			return err
		}
		r.stats, err = txn.OpenDBI("stats", lmdb.Create)
		return err
	}); err != nil {
		env.Close()
		return nil, err
	}
	if err := r.load(); err != nil {
		env.Close()
		return nil, err
	}
	return r, nil
}

func (r *Registry) Close() {
	r.statsMu.Lock()
	r.flushStats()
	r.statsMu.Unlock()
	r.env.Close()
}

func (r *Registry) load() error {
	if err := r.loadSettings(); err != nil {
		return err
	}
	return r.loadStats()
}
