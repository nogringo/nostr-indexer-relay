package crawler

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/lmdb"

	indexer "nostr-indexer-relay/internal/relay"
)

func TestWindow(t *testing.T) {
	boundaries := []nostr.Timestamp{100, 200}
	want := [][2]nostr.Timestamp{{0, 99}, {100, 199}, {200, 0}}
	for i, w := range want {
		if since, until := window(boundaries, i); since != w[0] || until != w[1] {
			t.Errorf("window %d: got [%d, %d], want %v", i, since, until, w)
		}
	}
	if since, until := window(nil, 0); since != 0 || until != 0 {
		t.Errorf("without boundaries the only window must be open, got [%d, %d]", since, until)
	}
}

func TestSplit(t *testing.T) {
	now := nostrEpoch + 1000
	if cut, ok := split(0, 0, now); !ok || cut != nostrEpoch+500 {
		t.Errorf("open window: got %d %v", cut, ok)
	}
	if cut, ok := split(nostrEpoch+100, nostrEpoch+300, now); !ok || cut != nostrEpoch+200 {
		t.Errorf("closed window: got %d %v", cut, ok)
	}
	if _, ok := split(nostrEpoch+100, nostrEpoch+101, now); ok {
		t.Error("a two-second window must not be cut")
	}
}

func TestSyncRelayDownloadsWhatWeLackWindowByWindow(t *testing.T) {
	remote := newTestCrawler(t)
	local := newTestCrawler(t)

	at := func(month int) nostr.Timestamp {
		return nostr.Timestamp(time.Date(2021, time.Month(1+month), 1, 0, 0, 0, 0, time.UTC).Unix())
	}
	var shared []nostr.Event
	for i := range 10 {
		list := signedList(t, nostr.Generate(), at(i*4), "wss://relay.example.com")
		mustAdd(t, remote, list)
		if i < 3 {
			mustAdd(t, local, list)
			shared = append(shared, list)
		}
	}
	keepsNewer, getsNewer := nostr.Generate(), nostr.Generate()
	mustAdd(t, remote, signedList(t, keepsNewer, at(1), "wss://old.example.com"))
	mustAdd(t, local, signedList(t, keepsNewer, at(2), "wss://new.example.com"))
	mustAdd(t, local, signedList(t, getsNewer, at(1), "wss://old.example.com"))
	newer := signedList(t, getsNewer, at(2), "wss://new.example.com")
	mustAdd(t, remote, newer)

	// The remote refuses sessions over 4 lists, so its 12 lists need several windows.
	server := httptest.NewServer(indexer.New(indexer.Config{MaxLimit: 100, NegentropyLimit: 4}, remote.store))
	defer server.Close()

	result, err := local.syncRelay(context.Background(), wsURL(server), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.stored != 8 {
		t.Errorf("expected 7 new lists and 1 newer one, got %d stored", result.stored)
	}
	if len(result.boundaries) == 0 {
		t.Error("expected the sync to split its window")
	}

	count := 0
	for event := range local.store.QueryEvents(relayListsFilter, 100) {
		count++
		if event.PubKey == keepsNewer.Public() && event.Tags[0][1] != "wss://new.example.com" {
			t.Error("an older remote list replaced our newer one")
		}
		if event.PubKey == getsNewer.Public() && event.ID != newer.ID {
			t.Error("the newer remote list did not replace our older one")
		}
	}
	if count != 12 {
		t.Errorf("expected one list per pubkey, 12 in all, got %d", count)
	}

	again, err := local.syncRelay(context.Background(), wsURL(server), result.boundaries)
	if err != nil || again.stored != 0 {
		t.Errorf("a second sync must find nothing new, got %d stored, %v", again.stored, err)
	}
}

func TestSyncRelayFailures(t *testing.T) {
	local := newTestCrawler(t)

	_, err := local.syncRelay(context.Background(), fakeRelay(t, []string{`["NOTICE","ERROR: bad msg: negentropy disabled"]`}), nil)
	var connect *connectError
	if err == nil || errors.As(err, &connect) {
		t.Errorf("a refusal must fail the sync without being a connection error, got %v", err)
	}

	closed := httptest.NewServer(http.NotFoundHandler())
	url := wsURL(closed)
	closed.Close()
	if _, err := local.syncRelay(context.Background(), url, nil); !errors.As(err, &connect) {
		t.Errorf("expected a connection error, got %v", err)
	}
}

func newTestCrawler(t *testing.T) *Crawler {
	t.Helper()
	store := &lmdb.LMDBBackend{Path: t.TempDir()}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return &Crawler{store: store, relay: indexer.New(indexer.Config{MaxLimit: 100}, store), client: http.DefaultClient, log: log.New(io.Discard, "", 0)}
}

func mustAdd(t *testing.T, c *Crawler, event nostr.Event) {
	t.Helper()
	if _, err := c.relay.AddEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
}
