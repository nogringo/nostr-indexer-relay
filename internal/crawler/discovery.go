package crawler

import (
	"context"
	"slices"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore"

	"nostr-indexer-relay/internal/scan"
)

// countRelayMentions returns, for each crawlable relay, the number of stored relay lists that mention it.
func countRelayMentions(ctx context.Context, store eventstore.Store, allowedPorts []int) (map[string]int, error) {
	counts := make(map[string]int)
	for event := range scan.Events(store, relayListsFilter, scan.PageSize) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, url := range mentionedRelays(event, allowedPorts) {
			counts[url]++
		}
	}
	return counts, nil
}

func mentionedRelays(event nostr.Event, allowedPorts []int) []string {
	var urls []string
	for _, tag := range event.Tags {
		if len(tag) < 2 || tag[0] != "r" {
			continue
		}
		if url, err := NormalizeRelayURL(tag[1], allowedPorts); err == nil && !slices.Contains(urls, url) {
			urls = append(urls, url)
		}
	}
	return urls
}
