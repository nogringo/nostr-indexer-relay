package crawler

import (
	"context"
	"fmt"
	"testing"

	"fiatjaf.com/nostr"

	indexer "nostr-indexer-relay/internal/relay"
)

func TestIngest(t *testing.T) {
	c := newTestCrawler(t)
	ctx := context.Background()
	sk := nostr.Generate()

	list := signedList(t, sk, 2000, "wss://relay.example.com")
	if !c.ingest(ctx, list) {
		t.Fatal("expected a new valid list to be stored")
	}
	if c.ingest(ctx, list) {
		t.Error("the same list must not be stored twice")
	}
	if c.ingest(ctx, signedList(t, sk, 1000, "wss://old.example.com")) {
		t.Error("an older list must not be stored")
	}

	protected := signedList(t, nostr.Generate(), 2000)
	protected.Tags = nostr.Tags{{"-"}}
	protected.Sign(nostr.Generate())

	tampered := signedList(t, nostr.Generate(), 2000, "wss://relay.example.com")
	tampered.Tags[0][1] = "wss://evil.example.com"

	badSignature := signedList(t, nostr.Generate(), 2000, "wss://relay.example.com")
	badSignature.Sig[0] ^= 0xff

	var many []string
	for i := range indexer.MaxMessageSize / 30 {
		many = append(many, fmt.Sprintf("wss://relay%d.example.com", i))
	}
	oversized := signedList(t, nostr.Generate(), 2000, many...)

	note := signedList(t, nostr.Generate(), 2000)
	note.Kind = nostr.KindTextNote
	note.Sign(nostr.Generate())

	for name, event := range map[string]nostr.Event{
		"protected": protected, "tampered": tampered, "bad signature": badSignature, "oversized": oversized, "kind 1": note,
	} {
		if c.ingest(ctx, event) {
			t.Errorf("%s: must not be stored", name)
		}
	}
}
