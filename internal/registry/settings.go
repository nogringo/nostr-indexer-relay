package registry

import (
	"crypto/rand"
	"encoding/json"
	"slices"

	"fiatjaf.com/nostr"
	"github.com/PowerDNS/lmdb-go/lmdb"
)

// Settings are changed by the admin, so that nothing has to be configured through files.
type Settings struct {
	// Whether the public search page is served.
	Visitors bool `json:"visitors"`
	// Minimum NIP-13 difficulty, in bits, of a list to be stored. 0 requires none.
	MinPoW int `json:"min_pow"`
}

func DefaultSettings() Settings {
	return Settings{Visitors: true}
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
	return r.config.settings
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
