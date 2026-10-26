package relay

import (
	"context"
	_ "embed"
	"net/http"
	"slices"
	"strings"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore"
	"fiatjaf.com/nostr/eventstore/lmdb"
	"fiatjaf.com/nostr/khatru"
	"fiatjaf.com/nostr/nip11"
)

// About 1950 authors at 67 bytes each, within defaultMaxLimit: a REQ that fits is never truncated.
const maxMessageSize = 128 << 10

func New(cfg Config) (*khatru.Relay, func(), error) {
	store := &lmdb.LMDBBackend{Path: cfg.LMDBPath}
	if err := store.Init(); err != nil {
		return nil, nil, err
	}

	return NewWithStore(cfg, store), store.Close, nil
}

func NewWithStore(cfg Config, store eventstore.Store) *khatru.Relay {
	relay := khatru.NewRelay()
	relay.Addr = cfg.Addr
	relay.ServiceURL = cfg.RelayURL
	relay.Negentropy = true
	relay.MaxMessageSize = maxMessageSize

	relay.Info.Name = cfg.Name
	relay.Info.Description = cfg.Description
	relay.Info.Software = "https://github.com/nogringo/nostr-indexer-relay"
	relay.Info.Version = version()
	relay.Info.Icon = cfg.Icon
	relay.Info.Contact = cfg.Contact
	relay.Info.PubKey = cfg.PubKey
	relay.Info.Limitation = &nip11.RelayLimitationDocument{
		MaxMessageLength: int(relay.MaxMessageSize),
		MaxLimit:         cfg.MaxLimit,
		DefaultLimit:     cfg.MaxLimit,
		RestrictedWrites: true,
	}
	// 42 and 70 come from khatru: they let authors publish protected lists themselves.
	relay.Info.SupportedNIPs = []any{1, 11, 40, 42, 45, 65, 70, 77}
	// khatru advertises NIP-09 whenever DeleteEvent is set, but kind 5 is rejected here.
	relay.OverwriteRelayInformation = func(_ context.Context, _ *http.Request, info nip11.RelayInformationDocument) nip11.RelayInformationDocument {
		info.SupportedNIPs = slices.DeleteFunc(slices.Clone(info.SupportedNIPs), func(nip any) bool { return nip == 9 })
		return info
	}

	relay.OnEvent = acceptOnlyRelayLists
	relay.UseEventstore(store, cfg.MaxLimit)

	return relay
}

func acceptOnlyRelayLists(_ context.Context, event nostr.Event) (bool, string) {
	if event.Kind != nostr.KindRelayListMetadata {
		return true, "blocked: only kind 10002 relay lists are accepted"
	}
	return false, ""
}

//go:embed version.txt
var versionFile string

func version() string {
	return strings.TrimSpace(versionFile)
}
