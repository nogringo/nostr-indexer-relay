package crawler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip77"
	"fiatjaf.com/nostr/nip77/negentropy"
	"fiatjaf.com/nostr/nip77/negentropy/storage/vector"
	"github.com/coder/websocket"

	"nostr-indexer-relay/internal/registry"
)

const (
	probeTimeout      = 10 * time.Second
	probeReadLimit    = 4 << 20
	probeSubscription = "nip77-test"
	countSubscription = "count"
	userAgent         = "nostr-indexer-relay"
)

var relayListsFilter = nostr.Filter{Kinds: []nostr.Kind{nostr.KindRelayListMetadata}}

type probeResult struct {
	status registry.NIP77Status
	detail string
	// The relay's answer to a NIP-45 COUNT of its lists, when one was asked for and given.
	lists   int
	counted bool
}

// probeNIP77 opens a negentropy session on kind 10002 and classifies the first answer. When
// withCount is set and the relay supports NIP-77, it also asks for a NIP-45 COUNT of its lists.
func probeNIP77(ctx context.Context, client *http.Client, url string, withCount bool) probeResult {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPClient: client,
		HTTPHeader: http.Header{"User-Agent": {userAgent}},
	})
	if errors.Is(err, errForbiddenAddress) {
		return probeResult{status: registry.NIP77Forbidden, detail: err.Error()}
	}
	if err != nil {
		return probeResult{status: registry.NIP77Unreachable, detail: err.Error()}
	}
	defer conn.CloseNow()
	conn.SetReadLimit(probeReadLimit)

	var result probeResult
	result.status, result.detail = testNegentropy(ctx, conn)
	if withCount && result.status == registry.NIP77Supported {
		result.lists, result.counted = countLists(ctx, conn)
	}
	return result
}

func testNegentropy(ctx context.Context, conn *websocket.Conn) (registry.NIP77Status, string) {
	open, err := nip77.OpenEnvelope{SubscriptionID: probeSubscription, Filter: relayListsFilter, Message: emptySetMessage()}.MarshalJSON()
	if err != nil {
		return registry.NIP77Unknown, err.Error()
	}
	if err := conn.Write(ctx, websocket.MessageText, open); err != nil {
		return registry.NIP77Unreachable, err.Error()
	}

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return registry.NIP77Unsupported, "no answer: " + err.Error()
		}
		label, first, second := parseMessage(data)
		switch {
		case label == "NOTICE":
			return registry.NIP77Unsupported, first
		case first != probeSubscription:
			continue
		case label == "NEG-MSG":
			if close, err := (nip77.CloseEnvelope{SubscriptionID: probeSubscription}).MarshalJSON(); err == nil {
				conn.Write(ctx, websocket.MessageText, close)
			}
			return registry.NIP77Supported, ""
		// khatru sends NEG-ERROR instead of NIP-77's NEG-ERR.
		case label == "NEG-ERR", label == "NEG-ERROR":
			return registry.NIP77Supported, second
		case label == "CLOSED":
			return registry.NIP77Unsupported, second
		}
	}
}

// countLists asks for a NIP-45 COUNT on kind 10002 and reports false when the relay gives none.
func countLists(ctx context.Context, conn *websocket.Conn) (int, bool) {
	request, err := nostr.CountEnvelope{SubscriptionID: countSubscription, Filter: relayListsFilter}.MarshalJSON()
	if err != nil || conn.Write(ctx, websocket.MessageText, request) != nil {
		return 0, false
	}

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return 0, false
		}
		label, subscription, _ := parseMessage(data)
		switch {
		case label == "NOTICE":
			return 0, false
		case subscription != countSubscription:
			continue
		case label == "COUNT":
			var parts []json.RawMessage
			var answer struct {
				Count *int `json:"count"`
			}
			if json.Unmarshal(data, &parts) != nil || len(parts) < 3 || json.Unmarshal(parts[2], &answer) != nil || answer.Count == nil {
				return 0, false
			}
			return *answer.Count, true
		default:
			return 0, false
		}
	}
}

func emptySetMessage() string {
	empty := vector.New()
	empty.Seal()
	return negentropy.New(empty, 60_000, false, true).Start()
}

// parseMessage returns the label of a relay message and its next two string elements.
func parseMessage(data []byte) (label, first, second string) {
	var parts []json.RawMessage
	if json.Unmarshal(data, &parts) != nil {
		return "", "", ""
	}
	values := make([]string, 3)
	for i := range min(3, len(parts)) {
		json.Unmarshal(parts[i], &values[i])
	}
	return values[0], values[1], values[2]
}
