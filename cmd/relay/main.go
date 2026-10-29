package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/lmdb"

	"nostr-indexer-relay/internal/crawler"
	"nostr-indexer-relay/internal/index"
	"nostr-indexer-relay/internal/registry"
	indexer "nostr-indexer-relay/internal/relay"
	"nostr-indexer-relay/internal/web"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := indexer.LoadConfig()
	if err != nil {
		return fmt.Errorf("configuration error: %w", err)
	}

	backend := &lmdb.LMDBBackend{Path: cfg.LMDBPath}
	if err := backend.Init(); err != nil {
		return fmt.Errorf("event store error: %w", err)
	}
	defer backend.Close()
	start := time.Now()
	store := index.New(backend)
	log.Printf("indexed %d relay lists in %s", store.Len(), time.Since(start).Round(time.Millisecond))

	relays, err := registry.Open(cfg.StatePath)
	if err != nil {
		return fmt.Errorf("relay registry error: %w", err)
	}
	defer relays.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	relay := indexer.New(cfg, store)
	relay.OnEventSaved = func(context.Context, nostr.Event) { relays.CountStored(time.Now()) }
	crawl := crawler.New(store, relay, relays, log.New(os.Stderr, "[crawler] ", log.LstdFlags))

	site := web.New(web.Config{
		Name:            cfg.Name,
		PublicURL:       cfg.RelayURL,
		Store:           store,
		Registry:        relays,
		AdminPubKey:     cfg.AdminPubKey,
		SettingsChanged: crawl.Rediscover,
		Log:             log.New(os.Stderr, "[admin] ", log.LstdFlags),
	})
	site.Register(relay.Router())
	if link, ok := site.SetupLink(web.BaseURL(cfg.RelayURL, cfg.Addr)); ok {
		log.Printf("no admin yet: open %s and sign to become the admin", link)
	}

	server := &http.Server{Addr: cfg.Addr, Handler: relay}
	go func() {
		<-ctx.Done()
		server.Shutdown(context.Background())
	}()

	crawlerDone := make(chan struct{})
	go func() {
		defer close(crawlerDone)
		crawl.Run(ctx)
	}()

	log.Printf("nostr indexer relay listening on %s", cfg.Addr)
	serveErr := server.ListenAndServe()
	stop()
	<-crawlerDone
	if !errors.Is(serveErr, http.ErrServerClosed) {
		return fmt.Errorf("relay stopped: %w", serveErr)
	}
	return nil
}
