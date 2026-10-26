package crawler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/lmdb"
	"github.com/coder/websocket"

	"nostr-indexer-relay/internal/registry"
	indexer "nostr-indexer-relay/internal/relay"
)

func TestProbeNIP77(t *testing.T) {
	tests := []struct {
		name       string
		answers    []string
		wantStatus registry.NIP77Status
		wantDetail string
	}{
		{
			name:       "notice",
			answers:    []string{`["NOTICE","ERROR: bad msg: negentropy disabled"]`},
			wantStatus: registry.NIP77Unsupported,
			wantDetail: "ERROR: bad msg: negentropy disabled",
		},
		{
			name:       "neg-err after an unrelated auth",
			answers:    []string{`["AUTH","challenge"]`, `["NEG-ERR","nip77-test","blocked: too many query results"]`},
			wantStatus: registry.NIP77Supported,
			wantDetail: "blocked: too many query results",
		},
		{
			name:       "khatru neg-error",
			answers:    []string{`["NEG-ERROR","nip77-test","blocked: too many query results"]`},
			wantStatus: registry.NIP77Supported,
			wantDetail: "blocked: too many query results",
		},
		{
			name:       "closed",
			answers:    []string{`["CLOSED","nip77-test","unsupported: no negentropy"]`},
			wantStatus: registry.NIP77Unsupported,
			wantDetail: "unsupported: no negentropy",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			url := fakeRelay(t, test.answers)
			result := probeNIP77(context.Background(), http.DefaultClient, url, false)
			if result.status != test.wantStatus || result.detail != test.wantDetail {
				t.Fatalf("got %q %q, want %q %q", result.status, result.detail, test.wantStatus, test.wantDetail)
			}
		})
	}
}

func TestProbeNIP77OnOurOwnRelay(t *testing.T) {
	store := &lmdb.LMDBBackend{Path: t.TempDir()}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for range 3 {
		if err := store.SaveEvent(signedList(t, nostr.Generate(), nostr.Now(), "wss://relay.example.com")); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(indexer.New(indexer.Config{MaxLimit: 10}, store))
	defer server.Close()

	if result := probeNIP77(context.Background(), http.DefaultClient, wsURL(server), true); result.status != registry.NIP77Supported || !result.counted || result.lists != 3 {
		t.Fatalf("expected a supported relay counting 3 lists, got %+v", result)
	}
	if result := probeNIP77(context.Background(), http.DefaultClient, wsURL(server), false); result.counted {
		t.Fatalf("expected no count when none was asked for, got %+v", result)
	}
}

func TestProbeNIP77WithoutCount(t *testing.T) {
	url := fakeRelay(t, []string{`["NEG-MSG","nip77-test","61"]`, `["CLOSED","count","unsupported: this relay does not support NIP-45"]`})

	if result := probeNIP77(context.Background(), http.DefaultClient, url, true); result.status != registry.NIP77Supported || result.counted {
		t.Fatalf("expected a supported relay without a count, got %+v", result)
	}
}

func TestProbeNIP77SilentRelayIsUnsupported(t *testing.T) {
	url := fakeRelay(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	if result := probeNIP77(ctx, http.DefaultClient, url, false); result.status != registry.NIP77Unsupported {
		t.Fatalf("got %q, want unsupported", result.status)
	}
}

func TestProbeNIP77Unreachable(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	url := wsURL(server)
	server.Close()

	if result := probeNIP77(context.Background(), http.DefaultClient, url, false); result.status != registry.NIP77Unreachable {
		t.Fatalf("got %q, want unreachable", result.status)
	}
}

func TestProbeNIP77Forbidden(t *testing.T) {
	url := fakeRelay(t, nil)

	if result := probeNIP77(context.Background(), newGuardedClient(), url, false); result.status != registry.NIP77Forbidden {
		t.Fatalf("got %q, want forbidden", result.status)
	}
}

// fakeRelay answers the first message it receives with the given messages, then stays silent.
func fakeRelay(t *testing.T, answers []string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		if _, _, err := conn.Read(r.Context()); err != nil {
			return
		}
		for _, answer := range answers {
			if err := conn.Write(r.Context(), websocket.MessageText, []byte(answer)); err != nil {
				return
			}
		}
		conn.Read(r.Context())
	}))
	t.Cleanup(server.Close)
	return wsURL(server)
}

func wsURL(server *httptest.Server) string {
	return "ws" + strings.TrimPrefix(server.URL, "http")
}
