package crawler

import (
	"context"
	"slices"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore"
)

const scanPageSize = 5000

// countRelayMentions returns, for each crawlable relay, the number of stored relay lists
// that mention it. It is paged because the LMDB backend allocates for the whole limit up front.
func countRelayMentions(ctx context.Context, store eventstore.Store, allowedPorts []int, pageSize int) (map[string]int, error) {
	counts := make(map[string]int)
	filter := relayListsFilter
	var seenAtUntil map[nostr.ID]bool

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		read := 0
		var oldest nostr.Timestamp
		var idsAtOldest map[nostr.ID]bool
		for event := range store.QueryEvents(filter, pageSize) {
			read++
			if idsAtOldest == nil || event.CreatedAt != oldest {
				oldest, idsAtOldest = event.CreatedAt, make(map[nostr.ID]bool)
			}
			idsAtOldest[event.ID] = true
			if seenAtUntil[event.ID] {
				continue
			}
			for _, url := range mentionedRelays(event, allowedPorts) {
				counts[url]++
			}
		}

		switch {
		case read < pageSize || oldest == 0:
			return counts, nil
		case oldest == filter.Until:
			// More than a page of lists share this timestamp: skip the rest of it.
			filter.Until, seenAtUntil = oldest-1, nil
		default:
			filter.Until, seenAtUntil = oldest, idsAtOldest
		}
	}
}

func mentionedRelays(event nostr.Event, allowedPorts []int) []string {
	var urls []string
	for _, tag := range event.Tags {
		if len(tag) < 2 || tag[0] != "r" {
			continue
		}
		if url, err := normalizeRelayURL(tag[1], allowedPorts); err == nil && !slices.Contains(urls, url) {
			urls = append(urls, url)
		}
	}
	return urls
}
