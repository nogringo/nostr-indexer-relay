package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip19"
)

const (
	sessionCookie   = "admin_session"
	sessionDuration = 30 * 24 * time.Hour
	// NIP-98 HTTP auth: the event names the login URL and method, and must be fresh.
	loginKind   nostr.Kind = 27235
	loginMaxAge            = time.Minute
	loginPath              = "/admin/login"
)

func (a *Admin) admin() (nostr.PubKey, bool) {
	if a.AdminPubKey != nil {
		return *a.AdminPubKey, true
	}
	return a.Registry.Admin()
}

// SetupLink returns a one-time link that makes whoever signs on it the admin, when the relay
// has none yet. Each call replaces the previous link.
func (a *Admin) SetupLink(base string) (string, bool) {
	if _, ok := a.admin(); ok {
		return "", false
	}
	token := make([]byte, 16)
	rand.Read(token)
	a.mu.Lock()
	a.setupToken = hex.EncodeToString(token)
	defer a.mu.Unlock()
	return strings.TrimSuffix(base, "/") + "/admin/setup?token=" + a.setupToken, true
}

// BaseURL returns the HTTP address of the relay, taken from its public URL when one is set.
func BaseURL(relayURL, addr string) string {
	if u, err := url.Parse(relayURL); err == nil && u.Host != "" {
		if u.Scheme == "ws" || u.Scheme == "http" {
			return "http://" + u.Host
		}
		return "https://" + u.Host
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://localhost"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func (a *Admin) tokenMatches(token string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.setupToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(a.setupToken)) == 1
}

func (a *Admin) consumeToken(token string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.setupToken == "" || subtle.ConstantTimeCompare([]byte(token), []byte(a.setupToken)) != 1 {
		return false
	}
	a.setupToken = ""
	return true
}

type loginData struct {
	page
	Setup      bool
	HasAdmin   bool
	Token      string
	TokenValid bool
}

func (a *Admin) setup(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	_, hasAdmin := a.admin()
	data := loginData{page: a.page("Set up", ""), Setup: true, HasAdmin: hasAdmin, Token: token, TokenValid: !hasAdmin && a.tokenMatches(token)}
	a.render(w, http.StatusOK, "login", data)
}

func (a *Admin) login(w http.ResponseWriter, r *http.Request) {
	if !a.sameOrigin(r) {
		http.Error(w, "cross-origin request refused", http.StatusForbidden)
		return
	}
	var body struct {
		Event nostr.Event `json:"event"`
		Token string      `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil {
		http.Error(w, "invalid login request", http.StatusBadRequest)
		return
	}
	now := time.Now()
	if err := a.checkLoginEvent(body.Event, r, now); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	pubkey := body.Event.PubKey
	admin, hasAdmin := a.admin()
	switch {
	case hasAdmin && pubkey == admin:
	case hasAdmin:
		http.Error(w, "This key is not the admin of this relay.", http.StatusForbidden)
		return
	case !a.consumeToken(body.Token):
		http.Error(w, "This setup link is no longer valid. Restart the relay to get a new one in its log.", http.StatusUnauthorized)
		return
	default:
		if err := a.Registry.SetAdmin(pubkey); err != nil {
			http.Error(w, "failed to store the admin: "+err.Error(), http.StatusInternalServerError)
			return
		}
		a.Log.Printf("%s is now the admin", nip19.EncodeNpub(pubkey))
	}

	http.SetCookie(w, a.sessionCookie(pubkey, now, isHTTPS(r)))
	w.WriteHeader(http.StatusNoContent)
}

// checkLoginEvent accepts a NIP-98 event signed for our login URL in the last minute, once.
func (a *Admin) checkLoginEvent(event nostr.Event, r *http.Request, now time.Time) error {
	if event.Kind != loginKind {
		return errors.New("The login event must be a NIP-98 event, kind 27235.")
	}
	// VerifySignature trusts the announced id, so the id is recomputed first.
	if !event.CheckID() || !event.VerifySignature() {
		return errors.New("The login event has an invalid signature.")
	}
	if age := now.Sub(event.CreatedAt.Time()); age > loginMaxAge || age < -loginMaxAge {
		return errors.New("The login event is more than a minute off. Check your computer's clock.")
	}
	u, err := url.Parse(tagValue(event, "u"))
	if err != nil || !a.ownHost(r, u.Host) || u.Path != loginPath || !strings.EqualFold(tagValue(event, "method"), http.MethodPost) {
		return errors.New("The login event was signed for another page.")
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	for id, usedAt := range a.usedLogins {
		if now.Sub(usedAt) > 2*loginMaxAge {
			delete(a.usedLogins, id)
		}
	}
	if _, used := a.usedLogins[event.ID]; used {
		return errors.New("This login event was already used.")
	}
	a.usedLogins[event.ID] = now
	return nil
}

func tagValue(event nostr.Event, key string) string {
	if tag := event.Tags.Find(key); len(tag) >= 2 {
		return tag[1]
	}
	return ""
}

func (a *Admin) logout(w http.ResponseWriter, r *http.Request) {
	if !a.sameOrigin(r) {
		http.Error(w, "cross-origin request refused", http.StatusForbidden)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/admin", MaxAge: -1, HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

// The session cookie holds the admin pubkey and an expiry, signed with a key kept in the registry.
func (a *Admin) sessionCookie(pubkey nostr.PubKey, now time.Time, secure bool) *http.Cookie {
	expires := now.Add(sessionDuration)
	value := pubkey.Hex() + "." + strconv.FormatInt(expires.Unix(), 10)
	return &http.Cookie{
		Name:     sessionCookie,
		Value:    value + "." + a.sign(value),
		Path:     "/admin",
		Expires:  expires,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	}
}

func (a *Admin) sign(value string) string {
	mac := hmac.New(sha256.New, a.Registry.SessionSecret())
	mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}

func (a *Admin) signedIn(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	cut := strings.LastIndexByte(cookie.Value, '.')
	if cut < 0 || !hmac.Equal([]byte(cookie.Value[cut+1:]), []byte(a.sign(cookie.Value[:cut]))) {
		return false
	}
	pubkey, expiry, _ := strings.Cut(cookie.Value[:cut], ".")
	expires, err := strconv.ParseInt(expiry, 10, 64)
	if err != nil || time.Now().Unix() > expires {
		return false
	}
	admin, ok := a.admin()
	return ok && admin.Hex() == pubkey
}

func (a *Admin) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && !a.sameOrigin(r) {
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return
		}
		if a.signedIn(r) {
			next(w, r)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "sign in first", http.StatusUnauthorized)
			return
		}
		_, hasAdmin := a.admin()
		a.render(w, http.StatusUnauthorized, "login", loginData{page: a.page("Sign in", ""), HasAdmin: hasAdmin})
	}
}

// sameOrigin refuses requests that a browser sent from another site. The session cookie is
// SameSite=Strict as well; this covers browsers that ignore it.
func (a *Admin) sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && a.ownHost(r, u.Host)
}

func (a *Admin) ownHost(r *http.Request, host string) bool {
	if host == r.Host {
		return true
	}
	public, err := url.Parse(a.PublicURL)
	return err == nil && public.Host != "" && host == public.Host
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}
