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
	// The same few URLs fill most lists, so each one is parsed once. An empty string is a rejected URL.
	normalized := make(map[string]string)
	for event := range scan.Events(store, relayListsFilter, scan.PageSize) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, url := range mentionedRelays(event, allowedPorts, normalized) {
			counts[url]++
		}
	}
	return counts, nil
}

func mentionedRelays(event nostr.Event, allowedPorts []int, normalized map[string]string) []string {
	var urls []string
	for _, tag := range event.Tags {
		if len(tag) < 2 || tag[0] != "r" {
			continue
		}
		url, seen := normalized[tag[1]]
		if !seen {
			url, _ = NormalizeRelayURL(tag[1], allowedPorts)
			normalized[tag[1]] = url
		}
		if url != "" && !slices.Contains(urls, url) {
			urls = append(urls, url)
		}
	}
	return urls
}
