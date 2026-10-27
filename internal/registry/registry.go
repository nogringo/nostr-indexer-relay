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

	"fiatjaf.com/nostr"
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

type ListsSource string

const (
	ListsUnmeasured ListsSource = ""
	// A NIP-45 COUNT, kept until a full sync measures the relay.
	ListsCounted ListsSource = "count"
	// A full negentropy sync, which is exact.
	ListsSynced ListsSource = "sync"
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
	// A relay that held the same lists at the last test, and is synced and followed in place of this one.
	AliasOf string `json:"alias_of,omitempty"`

	SyncedAt     time.Time     `json:"synced_at,omitzero"`
	NextSyncAt   time.Time     `json:"next_sync_at,omitzero"`
	SyncInterval time.Duration `json:"sync_interval,omitempty"`
	LastSyncNew  int           `json:"last_sync_new,omitempty"`
	SyncDetail   string        `json:"sync_detail,omitempty"`
	// Boundaries between the since/until windows the relay accepts one negentropy session for.
	SyncBoundaries []nostr.Timestamp `json:"sync_boundaries,omitempty"`

	// Number of 10002 the relay holds.
	Lists       int         `json:"lists,omitempty"`
	ListsSource ListsSource `json:"lists_source,omitempty"`
	ListsAt     time.Time   `json:"lists_at,omitzero"`
	Indexer     bool        `json:"indexer,omitempty"`
	// Lists received from this relay that no indexer had delivered, and when the last one came.
	Unique   int       `json:"unique,omitempty"`
	UniqueAt time.Time `json:"unique_at,omitzero"`
	Live     bool      `json:"live,omitempty"`

	// Set by the admin.
	Banned    bool `json:"banned,omitempty"`
	ForceLive bool `json:"force_live,omitempty"`
}

// Forgotten reports whether the relay is no seed and no stored list mentions it any more.
func (relay Relay) Forgotten() bool {
	return !relay.Seed && relay.Popularity == 0
}

// Registry keeps every relay, the settings and the daily stats in memory, and writes each
// change through to LMDB. Stats are written at most once per statsFlushInterval.
type Registry struct {
	env      *lmdb.Env
	dbi      lmdb.DBI
	settings lmdb.DBI
	stats    lmdb.DBI

	mu     sync.Mutex
	relays map[string]*Relay
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
	env.SetMaxDBs(3)
	env.SetMapSize(1 << 30)
	if err := env.Open(path, lmdb.NoTLS, 0644); err != nil {
		env.Close()
		return nil, err
	}

	r := &Registry{env: env, relays: make(map[string]*Relay), stored: make(map[string]int), unflushed: make(map[string]bool)}
	if err := env.Update(func(txn *lmdb.Txn) (err error) {
		if r.dbi, err = txn.OpenDBI("relays", lmdb.Create); err != nil {
			return err
		}
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
	if err := r.retestForAliases(); err != nil {
		env.Close()
		return nil, err
	}
	return r, nil
}

const aliasesRetestedKey = "aliases_retested"

// retestForAliases makes due, once, every relay with a path or a query that passed the NIP-77
// test before aliases were detected, rather than syncing it until its retest 7 days later.
func (r *Registry) retestForAliases() error {
	return r.env.Update(func(txn *lmdb.Txn) error {
		if _, err := txn.Get(r.settings, []byte(aliasesRetestedKey)); err == nil {
			return nil
		} else if !lmdb.IsNotFound(err) {
			return err
		}
		for _, relay := range r.relays {
			if relay.NIP77 != NIP77Supported || relay.Seed || !strings.ContainsAny(strings.TrimPrefix(relay.URL, "wss://"), "/?") {
				continue
			}
			relay.NextTestAt = time.Time{}
			value, err := json.Marshal(relay)
			if err != nil {
				return err
			}
			if err := txn.Put(r.dbi, []byte(relay.URL), value, 0); err != nil {
				return err
			}
		}
		return txn.Put(r.settings, []byte(aliasesRetestedKey), []byte("1"), 0)
	})
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
	if err := r.loadStats(); err != nil {
		return err
	}
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
// Banned relays, and relays that no list mentions any more, are left alone.
func (r *Registry) Due(now time.Time, n int) []Relay {
	return r.due(n, func(relay *Relay) bool { return !relay.NextTestAt.After(now) })
}

// DueForSync is like Due, for the relays that support NIP-77, are no alias, and whose next sync is due.
func (r *Registry) DueForSync(now time.Time, n int) []Relay {
	return r.due(n, func(relay *Relay) bool {
		return relay.NIP77 == NIP77Supported && relay.AliasOf == "" && !relay.NextSyncAt.After(now)
	})
}

func (r *Registry) due(n int, isDue func(*Relay) bool) []Relay {
	r.mu.Lock()
	defer r.mu.Unlock()

	var due []Relay
	for _, relay := range r.relays {
		if !relay.Forgotten() && !relay.Banned && isDue(relay) {
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

// All returns a copy of every relay.
func (r *Registry) All() []Relay {
	r.mu.Lock()
	defer r.mu.Unlock()

	relays := make([]Relay, 0, len(r.relays))
	for _, relay := range r.relays {
		relays = append(relays, *relay)
	}
	return relays
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

// UpdateAll applies update to every relay and keeps the changes of the ones it reports as changed.
func (r *Registry) UpdateAll(update func(*Relay) bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	var changed []*Relay
	for _, relay := range r.relays {
		updated := *relay
		if update(&updated) {
			changed = append(changed, &updated)
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
