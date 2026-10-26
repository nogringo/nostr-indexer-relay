package relay

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/lmdb"
	"fiatjaf.com/nostr/nip11"
	"fiatjaf.com/nostr/nip77"
	"fiatjaf.com/nostr/nip77/negentropy"
	"fiatjaf.com/nostr/nip77/negentropy/storage/vector"
	"github.com/coder/websocket"
)

var skA = nostr.MustSecretKeyFromHex("0000000000000000000000000000000000000000000000000000000000000001")

func TestRelayAcceptsOnlyRelayLists(t *testing.T) {
	relay := New(testConfig(), newTestStore(t))
	ctx := context.Background()

	if _, err := relay.AddEvent(ctx, relayList(t, skA, nostr.Now(), "wss://relay.example.com")); err != nil {
		t.Fatalf("expected kind 10002 to be accepted: %v", err)
	}

	for _, kind := range []nostr.Kind{nostr.KindTextNote, nostr.KindDeletion, 10050} {
		if _, err := relay.AddEvent(ctx, signedEvent(t, skA, kind, nostr.Now(), nil)); err == nil {
			t.Fatalf("expected kind %d to be rejected", kind)
		}
	}
}

func TestRelayKeepsNewestListWhateverTheArrivalOrder(t *testing.T) {
	store := newTestStore(t)
	relay := New(testConfig(), store)
	ctx := context.Background()

	newer := relayList(t, skA, 2000, "wss://new.example.com")
	older := relayList(t, skA, 1000, "wss://old.example.com")
	for _, event := range []nostr.Event{newer, older} {
		if _, err := relay.AddEvent(ctx, event); err != nil {
			t.Fatalf("unexpected error adding %s: %v", event.ID, err)
		}
	}

	var stored []nostr.Event
	for event := range store.QueryEvents(nostr.Filter{Authors: []nostr.PubKey{skA.Public()}}, 10) {
		stored = append(stored, event)
	}
	if len(stored) != 1 || stored[0].ID != newer.ID {
		t.Fatalf("expected only the newest list to be stored, got %d events", len(stored))
	}
}

func TestRelayReportsOlderAndKnownListsAsDuplicates(t *testing.T) {
	relay := New(testConfig(), newTestStore(t))
	saved := 0
	relay.OnEventSaved = func(context.Context, nostr.Event) { saved++ }
	ctx := context.Background()

	newer := relayList(t, skA, 2000, "wss://new.example.com")
	for _, test := range []struct {
		name          string
		event         nostr.Event
		wantDuplicate bool
	}{
		{"new", newer, false},
		{"known", newer, true},
		{"older", relayList(t, skA, 1000, "wss://old.example.com"), true},
	} {
		duplicate, err := relay.AddEvent(ctx, test.event)
		if err != nil || duplicate != test.wantDuplicate {
			t.Errorf("%s list: got duplicate %v, %v", test.name, duplicate, err)
		}
	}
	if saved != 1 {
		t.Errorf("expected OnEventSaved for the new list only, got %d calls", saved)
	}
}

func TestRelayServesLargeAuthorsFilter(t *testing.T) {
	relay := New(testConfig(), newTestStore(t))
	ctx := context.Background()

	const authors = 2000
	filter := nostr.Filter{Kinds: []nostr.Kind{nostr.KindRelayListMetadata}}
	for range authors {
		author := nostr.Generate()
		if _, err := relay.AddEvent(ctx, relayList(t, author, nostr.Now(), "wss://relay.example.com")); err != nil {
			t.Fatal(err)
		}
		filter.Authors = append(filter.Authors, author.Public())
	}

	count := 0
	for range relay.QueryStored(ctx, filter) {
		count++
	}
	if count != authors {
		t.Fatalf("expected %d relay lists, got %d", authors, count)
	}
}

func TestRelayInformationListsOnlySupportedNIPs(t *testing.T) {
	relay := New(testConfig(), newTestStore(t))

	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("Accept", "application/nostr+json")
	response := httptest.NewRecorder()
	relay.ServeHTTP(response, request)

	var info nip11.RelayInformationDocument
	if err := json.NewDecoder(response.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	var nips []int
	for _, nip := range info.SupportedNIPs {
		nips = append(nips, int(nip.(float64)))
	}
	slices.Sort(nips)
	if want := []int{1, 11, 40, 42, 45, 65, 70, 77}; !slices.Equal(nips, want) {
		t.Fatalf("expected supported NIPs %v, got %v", want, nips)
	}
}

func TestRelayServesNegentropySessionsBeyondKhatruCap(t *testing.T) {
	cfg := testConfig()
	cfg.MaxLimit = 2
	url := serveRelayWithLists(t, cfg, 50)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ids := 0
	err := nip77.NegentropySync(ctx, url, nostr.Filter{Kinds: []nostr.Kind{nostr.KindRelayListMetadata}}, nil, discard{}, func(_ context.Context, dir nip77.Direction) {
		for range dir.Items {
			ids++
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if ids != 50 {
		t.Fatalf("expected all 50 lists in the session, khatru alone stops at %d; got %d", 20*cfg.MaxLimit, ids)
	}
}

func TestRelayRefusesNegentropySessionsBeyondLimit(t *testing.T) {
	cfg := testConfig()
	cfg.NegentropyLimit = 10
	url := serveRelayWithLists(t, cfg, 11)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	empty := vector.New()
	empty.Seal()
	open, _ := nip77.OpenEnvelope{SubscriptionID: "s", Filter: nostr.Filter{Kinds: []nostr.Kind{nostr.KindRelayListMetadata}}, Message: negentropy.New(empty, 60_000, false, true).Start()}.MarshalJSON()
	if err := conn.Write(ctx, websocket.MessageText, open); err != nil {
		t.Fatal(err)
	}
	_, reply, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// khatru labels it NEG-ERROR instead of NEG-ERR.
	if want := `["NEG-ERROR","s","blocked: too many query results"]`; strings.TrimSpace(string(reply)) != want {
		t.Fatalf("got %s, want %s", reply, want)
	}
}

func serveRelayWithLists(t *testing.T, cfg Config, lists int) string {
	t.Helper()
	relay := New(cfg, newTestStore(t))
	for range lists {
		if _, err := relay.AddEvent(context.Background(), relayList(t, nostr.Generate(), nostr.Now(), "wss://relay.example.com")); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(relay)
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http")
}

type discard struct{}

func (discard) Publish(context.Context, nostr.Event) error { return nil }

func testConfig() Config {
	return Config{
		Addr:        ":0",
		Name:        "test relay",
		Description: "test",
		LMDBPath:    "unused",
		MaxLimit:    defaultMaxLimit,
	}
}

func newTestStore(t *testing.T) *lmdb.LMDBBackend {
	t.Helper()
	store := &lmdb.LMDBBackend{Path: t.TempDir()}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return store
}

func relayList(t *testing.T, sk nostr.SecretKey, createdAt nostr.Timestamp, relayURLs ...string) nostr.Event {
	t.Helper()
	tags := make(nostr.Tags, 0, len(relayURLs))
	for _, url := range relayURLs {
		tags = append(tags, nostr.Tag{"r", url})
	}
	return signedEvent(t, sk, nostr.KindRelayListMetadata, createdAt, tags)
}

func signedEvent(t *testing.T, sk nostr.SecretKey, kind nostr.Kind, createdAt nostr.Timestamp, tags nostr.Tags) nostr.Event {
	t.Helper()
	event := nostr.Event{
		CreatedAt: createdAt,
		Kind:      kind,
		Tags:      tags,
	}
	if err := event.Sign(sk); err != nil {
		t.Fatal(err)
	}
	return event
}
