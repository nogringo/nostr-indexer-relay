package relay

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip19"
)

const (
	defaultAddr        = ":3334"
	defaultName        = "Indexer relay"
	defaultDescription = "An indexer relay that stores NIP-65 relay lists (kind 10002) only."
	defaultLMDBPath    = "./data/lmdb"
	defaultStatePath   = "./data/state"
	defaultMaxLimit    = 2000
	// strfry's default: a session holds about 40 bytes per event in memory.
	defaultNegentropyLimit = 1_000_000
)

type Config struct {
	Addr        string
	RelayURL    string
	Name        string
	Description string
	Icon        string
	Contact     string
	PubKey      *nostr.PubKey
	AdminPubKey *nostr.PubKey
	LMDBPath    string
	StatePath   string
	MaxLimit    int
	// Largest negentropy session served; 0 means defaultNegentropyLimit.
	NegentropyLimit int
	// Minimum NIP-13 difficulty of a stored list, read for each event; nil requires none.
	MinPoW func() int
}

func LoadConfig() (Config, error) {
	cfg := Config{
		Addr:        envOrDefault("RELAY_ADDR", defaultAddr),
		RelayURL:    os.Getenv("RELAY_URL"),
		Name:        envOrDefault("RELAY_NAME", defaultName),
		Description: envOrDefault("RELAY_DESCRIPTION", defaultDescription),
		Icon:        os.Getenv("RELAY_ICON"),
		Contact:     os.Getenv("RELAY_CONTACT"),
		LMDBPath:    envOrDefault("RELAY_LMDB_PATH", defaultLMDBPath),
		StatePath:   envOrDefault("RELAY_STATE_PATH", defaultStatePath),
		MaxLimit:    defaultMaxLimit,
	}

	if raw := os.Getenv("RELAY_MAX_LIMIT"); raw != "" {
		maxLimit, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("RELAY_MAX_LIMIT must be an integer: %w", err)
		}
		if maxLimit <= 0 {
			return Config{}, fmt.Errorf("RELAY_MAX_LIMIT must be greater than 0")
		}
		cfg.MaxLimit = maxLimit
	}

	if raw := os.Getenv("RELAY_PUBKEY"); raw != "" {
		pubkey, err := nostr.PubKeyFromHex(raw)
		if err != nil {
			return Config{}, fmt.Errorf("RELAY_PUBKEY must be a 64-character hex nostr pubkey: %w", err)
		}
		cfg.PubKey = &pubkey
	}

	if raw := os.Getenv("RELAY_ADMIN_PUBKEY"); raw != "" {
		admin, err := parsePubKey(raw)
		if err != nil {
			return Config{}, fmt.Errorf("RELAY_ADMIN_PUBKEY must be an npub or a 64-character hex nostr pubkey: %w", err)
		}
		cfg.AdminPubKey = &admin
	}

	return cfg, nil
}

func parsePubKey(raw string) (nostr.PubKey, error) {
	if !strings.HasPrefix(raw, "npub1") {
		return nostr.PubKeyFromHex(raw)
	}
	prefix, value, err := nip19.Decode(raw)
	if err != nil {
		return nostr.PubKey{}, err
	}
	pubkey, ok := value.(nostr.PubKey)
	if prefix != "npub" || !ok {
		return nostr.PubKey{}, fmt.Errorf("not an npub")
	}
	return pubkey, nil
}

func envOrDefault(name string, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
