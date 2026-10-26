package registry

import (
	"crypto/rand"
	"encoding/json"
	"slices"
	"time"

	"fiatjaf.com/nostr"
	"github.com/PowerDNS/lmdb-go/lmdb"
)

var defaultSeeds = []string{
	"wss://indexer.coracle.social",
	"wss://relay.ditto.pub",
	"wss://relay.nos.social",
	"wss://directory.yabu.me",
	"wss://profiles.nostr1.com",
}

// Settings are changed by the admin, so that nothing has to be configured through files.
type Settings struct {
	Seeds []string `json:"seeds"`
	// Ports other than 443 that relay URLs may use.
	AllowedPorts []int `json:"allowed_ports"`
	LiveCap      int   `json:"live_cap"`
	// Share of our lists a relay must hold to be an indexer, between 0 and 1.
	IndexerShare float64 `json:"indexer_share"`
	// A non-indexer that brings no list the indexers miss for this long loses its live subscription.
	LiveQuietPeriod time.Duration `json:"live_quiet_period"`
	// Whether the public search page is served.
	Visitors bool `json:"visitors"`
}

func DefaultSettings() Settings {
	return Settings{
		Seeds:           slices.Clone(defaultSeeds),
		LiveCap:         50,
		IndexerShare:    0.5,
		LiveQuietPeriod: 7 * 24 * time.Hour,
		Visitors:        true,
	}
}

// storedSettings is everything kept under the settings database, the admin's settings and the
// state of the admin interface itself.
type storedSettings struct {
	settings      Settings
	admin         *nostr.PubKey
	sessionSecret []byte
}

const (
	settingsKey      = "settings"
	adminKey         = "admin"
	sessionSecretKey = "session_secret"
)

func (r *Registry) loadSettings() error {
	r.config.settings = DefaultSettings()
	return r.env.Update(func(txn *lmdb.Txn) error {
		if value, err := txn.Get(r.settings, []byte(settingsKey)); err == nil {
			// Unmarshalled over the defaults, so that a setting added later starts at its default.
			if err := json.Unmarshal(value, &r.config.settings); err != nil {
				return err
			}
		} else if !lmdb.IsNotFound(err) {
			return err
		}

		if value, err := txn.Get(r.settings, []byte(adminKey)); err == nil {
			admin, err := nostr.PubKeyFromHex(string(value))
			if err != nil {
				return err
			}
			r.config.admin = &admin
		} else if !lmdb.IsNotFound(err) {
			return err
		}

		secret, err := txn.Get(r.settings, []byte(sessionSecretKey))
		if lmdb.IsNotFound(err) {
			secret = make([]byte, 32)
			rand.Read(secret)
			err = txn.Put(r.settings, []byte(sessionSecretKey), secret, 0)
		}
		if err != nil {
			return err
		}
		r.config.sessionSecret = slices.Clone(secret)
		return nil
	})
}

func (r *Registry) Settings() Settings {
	r.mu.Lock()
	defer r.mu.Unlock()

	settings := r.config.settings
	settings.Seeds = slices.Clone(settings.Seeds)
	settings.AllowedPorts = slices.Clone(settings.AllowedPorts)
	return settings
}

func (r *Registry) SetSettings(settings Settings) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	value, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	if err := r.env.Update(func(txn *lmdb.Txn) error {
		return txn.Put(r.settings, []byte(settingsKey), value, 0)
	}); err != nil {
		return err
	}
	settings.Seeds = slices.Clone(settings.Seeds)
	settings.AllowedPorts = slices.Clone(settings.AllowedPorts)
	r.config.settings = settings
	return nil
}

func (r *Registry) Admin() (nostr.PubKey, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.config.admin == nil {
		return nostr.PubKey{}, false
	}
	return *r.config.admin, true
}

func (r *Registry) SetAdmin(admin nostr.PubKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := r.env.Update(func(txn *lmdb.Txn) error {
		return txn.Put(r.settings, []byte(adminKey), []byte(admin.Hex()), 0)
	}); err != nil {
		return err
	}
	r.config.admin = &admin
	return nil
}

// SessionSecret returns the key that signs admin session cookies, created on first start.
func (r *Registry) SessionSecret() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.config.sessionSecret)
}
