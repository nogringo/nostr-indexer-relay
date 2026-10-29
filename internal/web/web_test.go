package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/lmdb"

	"nostr-indexer-relay/internal/registry"
)

var (
	skA = nostr.MustSecretKeyFromHex("0000000000000000000000000000000000000000000000000000000000000001")
	skB = nostr.MustSecretKeyFromHex("0000000000000000000000000000000000000000000000000000000000000002")
)

func TestSetupMakesTheFirstSignerTheAdmin(t *testing.T) {
	ta := newTestAdmin(t, nil)
	link, ok := ta.SetupLink(ta.server.URL)
	if !ok {
		t.Fatal("expected a setup link while there is no admin")
	}
	setup, _ := url.Parse(link)
	token := setup.Query().Get("token")

	if status, body := ta.get(t, setup.RequestURI()); status != http.StatusOK || !strings.Contains(body, "Sign with my Nostr extension") {
		t.Fatalf("expected the setup page, got %d %s", status, body)
	}
	if status, _ := ta.postLogin(t, ta.loginEvent(t, skA, nostr.Now()-1), "wrong token"); status != http.StatusUnauthorized {
		t.Fatalf("expected a wrong token to be refused, got %d", status)
	}
	if status, body := ta.postLogin(t, ta.loginEvent(t, skA, nostr.Now()), token); status != http.StatusNoContent {
		t.Fatalf("expected the first signer to become the admin, got %d %s", status, body)
	}
	if admin, ok := ta.Registry.Admin(); !ok || admin != skA.Public() {
		t.Fatalf("expected the admin to be stored, got %v %v", admin, ok)
	}
	if status, body := ta.get(t, "/admin"); status != http.StatusOK || !strings.Contains(body, "pubkeys indexed") {
		t.Fatalf("expected the overview for the new admin, got %d", status)
	}

	if status, _ := ta.postLogin(t, ta.loginEvent(t, skB, nostr.Now()), token); status != http.StatusForbidden {
		t.Errorf("expected the setup link to stop working once used, got %d", status)
	}
	if _, ok := ta.SetupLink(ta.server.URL); ok {
		t.Error("expected no setup link once the relay has an admin")
	}
}

func TestLoginChecksTheEvent(t *testing.T) {
	admin := skA.Public()
	ta := newTestAdmin(t, &admin)
	now := nostr.Now()

	otherPage := nostr.Event{Kind: loginKind, CreatedAt: now, Tags: nostr.Tags{{"u", "https://evil.example.com" + loginPath}, {"method", "POST"}}}
	otherPage.Sign(skA)
	otherKind := ta.loginEvent(t, skA, now)
	otherKind.Kind = nostr.KindTextNote
	otherKind.Sign(skA)
	badSignature := ta.loginEvent(t, skA, now)
	badSignature.Sig[0] ^= 0xff

	for _, test := range []struct {
		name  string
		event nostr.Event
		want  int
	}{
		{"another key", ta.loginEvent(t, skB, now), http.StatusForbidden},
		{"stale", ta.loginEvent(t, skA, now-120), http.StatusUnauthorized},
		{"signed for another page", otherPage, http.StatusUnauthorized},
		{"another kind", otherKind, http.StatusUnauthorized},
		{"bad signature", badSignature, http.StatusUnauthorized},
	} {
		if status, body := ta.postLogin(t, test.event, ""); status != test.want {
			t.Errorf("%s: got %d %s, want %d", test.name, status, body, test.want)
		}
	}

	valid := ta.loginEvent(t, skA, now)
	if status, body := ta.postLogin(t, valid, ""); status != http.StatusNoContent {
		t.Fatalf("expected the admin to sign in, got %d %s", status, body)
	}
	if status, _ := ta.postLogin(t, valid, ""); status != http.StatusUnauthorized {
		t.Errorf("expected a login event to work once, got %d", status)
	}
}

func TestLoginAcceptsThePublicHostBehindAProxy(t *testing.T) {
	admin := skA.Public()
	ta := newTestAdmin(t, &admin)
	ta.PublicURL = "wss://indexer.example.com/"

	event := nostr.Event{Kind: loginKind, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"u", "https://indexer.example.com" + loginPath}, {"method", "POST"}}}
	event.Sign(skA)
	body, _ := json.Marshal(map[string]any{"event": event})
	if status, text := ta.do(t, http.MethodPost, loginPath, "application/json", bytes.NewReader(body), "https://indexer.example.com"); status != http.StatusNoContent {
		t.Fatalf("expected the public host to be accepted, got %d %s", status, text)
	}
}

func TestAdminPagesNeedASession(t *testing.T) {
	admin := skA.Public()
	ta := newTestAdmin(t, &admin)

	if status, body := ta.get(t, "/admin/settings"); status != http.StatusUnauthorized || !strings.Contains(body, "Sign in to Test relay") {
		t.Errorf("expected the sign-in page, got %d", status)
	}
	if status, _ := ta.postForm(t, "/admin/settings", url.Values{"visitors": {"on"}}, ""); status != http.StatusUnauthorized {
		t.Errorf("expected a post without session to be refused, got %d", status)
	}

	forged := fmt.Sprintf("%s.%d.%064x", admin.Hex(), time.Now().Add(time.Hour).Unix(), 0)
	serverURL, _ := url.Parse(ta.server.URL)
	ta.client.Jar.SetCookies(serverURL, []*http.Cookie{{Name: sessionCookie, Value: forged, Path: "/admin"}})
	if status, _ := ta.get(t, "/admin"); status != http.StatusUnauthorized {
		t.Errorf("expected a forged session to be refused, got %d", status)
	}
}

