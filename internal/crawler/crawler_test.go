package crawler

import (
	"testing"
	"time"

	"nostr-indexer-relay/internal/registry"
)

func TestRecordTestSchedulesTheNextOne(t *testing.T) {
	now := time.Now()
	var relay registry.Relay

	for failure, wantDelay := range []time.Duration{time.Hour, 2 * time.Hour, 4 * time.Hour} {
		recordTest(&relay, registry.NIP77Unreachable, "", now)
		if relay.Failures != failure+1 || relay.NextTestAt.Sub(now) != wantDelay {
			t.Fatalf("failure %d: got %d failures, next test in %s", failure+1, relay.Failures, relay.NextTestAt.Sub(now))
		}
	}

	relay.Failures = deadAfterFailures - 2
	recordTest(&relay, registry.NIP77Unreachable, "", now)
	if relay.NextTestAt.Sub(now) != 64*time.Hour {
		t.Fatalf("expected the last backoff before dead to be 64h, got %s", relay.NextTestAt.Sub(now))
	}
	recordTest(&relay, registry.NIP77Unreachable, "", now)
	if relay.NextTestAt.Sub(now) != deadRetestInterval {
		t.Fatalf("expected a dead relay to be retested in %s, got %s", deadRetestInterval, relay.NextTestAt.Sub(now))
	}

	recordTest(&relay, registry.NIP77Supported, "", now)
	if relay.Failures != 0 || relay.NextTestAt.Sub(now) != retestInterval {
		t.Fatalf("expected a reachable relay to reset failures and be retested in %s, got %+v", retestInterval, relay)
	}

	recordTest(&relay, registry.NIP77Forbidden, "", now)
	if relay.NextTestAt.Sub(now) != deadRetestInterval {
		t.Fatalf("expected a forbidden relay to be retested in %s, got %s", deadRetestInterval, relay.NextTestAt.Sub(now))
	}
}
