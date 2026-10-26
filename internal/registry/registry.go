package registry

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/PowerDNS/lmdb-go/lmdb"
)

type NIP77Status string

const (
	NIP77Unknown     NIP77Status = ""
	NIP77Supported   NIP77Status = "supported"
	NIP77Unsupported NIP77Status = "unsupported"
	NIP77Unreachable NIP77Status = "unreachable"
	NIP77Forbidden   NIP77Status = "forbidden"
)

type Relay struct {
	URL        string      `json:"url"`
	Seed       bool        `json:"seed,omitempty"`
	Popularity int         `json:"popularity"`
	NIP77      NIP77Status `json:"nip77,omitempty"`
	Detail     string      `json:"detail,omitempty"`
	TestedAt   time.Time   `json:"tested_at,omitzero"`
	NextTestAt time.Time   `json:"next_test_at,omitzero"`
	Failures   int         `json:"failures,omitempty"`
}

// Registry keeps every relay in memory and writes each change through to LMDB.
type Registry struct {
	env *lmdb.Env
	dbi lmdb.DBI

	mu     sync.Mutex
	relays map[string]*Relay
}

func Open(path string) (*Registry, error) {
	if err := os.MkdirAll(path, 0755); err != nil {
		return nil, err
	}

	env, err := lmdb.NewEnv()
	if err != nil {
		return nil, err
	}
	env.SetMaxDBs(1)
	env.SetMapSize(1 << 30)
	if err := env.Open(path, lmdb.NoTLS, 0644); err != nil {
		env.Close()
		return nil, err
	}

	r := &Registry{env: env, relays: make(map[string]*Relay)}
	if err := env.Update(func(txn *lmdb.Txn) (err error) {
		r.dbi, err = txn.OpenDBI("relays", lmdb.Create)
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
	r.env.Close()
}

func (r *Registry) load() error {
	return r.env.View(func(txn *lmdb.Txn) error {
		cursor, err := txn.OpenCursor(r.dbi)
		if err != nil {
			return err
		}
		defer cursor.Close()

		for {
			_, value, err := cursor.Get(nil, nil, lmdb.Next)
			if lmdb.IsNotFound(err) {
				return nil
			}
			if err != nil {
				return err
			}
			var relay Relay
			if err := json.Unmarshal(value, &relay); err != nil {
				return err
			}
			r.relays[relay.URL] = &relay
		}
	})
}

func (r *Registry) Get(url string) (Relay, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	relay, ok := r.relays[url]
	if !ok {
		return Relay{}, false
	}
	return *relay, true
}

// SetPopularity stores the result of a full scan of the relay lists: known relays
// missing from counts drop to 0, unknown ones are added untested.
func (r *Registry) SetPopularity(counts map[string]int, seeds []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	isSeed := make(map[string]bool, len(seeds))
	for _, url := range seeds {
		isSeed[url] = true
	}

	var changed []*Relay
	for url, relay := range r.relays {
		if relay.Popularity != counts[url] || relay.Seed != isSeed[url] {
			updated := *relay
			updated.Popularity, updated.Seed = counts[url], isSeed[url]
			changed = append(changed, &updated)
		}
	}
	added := make(map[string]bool)
	for _, url := range slices.Concat(slices.Collect(maps.Keys(counts)), seeds) {
		if _, known := r.relays[url]; !known && !added[url] {
			added[url] = true
			changed = append(changed, &Relay{URL: url, Popularity: counts[url], Seed: isSeed[url]})
		}
	}

	if err := r.put(changed...); err != nil {
		return err
	}
	for _, relay := range changed {
		r.relays[relay.URL] = relay
	}
	return nil
}

// Due returns up to n relays whose next test is due, seeds first, then the most popular.
// Relays that no list mentions any more are left alone.
func (r *Registry) Due(now time.Time, n int) []Relay {
	r.mu.Lock()
	defer r.mu.Unlock()

	var due []Relay
	for _, relay := range r.relays {
		if (relay.Seed || relay.Popularity > 0) && !relay.NextTestAt.After(now) {
			due = append(due, *relay)
		}
	}
	slices.SortFunc(due, func(a, b Relay) int {
		if a.Seed != b.Seed {
			if a.Seed {
				return -1
			}
			return 1
		}
		return cmp.Or(cmp.Compare(b.Popularity, a.Popularity), strings.Compare(a.URL, b.URL))
	})
	return due[:min(n, len(due))]
}

func (r *Registry) Update(url string, update func(*Relay)) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	current, ok := r.relays[url]
	if !ok {
		return fmt.Errorf("unknown relay %s", url)
	}
	updated := *current
	update(&updated)
	if err := r.put(&updated); err != nil {
		return err
	}
	r.relays[url] = &updated
	return nil
}

func (r *Registry) put(relays ...*Relay) error {
	if len(relays) == 0 {
		return nil
	}
	return r.env.Update(func(txn *lmdb.Txn) error {
		for _, relay := range relays {
			value, err := json.Marshal(relay)
			if err != nil {
				return err
			}
			if err := txn.Put(r.dbi, []byte(relay.URL), value, 0); err != nil {
				return fmt.Errorf("storing %s: %w", relay.URL, err)
			}
		}
		return nil
	})
}
