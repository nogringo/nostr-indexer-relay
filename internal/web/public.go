package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip19"
)

// Counting every list takes a while on a full index, so a public page does it at most this often.
const publicStatsMaxAge = time.Minute

var relayListsFilter = nostr.Filter{Kinds: []nostr.Kind{nostr.KindRelayListMetadata}}

type publicStats struct {
	at    time.Time
	lists int
}

// requireVisitors serves the public pages only while the admin keeps them on.
func (s *Site) requireVisitors(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.Registry.Settings().Visitors {
			http.NotFound(w, r)
			return
		}
		next(w, r)
	}
}

func (s *Site) publicPage(title string) page {
	return page{Relay: s.Name, Title: title, Public: true}
}

type homeData struct {
	page
	Query    string
	Error    string
	RelayURL string
	Lists    int
}

func (s *Site) home(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		s.renderHome(w, r, http.StatusOK, "", "")
		return
	}
	if pubkey, err := parsePubKey(query); err == nil {
		http.Redirect(w, r, "/p/"+nip19.EncodeNpub(pubkey), http.StatusSeeOther)
		return
	}
	message := "Enter an npub, a hex pubkey or a NIP-05 address like name@example.com."
	if looksLikeNIP05(query) {
		message = "NIP-05 addresses are looked up by your browser, which needs JavaScript for it. Search by npub or hex pubkey instead."
	}
	s.renderHome(w, r, http.StatusBadRequest, query, message)
}

func (s *Site) renderHome(w http.ResponseWriter, r *http.Request, status int, query, message string) {
	stats, err := s.publicStats()
	if err != nil {
		http.Error(w, "failed to count the stored lists: "+err.Error(), http.StatusInternalServerError)
		return
	}
	data := homeData{page: s.publicPage("Look up a relay list"), Query: query, Error: message, RelayURL: s.relayURL(r), Lists: stats.lists}
	s.render(w, status, "home", data)
}

func (s *Site) publicStats() (publicStats, error) {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	if time.Since(s.stats.at) < publicStatsMaxAge {
		return s.stats, nil
	}

	lists, err := s.Store.CountEvents(relayListsFilter)
	if err != nil {
		return publicStats{}, err
	}
	s.stats = publicStats{at: time.Now(), lists: int(lists)}
	return s.stats, nil
}

// relayURL is the address to give to clients: RELAY_URL when set, or the one this page was reached at.
func (s *Site) relayURL(r *http.Request) string {
	if s.PublicURL != "" {
		return s.PublicURL
	}
	if isHTTPS(r) {
		return "wss://" + r.Host + "/"
	}
	return "ws://" + r.Host + "/"
}

type profileData struct {
	page
	Npub      string
	Hex       string
	Found     bool
	Published time.Time
	Relays    []listedRelay
	Event     string
}

type listedRelay struct {
	URL         string
	Read, Write bool
}

func (s *Site) profile(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("pubkey")
	pubkey, err := parsePubKey(key)
	if err != nil {
		s.renderHome(w, r, http.StatusNotFound, key, "This address does not name a pubkey. Enter an npub, a hex pubkey or a NIP-05 address.")
		return
	}
	npub := nip19.EncodeNpub(pubkey)
	if key != npub {
		http.Redirect(w, r, "/p/"+npub, http.StatusMovedPermanently)
		return
	}

	data := profileData{page: s.publicPage("Relay list of " + npub[:12] + "..." + npub[len(npub)-4:]), Npub: npub, Hex: pubkey.Hex()}
	filter := nostr.Filter{Kinds: relayListsFilter.Kinds, Authors: []nostr.PubKey{pubkey}}
	for event := range s.Store.QueryEvents(filter, 1) {
		data.Found, data.Published, data.Relays = true, event.CreatedAt.Time().UTC(), listedRelays(event)
		raw, err := json.MarshalIndent(event, "", "  ")
		if err == nil {
			data.Event = string(raw)
		}
	}
	s.render(w, http.StatusOK, "profile", data)
}

// listedRelays reads the r tags of a NIP-65 list. A relay without a marker is read and written.
func listedRelays(event nostr.Event) []listedRelay {
	var relays []listedRelay
	for tag := range event.Tags.FindAll("r") {
		relay := listedRelay{URL: tag[1], Read: true, Write: true}
		if len(tag) >= 3 {
			switch tag[2] {
			case "read":
				relay.Write = false
			case "write":
				relay.Read = false
			}
		}
		relays = append(relays, relay)
	}
	return relays
}

// parsePubKey accepts a hex pubkey, an npub or an nprofile, with or without the nostr: prefix of NIP-21.
func parsePubKey(raw string) (nostr.PubKey, error) {
	raw = strings.TrimPrefix(strings.TrimSpace(raw), "nostr:")
	if len(raw) == 64 {
		return nostr.PubKeyFromHex(strings.ToLower(raw))
	}
	prefix, value, err := nip19.Decode(raw)
	if err != nil {
		return nostr.PubKey{}, err
	}
	switch value := value.(type) {
	case nostr.PubKey:
		return value, nil
	case nostr.ProfilePointer:
		return value.PublicKey, nil
	}
	return nostr.PubKey{}, fmt.Errorf("%s does not name a pubkey", prefix)
}

func looksLikeNIP05(query string) bool {
	return !strings.ContainsAny(query, " /") && (strings.Contains(query, "@") || strings.Contains(query, "."))
}

func robots(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, "User-agent: *\nDisallow: /admin\nDisallow: /p/\n")
}
