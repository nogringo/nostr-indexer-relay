package crawler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
)

// Relays that refuse a session on all their lists are compared on this recent window instead.
const aliasRecentWindow = 7 * 24 * time.Hour

var errRefused = errors.New("negentropy session refused")

// ancestors returns the URLs that url extends, shortest first: wss://h/a/b?q gives wss://h,
// wss://h/a and wss://h/a/b.
func ancestors(url string) []string {
	base, _, hasQuery := strings.Cut(url, "?")
	var result []string
	for i := len("wss://"); i < len(base); i++ {
		if base[i] == '/' {
			result = append(result, base[:i])
		}
	}
	if hasQuery {
		result = append(result, base)
	}
	return result
}

// aliasOf returns the first of candidates that holds the same lists as url right now, or ""
// when none does. Both sides are asked at the same moment, with the same until.
func aliasOf(ctx context.Context, client *http.Client, url string, candidates []string) string {
	if len(candidates) == 0 {
		return ""
	}
	now := nostr.Now()
	filters := []nostr.Filter{
		{Kinds: relayListsFilter.Kinds, Until: now},
		{Kinds: relayListsFilter.Kinds, Since: now - nostr.Timestamp(aliasRecentWindow/time.Second), Until: now},
	}
	urls := slices.Concat([]string{url}, candidates)

	for _, filter := range filters {
		answers := make([]string, len(urls))
		errs := make([]error, len(urls))
		var wg sync.WaitGroup
		for i, u := range urls {
			wg.Go(func() { answers[i], errs[i] = firstAnswer(ctx, client, u, filter) })
		}
		wg.Wait()
		if slices.ContainsFunc(errs, func(err error) bool { return errors.Is(err, errRefused) }) {
			continue
		}

		// A relay that holds no list there answers with our own empty set, which tells nothing.
		if errs[0] != nil || answers[0] == emptySetMessage() {
			return ""
		}
		for i, candidate := range candidates {
			if errs[i+1] == nil && answers[i+1] == answers[0] {
				return candidate
			}
		}
		return ""
	}
	return ""
}

// firstAnswer returns the relay's first NEG-MSG for a session on filter opened with an empty
// set, which depends only on the lists it holds there.
func firstAnswer(ctx context.Context, client *http.Client, url string, filter nostr.Filter) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	conn, err := dial(ctx, client, url)
	if err != nil {
		return "", err
	}
	defer conn.CloseNow()

	if err := openSession(ctx, conn, filter); err != nil {
		return "", err
	}
	label, content, err := sessionAnswer(ctx, conn)
	switch {
	case err != nil:
		return "", err
	case label == "NEG-MSG":
		return content, nil
	case refused(label):
		return "", errRefused
	default:
		return "", fmt.Errorf("%s: %s", label, content)
	}
}