func TestSaveSettings(t *testing.T) {
	ta := newSignedInAdmin(t)

	if status, _ := ta.postForm(t, "/admin/settings", url.Values{}, ""); status != http.StatusSeeOther || ta.Registry.Settings().Visitors {
		t.Errorf("expected the unchecked box to turn the public page off, got %d", status)
	}
	if status, _ := ta.postForm(t, "/admin/settings", url.Values{"visitors": {"on"}}, ""); status != http.StatusSeeOther || !ta.Registry.Settings().Visitors {
		t.Errorf("expected the checked box to turn the public page on, got %d", status)
	}
	if status, _ := ta.postForm(t, "/admin/settings", url.Values{}, "https://evil.example.com"); status != http.StatusForbidden || !ta.Registry.Settings().Visitors {
		t.Errorf("expected a post from another site to be refused, got %d", status)
	}
}

func TestPagesRender(t *testing.T) {
	ta := newSignedInAdmin(t)
	ta.Registry.CountStored(time.Now())

	for _, test := range []struct {
		path string
		want []string
	}{
		{"/admin", []string{"pubkeys indexed", "Show the numbers"}},
		{"/admin/settings", []string{"Public search page", "checked", "Save settings", "npub1"}},
		{"/static/admin.js", []string{"signEvent"}},
	} {
		status, body := ta.get(t, test.path)
		if status != http.StatusOK {
			t.Errorf("%s: got %d", test.path, status)
			continue
		}
		for _, want := range test.want {
			if !strings.Contains(body, want) {
				t.Errorf("%s: expected %q in the page", test.path, want)
			}
		}
	}
}

func TestBaseURL(t *testing.T) {
	for _, test := range []struct{ relayURL, addr, want string }{
		{"wss://indexer.example.com/", ":3334", "https://indexer.example.com"},
		{"ws://192.168.1.2:3334", ":3334", "http://192.168.1.2:3334"},
		{"", ":3334", "http://localhost:3334"},
		{"", "127.0.0.1:8080", "http://127.0.0.1:8080"},
	} {
		if got := BaseURL(test.relayURL, test.addr); got != test.want {
			t.Errorf("BaseURL(%q, %q) = %q, want %q", test.relayURL, test.addr, got, test.want)
		}
	}
}

type testAdmin struct {
	*Site
	server *httptest.Server
	client *http.Client
}

func newTestAdmin(t *testing.T, fixedAdmin *nostr.PubKey) *testAdmin {
	t.Helper()
	store := &lmdb.LMDBBackend{Path: t.TempDir()}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	state, err := registry.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(state.Close)

	ta := &testAdmin{}
	ta.Site = New(Config{
		Name:        "Test relay",
		Store:       store,
		Registry:    state,
		AdminPubKey: fixedAdmin,
		Log:         log.New(io.Discard, "", 0),
	})
	mux := http.NewServeMux()
	ta.Register(mux)
	ta.server = httptest.NewServer(mux)
	t.Cleanup(ta.server.Close)
	jar, _ := cookiejar.New(nil)
	ta.client = &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return ta
}

func newSignedInAdmin(t *testing.T) *testAdmin {
	t.Helper()
	admin := skA.Public()
	ta := newTestAdmin(t, &admin)
	if status, body := ta.postLogin(t, ta.loginEvent(t, skA, nostr.Now()), ""); status != http.StatusNoContent {
		t.Fatalf("sign in failed: %d %s", status, body)
	}
	return ta
}

func (ta *testAdmin) loginEvent(t *testing.T, sk nostr.SecretKey, createdAt nostr.Timestamp) nostr.Event {
	t.Helper()
	event := nostr.Event{Kind: loginKind, CreatedAt: createdAt, Tags: nostr.Tags{{"u", ta.server.URL + loginPath}, {"method", "POST"}}}
	if err := event.Sign(sk); err != nil {
		t.Fatal(err)
	}
	return event
}

func (ta *testAdmin) postLogin(t *testing.T, event nostr.Event, token string) (int, string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"event": event, "token": token})
	if err != nil {
		t.Fatal(err)
	}
	return ta.do(t, http.MethodPost, loginPath, "application/json", bytes.NewReader(body), "")
}

func (ta *testAdmin) get(t *testing.T, path string) (int, string) {
	t.Helper()
	return ta.do(t, http.MethodGet, path, "", nil, "")
}

func (ta *testAdmin) postForm(t *testing.T, path string, form url.Values, origin string) (int, string) {
	t.Helper()
	return ta.do(t, http.MethodPost, path, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()), origin)
}

func (ta *testAdmin) do(t *testing.T, method, path, contentType string, body io.Reader, origin string) (int, string) {
	t.Helper()
	response := ta.send(t, method, path, contentType, body, origin)
	defer response.Body.Close()
	read, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, string(read)
}

func (ta *testAdmin) send(t *testing.T, method, path, contentType string, body io.Reader, origin string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, ta.server.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	response, err := ta.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
