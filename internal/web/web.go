package web

import (
	"bytes"
	"cmp"
	"embed"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore"
	"fiatjaf.com/nostr/nip19"

	"nostr-indexer-relay/internal/registry"
)

//go:embed templates static
var files embed.FS

var pages = parsePages("login", "overview", "relays", "settings", "home", "profile")

func parsePages(names ...string) map[string]*template.Template {
	funcs := template.FuncMap{"number": number, "ago": ago, "status": status, "failures": failures}
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
	// Called once the settings changed, so that the crawler applies them right away.
	SettingsChanged func()
	Log             *log.Logger
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
	mux.HandleFunc("GET /admin/relays", s.requireAdmin(s.relays))
	mux.HandleFunc("POST /admin/relays", s.requireAdmin(s.relayAction))
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
	Lists     int
	Tracked   int
	Supported int
	Live      int
	Indexers  []registry.Relay
	Chart     chart
}

func (s *Site) overview(w http.ResponseWriter, r *http.Request) {
	lists, err := s.Store.CountEvents(nostr.Filter{Kinds: []nostr.Kind{nostr.KindRelayListMetadata}})
	if err != nil {
		http.Error(w, "failed to count the stored lists: "+err.Error(), http.StatusInternalServerError)
		return
	}
	data := overviewData{page: s.page("Overview", "overview"), Lists: int(lists)}
	for _, relay := range s.Registry.All() {
		if !relay.Forgotten() {
			data.Tracked++
			if relay.NIP77 == registry.NIP77Supported {
				data.Supported++
			}
		}
		if relay.Live {
			data.Live++
		}
		if relay.Indexer {
			data.Indexers = append(data.Indexers, relay)
		}
	}
	slices.SortFunc(data.Indexers, func(a, b registry.Relay) int {
		return cmp.Or(cmp.Compare(b.Lists, a.Lists), strings.Compare(a.URL, b.URL))
	})
	data.Chart = newChart(s.Registry.StoredPerDay(time.Now(), chartDays))
	s.render(w, http.StatusOK, "overview", data)
}

const relaysPerPage = 100

type relayFilter struct {
	value, label string
	match        func(registry.Relay) bool
}

var relayFilters = []relayFilter{
	{"", "Tracked", func(r registry.Relay) bool { return !r.Forgotten() }},
	{"supported", "NIP-77 supported", func(r registry.Relay) bool { return r.NIP77 == registry.NIP77Supported }},
	{"indexers", "Indexers", func(r registry.Relay) bool { return r.Indexer }},
	{"live", "Live", func(r registry.Relay) bool { return r.Live }},
	{"aliases", "Aliases", func(r registry.Relay) bool { return r.AliasOf != "" }},
	{"untested", "Untested", func(r registry.Relay) bool { return r.NIP77 == registry.NIP77Unknown && !r.Forgotten() }},
	{"unsupported", "NIP-77 unsupported", func(r registry.Relay) bool { return r.NIP77 == registry.NIP77Unsupported }},
	{"unreachable", "Unreachable", func(r registry.Relay) bool { return r.NIP77 == registry.NIP77Unreachable }},
	{"forbidden", "Forbidden address", func(r registry.Relay) bool { return r.NIP77 == registry.NIP77Forbidden }},
	{"banned", "Banned", func(r registry.Relay) bool { return r.Banned }},
	{"all", "All, forgotten ones included", func(registry.Relay) bool { return true }},
}

type filterOption struct {
	Value, Label string
	Count        int
	Selected     bool
}

type relaysData struct {
	page
	Query   string
	Filters []filterOption
	Relays  []registry.Relay
	Total   int
	Page    int
	Pages   int
	PrevURL string
	NextURL string
	// Query string of this view, so that an action comes back to it.
	Return string
}

func (s *Site) relays(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	view := relaysView{status: query.Get("status"), search: strings.TrimSpace(query.Get("q"))}
	view.page, _ = strconv.Atoi(query.Get("page"))

	filter := relayFilters[0]
	for _, candidate := range relayFilters {
		if candidate.value == view.status {
			filter = candidate
		}
	}
	view.status = filter.value

	all := s.Registry.All()
	data := relaysData{page: s.page("Relays", "relays"), Query: view.search}
	for _, candidate := range relayFilters {
		option := filterOption{Value: candidate.value, Label: candidate.label, Selected: candidate.value == filter.value}
		for _, relay := range all {
			if candidate.match(relay) {
				option.Count++
			}
		}
		data.Filters = append(data.Filters, option)
	}

	search := strings.ToLower(view.search)
	matching := slices.DeleteFunc(all, func(relay registry.Relay) bool {
		return !filter.match(relay) || !strings.Contains(relay.URL, search)
	})
	slices.SortFunc(matching, func(a, b registry.Relay) int {
		if a.Seed != b.Seed {
			if a.Seed {
				return -1
			}
			return 1
		}
		return cmp.Or(cmp.Compare(b.Popularity, a.Popularity), strings.Compare(a.URL, b.URL))
	})

	data.Total = len(matching)
	data.Pages = max(1, (len(matching)+relaysPerPage-1)/relaysPerPage)
	view.page = min(max(view.page, 1), data.Pages)
	data.Page = view.page
	data.Relays = matching[(view.page-1)*relaysPerPage : min(view.page*relaysPerPage, len(matching))]
	data.Return = view.query(view.page)
	if view.page > 1 {
		data.PrevURL = "/admin/relays?" + view.query(view.page-1)
	}
	if view.page < data.Pages {
		data.NextURL = "/admin/relays?" + view.query(view.page+1)
	}
	s.render(w, http.StatusOK, "relays", data)
}

