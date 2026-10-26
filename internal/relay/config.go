package relay

import (
	"fmt"
	"os"
	"strconv"

	"fiatjaf.com/nostr"
)

const (
	defaultAddr        = ":3334"
	defaultName        = "Indexer relay"
	defaultDescription = "An indexer relay that stores NIP-65 relay lists (kind 10002) only."
	defaultLMDBPath    = "./data/lmdb"
	defaultMaxLimit    = 2000
)

type Config struct {
	Addr        string
	RelayURL    string
	Name        string
	Description string
	Icon        string
	Contact     string
	PubKey      *nostr.PubKey
	LMDBPath    string
	MaxLimit    int
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

	return cfg, nil
}

func envOrDefault(name string, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
