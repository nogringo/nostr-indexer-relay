package web

import (
	"bytes"
	"embed"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore"
	"fiatjaf.com/nostr/nip19"

	"nostr-indexer-relay/internal/registry"
)

//go:embed templates static
var files embed.FS

var pages = parsePages("login", "overview", "settings", "home", "profile")

func parsePages(names ...string) map[string]*template.Template {
	funcs := template.FuncMap{"number": number, "ago": ago}
	pages := make(map[string]*template.Template)
	for _, name := range names {
		pages[name] = template.Must(template.New("").Funcs(funcs).ParseFS(files, "templates/layout.html", "templates/"+name+".html"))
	}
	return pages
}

type Config struct {
	Name string
	// RELAY_URL. Its host is accepted besides the request's, for proxies that rewrite the Host header.
	PublicURL string
	Store     eventstore.Store
	Registry  *registry.Registry
	// Set by RELAY_ADMIN_PUBKEY, it takes precedence over the admin stored in the registry.
	AdminPubKey *nostr.PubKey
	Log         *log.Logger
}

// Site serves the visitor pages at the root and the admin interface under /admin.
type Site struct {
	Config

	mu         sync.Mutex
	setupToken string
	usedLogins map[nostr.ID]time.Time

	statsMu sync.Mutex
	stats   publicStats
}

func New(cfg Config) *Site {
	return &Site{Config: cfg, usedLogins: make(map[nostr.ID]time.Time)}
}

func (s *Site) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin", s.requireAdmin(s.overview))
	mux.Handle("GET /admin/{$}", http.RedirectHandler("/admin", http.StatusMovedPermanently))
	mux.HandleFunc("GET /admin/settings", s.requireAdmin(s.settings))
	mux.HandleFunc("POST /admin/settings", s.requireAdmin(s.saveSettings))
	mux.HandleFunc("GET /admin/setup", s.setup)
	mux.HandleFunc("POST "+loginPath, s.login)
	mux.HandleFunc("POST /admin/logout", s.logout)
	mux.HandleFunc("GET /{$}", s.requireVisitors(s.home))
	mux.HandleFunc("GET /p/{pubkey}", s.requireVisitors(s.profile))
	mux.HandleFunc("GET /robots.txt", robots)
	static, _ := fs.Sub(files, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
}

type page struct {
	Relay    string
	Title    string
	Nav      string
	SignedIn bool
	Public   bool
}

func (s *Site) page(title, nav string) page {
	return page{Relay: s.Name, Title: title, Nav: nav, SignedIn: nav != ""}
}

const (
	adminPolicy = "default-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
	// NIP-05 addresses are fetched by the visitor's browser, from any https origin.
	publicPolicy = "default-src 'self'; connect-src 'self' https:; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
)

func (s *Site) render(w http.ResponseWriter, status int, name string, data any) {
	policy := adminPolicy
	if name == "home" || name == "profile" {
		policy = publicPolicy
	}
	var buf bytes.Buffer
	if err := pages[name].ExecuteTemplate(&buf, "layout", data); err != nil {
		s.Log.Printf("failed to render the %s page: %v", name, err)
		http.Error(w, "the page could not be rendered", http.StatusInternalServerError)
		return
	}
	header := w.Header()
	header.Set("Content-Type", "text/html; charset=utf-8")
	header.Set("Content-Security-Policy", policy)
	header.Set("Referrer-Policy", "same-origin")
	header.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

const chartDays = 30

type overviewData struct {
	page
	Lists int
	Chart chart
}

func (s *Site) overview(w http.ResponseWriter, r *http.Request) {
	lists, err := s.Store.CountEvents(nostr.Filter{Kinds: []nostr.Kind{nostr.KindRelayListMetadata}})
	if err != nil {
		http.Error(w, "failed to count the stored lists: "+err.Error(), http.StatusInternalServerError)
		return
	}
	data := overviewData{page: s.page("Overview", "overview"), Lists: int(lists), Chart: newChart(s.Registry.StoredPerDay(time.Now(), chartDays))}
	s.render(w, http.StatusOK, "overview", data)
}

type settingsData struct {
	page
	registry.Settings
	Admin        string
	AdminFromEnv bool
}

func (s *Site) settings(w http.ResponseWriter, r *http.Request) {
	data := settingsData{page: s.page("Settings", "settings"), Settings: s.Registry.Settings(), AdminFromEnv: s.AdminPubKey != nil}
	if admin, ok := s.admin(); ok {
		data.Admin = nip19.EncodeNpub(admin)
	}
	s.render(w, http.StatusOK, "settings", data)
}

func (s *Site) saveSettings(w http.ResponseWriter, r *http.Request) {
	settings := s.Registry.Settings()
	settings.Visitors = r.PostFormValue("visitors") != ""
	if err := s.Registry.SetSettings(settings); err != nil {
		http.Error(w, "failed to save the settings: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.Log.Printf("admin: settings changed")
	http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
}

func number(n int) string {
	if n < 0 {
		return "-" + number(-n)
	}
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	future := d < 0
	if future {
		d = -d
	}
	var span string
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		span = strconv.Itoa(int(d.Minutes())) + " min"
	case d < 48*time.Hour:
		span = strconv.Itoa(int(d.Hours())) + " h"
	default:
		span = strconv.Itoa(int(d.Hours()/24)) + " days"
	}
	if future {
		return "in " + span
	}
	return span + " ago"
}
