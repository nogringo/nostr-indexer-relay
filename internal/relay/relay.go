package relay

import (
	"cmp"
	"context"
	_ "embed"
	"iter"
	"net/http"
	"slices"
	"strings"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/khatru"
	"fiatjaf.com/nostr/nip11"

	"nostr-indexer-relay/internal/index"
)

// About 1950 authors at 67 bytes each, within defaultMaxLimit: a REQ that fits is never truncated.
// It is also the size limit of every stored event, whatever its path.
const MaxMessageSize = 128 << 10

func New(cfg Config, store *index.Store) *khatru.Relay {
	relay := khatru.NewRelay()
	relay.Addr = cfg.Addr
	relay.ServiceURL = cfg.RelayURL
	relay.Negentropy = true
	relay.MaxMessageSize = MaxMessageSize

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
	serveFullNegentropySessions(relay, store, cmp.Or(cfg.NegentropyLimit, defaultNegentropyLimit))

	return relay
}

// khatru caps a negentropy session at 20 times the max limit and truncates the rest silently,
// so a peer would never learn about older lists. Sessions are served in full instead, and
// refused beyond limit with strfry's reason, which tells the peer to split its sync.
func serveFullNegentropySessions(relay *khatru.Relay, store *index.Store, limit int) {
	queryStored := relay.QueryStored
	relay.QueryStored = func(ctx context.Context, filter nostr.Filter) iter.Seq[nostr.Event] {
		if khatru.IsNegentropySession(ctx) {
			return store.NegentropyEvents(filter)
		}
		return queryStored(ctx, filter)
	}
	relay.OnRequest = func(ctx context.Context, filter nostr.Filter) (bool, string) {
		if !khatru.IsNegentropySession(ctx) {
			return false, ""
		}
		count, err := store.CountEvents(filter)
		if err != nil {
			return true, "error: " + err.Error()
		}
		if int(count) > limit {
			return true, "blocked: too many query results"
		}
		return false, ""
	}
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
