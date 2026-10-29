package relay

import (
	"cmp"
	"context"
	_ "embed"
	"fmt"
	"iter"
	"net/http"
	"slices"
	"strings"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore"
	"fiatjaf.com/nostr/khatru"
	"fiatjaf.com/nostr/nip11"
	"fiatjaf.com/nostr/nip13"

	"nostr-indexer-relay/internal/scan"
)

// About 1950 authors at 67 bytes each, within defaultMaxLimit: a REQ that fits is never truncated.
const maxMessageSize = 128 << 10

func New(cfg Config, store eventstore.Store) *khatru.Relay {
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
	relay.Info.SupportedNIPs = []any{1, 11, 13, 40, 42, 45, 65, 70, 77}
	// khatru advertises NIP-09 whenever DeleteEvent is set, but kind 5 is rejected here.
	relay.OverwriteRelayInformation = func(_ context.Context, _ *http.Request, info nip11.RelayInformationDocument) nip11.RelayInformationDocument {
		info.SupportedNIPs = slices.DeleteFunc(slices.Clone(info.SupportedNIPs), func(nip any) bool { return nip == 9 })
		if cfg.MinPoW != nil {
			limitation := *info.Limitation
			limitation.MinPowDifficulty = cfg.MinPoW()
			info.Limitation = &limitation
		}
		return info
	}

	relay.OnEvent = acceptRelayLists(cfg.MinPoW)
	relay.UseEventstore(store, cfg.MaxLimit)
	reportOlderListsAsDuplicates(relay, store)
	serveFullNegentropySessions(relay, store, cmp.Or(cfg.NegentropyLimit, defaultNegentropyLimit))

	return relay
}

// The store drops a list older than ours, or one it already has, without an error. khatru would
// then broadcast it and call OnEventSaved, so such a list is reported as a duplicate instead.
func reportOlderListsAsDuplicates(relay *khatru.Relay, store eventstore.Store) {
	replace := relay.ReplaceEvent
	relay.ReplaceEvent = func(ctx context.Context, event nostr.Event) error {
		for current := range store.QueryEvents(nostr.Filter{Kinds: []nostr.Kind{event.Kind}, Authors: []nostr.PubKey{event.PubKey}}, 1) {
			if !nostr.IsOlder(current, event) {
				return eventstore.ErrDupEvent
			}
		}
		return replace(ctx, event)
	}
}

// khatru caps a negentropy session at 20 times the max limit and truncates the rest silently,
// so a peer would never learn about older lists. Sessions are served in full instead, and
// refused beyond limit with strfry's reason, which tells the peer to split its sync.
func serveFullNegentropySessions(relay *khatru.Relay, store eventstore.Store, limit int) {
	queryStored := relay.QueryStored
	relay.QueryStored = func(ctx context.Context, filter nostr.Filter) iter.Seq[nostr.Event] {
		if khatru.IsNegentropySession(ctx) {
			return scan.Events(store, filter, scan.PageSize)
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

func acceptRelayLists(minPoW func() int) func(context.Context, nostr.Event) (bool, string) {
	return func(_ context.Context, event nostr.Event) (bool, string) {
		if event.Kind != nostr.KindRelayListMetadata {
			return true, "blocked: only kind 10002 relay lists are accepted"
		}
		if minPoW == nil {
			return false, ""
		}
		// The committed target counts, as NIP-13 advises, so that a list aimed lower that got lucky is refused.
		if min, work := minPoW(), nip13.CommittedDifficulty(event); work < min {
			return true, fmt.Sprintf("pow: difficulty %d is less than %d", work, min)
		}
		return false, ""
	}
}

//go:embed version.txt
var versionFile string

func version() string {
	return strings.TrimSpace(versionFile)
}
