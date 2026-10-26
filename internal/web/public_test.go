package web

import (
	"net/http"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip19"

	"nostr-indexer-relay/internal/registry"
)

func TestHomeShowsTheStatsAndTheRelayURL(t *testing.T) {
	ta := newTestAdmin(t, nil)
	for range 3 {
		saveList(t, ta, nostr.Generate(), nostr.Tags{{"r", "wss://popular.example.com"}})
	}
	if err := ta.Registry.SetPopularity(map[string]int{"wss://popular.example.com": 3, "wss://rare.example.com": 1, "wss://banned.example.com": 2}, nil); err != nil {
		t.Fatal(err)
	}
	if err := ta.Registry.Update("wss://banned.example.com", func(r *registry.Relay) { r.Banned = true }); err != nil {
		t.Fatal(err)
	}

	response := ta.send(t, http.MethodGet, "/", "", nil, "")
	response.Body.Close()
	if policy := response.Header.Get("Content-Security-Policy"); !strings.Contains(policy, "connect-src 'self' https:") {
		t.Errorf("expected the public policy to allow NIP-05 lookups, got %q", policy)
	}
	status, body := ta.get(t, "/")
	if status != http.StatusOK {
		t.Fatalf("got %d", status)
	}
	for _, want := range []string{"3 relay lists indexed", "ws://" + strings.TrimPrefix(ta.server.URL, "http://") + "/", "100.0%", "33.3%"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q on the home page", want)
		}
	}
	if popular, rare := strings.Index(body, "wss://popular.example.com"), strings.Index(body, "wss://rare.example.com"); popular < 0 || rare < popular {
		t.Error("expected the most listed relays first")
	}
	if strings.Contains(body, "wss://banned.example.com") {
		t.Error("expected banned relays to stay out of the public stats")
	}
}

func TestHomeSearch(t *testing.T) {
	ta := newTestAdmin(t, nil)
	pubkey := nostr.Generate().Public()
	npub := nip19.EncodeNpub(pubkey)

	for _, query := range []string{npub, pubkey.Hex(), "nostr:" + nip19.EncodeNprofile(pubkey, []string{"wss://relay.example.com"})} {
		response := ta.send(t, http.MethodGet, "/?q="+query, "", nil, "")
		response.Body.Close()
		if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/p/"+npub {
			t.Errorf("%s: got %d to %q", query, response.StatusCode, response.Header.Get("Location"))
		}
	}
	for query, want := range map[string]string{
		"jack@example.com": "looked up by your browser",
		"not a key":        "Enter an npub",
	} {
		if status, body := ta.get(t, "/?q="+strings.ReplaceAll(query, " ", "+")); status != http.StatusBadRequest || !strings.Contains(body, want) {
			t.Errorf("%s: got %d, want %q in the page", query, status, want)
		}
	}
}

func TestProfileShowsTheRelayList(t *testing.T) {
	ta := newTestAdmin(t, nil)
	sk := nostr.Generate()
	saveList(t, ta, sk, nostr.Tags{{"r", "wss://both.example.com"}, {"r", "wss://inbox.example.com", "read"}, {"r", "wss://outbox.example.com", "write"}})
	npub := nip19.EncodeNpub(sk.Public())

	status, body := ta.get(t, "/p/"+npub)
	if status != http.StatusOK {
		t.Fatalf("got %d", status)
	}
	for _, want := range []string{npub, sk.Public().Hex(), "wss://both.example.com", "wss://inbox.example.com", "wss://outbox.example.com", "Published", "Show the event"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q on the profile page", want)
		}
	}
	if marks := strings.Count(body, "✓"); marks != 4 {
		t.Errorf("expected 4 read or write marks, got %d", marks)
	}

	response := ta.send(t, http.MethodGet, "/p/"+sk.Public().Hex(), "", nil, "")
	response.Body.Close()
	if response.StatusCode != http.StatusMovedPermanently || response.Header.Get("Location") != "/p/"+npub {
		t.Errorf("expected a hex address to redirect to the npub one, got %d %q", response.StatusCode, response.Header.Get("Location"))
	}
	if _, body := ta.get(t, "/p/"+nip19.EncodeNpub(nostr.Generate().Public())); !strings.Contains(body, "No relay list is known") {
		t.Error("expected an unknown pubkey to say so")
	}
	if status, _ := ta.get(t, "/p/garbage"); status != http.StatusNotFound {
		t.Errorf("expected an invalid pubkey to be not found, got %d", status)
	}
}

func TestVisitorPagesCanBeTurnedOff(t *testing.T) {
	ta := newTestAdmin(t, nil)
	settings := ta.Registry.Settings()
	settings.Visitors = false
	if err := ta.Registry.SetSettings(settings); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/", "/p/" + nip19.EncodeNpub(nostr.Generate().Public())} {
		if status, _ := ta.get(t, path); status != http.StatusNotFound {
			t.Errorf("%s: expected not found, got %d", path, status)
		}
	}
	if status, body := ta.get(t, "/admin"); status != http.StatusUnauthorized || !strings.Contains(body, "Sign in") {
		t.Errorf("expected the admin to keep working, got %d", status)
	}
	if _, body := ta.get(t, "/robots.txt"); !strings.Contains(body, "Disallow: /p/") {
		t.Errorf("unexpected robots.txt %q", body)
	}
}

func saveList(t *testing.T, ta *testAdmin, sk nostr.SecretKey, tags nostr.Tags) {
	t.Helper()
	event := nostr.Event{Kind: nostr.KindRelayListMetadata, CreatedAt: nostr.Now(), Tags: tags}
	if err := event.Sign(sk); err != nil {
		t.Fatal(err)
	}
	if err := ta.Store.SaveEvent(event); err != nil {
		t.Fatal(err)
	}
}