type relaysView struct {
	status, search string
	page           int
}

func (v relaysView) query(page int) string {
	values := url.Values{}
	if v.status != "" {
		values.Set("status", v.status)
	}
	if v.search != "" {
		values.Set("q", v.search)
	}
	if page > 1 {
		values.Set("page", strconv.Itoa(page))
	}
	return values.Encode()
}

var relayActions = map[string]func(*registry.Relay){
	"test":         func(r *registry.Relay) { r.NextTestAt = time.Time{} },
	"sync":         func(r *registry.Relay) { r.NextSyncAt = time.Time{} },
	"force-live":   func(r *registry.Relay) { r.ForceLive = true },
	"unforce-live": func(r *registry.Relay) { r.ForceLive = false },
	"ban":          func(r *registry.Relay) { r.Banned = true },
	"unban":        func(r *registry.Relay) { r.Banned = false },
}

func (s *Site) relayAction(w http.ResponseWriter, r *http.Request) {
	relayURL, action := r.PostFormValue("url"), r.PostFormValue("action")
	update, ok := relayActions[action]
	if !ok {
		http.Error(w, "unknown action "+action, http.StatusBadRequest)
		return
	}
	if _, ok := s.Registry.Get(relayURL); !ok {
		http.Error(w, "unknown relay "+relayURL, http.StatusNotFound)
		return
	}
	if err := s.Registry.Update(relayURL, update); err != nil {
		http.Error(w, "failed to update the relay: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.Log.Printf("admin: %s %s", action, relayURL)

	// Rebuilt from the known parameters only, so that the redirect stays on this page.
	back, _ := url.ParseQuery(r.PostFormValue("return"))
	page, _ := strconv.Atoi(back.Get("page"))
	view := relaysView{status: back.Get("status"), search: back.Get("q"), page: page}
	http.Redirect(w, r, "/admin/relays?"+view.query(view.page), http.StatusSeeOther)
}

func (s *Site) settings(w http.ResponseWriter, r *http.Request) {
	s.renderSettings(w, http.StatusOK, newSettingsForm(s.Registry.Settings()), nil)
}

func (s *Site) saveSettings(w http.ResponseWriter, r *http.Request) {
	form := settingsForm{
		Seeds:        r.PostFormValue("seeds"),
		Ports:        r.PostFormValue("ports"),
		IndexerShare: r.PostFormValue("indexer_share"),
		LiveCap:      r.PostFormValue("live_cap"),
		QuietDays:    r.PostFormValue("quiet_days"),
		Visitors:     r.PostFormValue("visitors") != "",
	}
	settings, errs := form.parse(s.Registry.Settings())
	if len(errs) > 0 {
		s.renderSettings(w, http.StatusBadRequest, form, errs)
		return
	}
	if err := s.Registry.SetSettings(settings); err != nil {
		http.Error(w, "failed to save the settings: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.Log.Printf("admin: settings changed")
	if s.SettingsChanged != nil {
		s.SettingsChanged()
	}
	http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
}

type settingsData struct {
	page
	settingsForm
	Errors       map[string]string
	Admin        string
	AdminFromEnv bool
}

func (s *Site) renderSettings(w http.ResponseWriter, status int, form settingsForm, errs map[string]string) {
	data := settingsData{page: s.page("Settings", "settings"), settingsForm: form, Errors: errs, AdminFromEnv: s.AdminPubKey != nil}
	if admin, ok := s.admin(); ok {
		data.Admin = nip19.EncodeNpub(admin)
	}
	s.render(w, status, "settings", data)
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

func failures(n int) string {
	if n == 1 {
		return "1 failure"
	}
	return number(n) + " failures"
}

func status(relay registry.Relay) string {
	switch relay.NIP77 {
	case registry.NIP77Supported:
		return "supported"
	case registry.NIP77Unsupported:
		return "unsupported"
	case registry.NIP77Unreachable:
		return "unreachable"
	case registry.NIP77Forbidden:
		return "forbidden address"
	default:
		return "untested"
	}
}
