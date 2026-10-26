package crawler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
			status, detail := probeNIP77(context.Background(), http.DefaultClient, url)
			if status != test.wantStatus || detail != test.wantDetail {
				t.Fatalf("got %q %q, want %q %q", status, detail, test.wantStatus, test.wantDetail)
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
	server := httptest.NewServer(indexer.New(indexer.Config{MaxLimit: 10}, store))
	defer server.Close()

	if status, detail := probeNIP77(context.Background(), http.DefaultClient, wsURL(server)); status != registry.NIP77Supported {
		t.Fatalf("got %q %q, want supported", status, detail)
	}
}

func TestProbeNIP77SilentRelayIsUnsupported(t *testing.T) {
	url := fakeRelay(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	if status, _ := probeNIP77(ctx, http.DefaultClient, url); status != registry.NIP77Unsupported {
		t.Fatalf("got %q, want unsupported", status)
	}
}

func TestProbeNIP77Unreachable(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	url := wsURL(server)
	server.Close()

	if status, _ := probeNIP77(context.Background(), http.DefaultClient, url); status != registry.NIP77Unreachable {
		t.Fatalf("got %q, want unreachable", status)
	}
}

func TestProbeNIP77Forbidden(t *testing.T) {
	url := fakeRelay(t, nil)

	if status, _ := probeNIP77(context.Background(), newGuardedClient(), url); status != registry.NIP77Forbidden {
		t.Fatalf("got %q, want forbidden", status)
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
