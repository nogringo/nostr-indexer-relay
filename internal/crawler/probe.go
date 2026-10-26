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
	userAgent         = "nostr-indexer-relay"
)

var relayListsFilter = nostr.Filter{Kinds: []nostr.Kind{nostr.KindRelayListMetadata}}

// probeNIP77 opens a negentropy session on kind 10002 and classifies the first answer.
func probeNIP77(ctx context.Context, client *http.Client, url string) (registry.NIP77Status, string) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPClient: client,
		HTTPHeader: http.Header{"User-Agent": {userAgent}},
	})
	if errors.Is(err, errForbiddenAddress) {
		return registry.NIP77Forbidden, err.Error()
	}
	if err != nil {
		return registry.NIP77Unreachable, err.Error()
	}
	defer conn.CloseNow()
	conn.SetReadLimit(probeReadLimit)

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
