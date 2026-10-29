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

	store := &lmdb.LMDBBackend{Path: cfg.LMDBPath}
	if err := store.Init(); err != nil {
		return fmt.Errorf("event store error: %w", err)
	}
	defer store.Close()

	state, err := registry.Open(cfg.StatePath)
	if err != nil {
		return fmt.Errorf("state database error: %w", err)
	}
	defer state.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	relay := indexer.New(cfg, store)
	relay.OnEventSaved = func(context.Context, nostr.Event) { state.CountStored(time.Now()) }

	site := web.New(web.Config{
		Name:        cfg.Name,
		PublicURL:   cfg.RelayURL,
		Store:       store,
		Registry:    state,
		AdminPubKey: cfg.AdminPubKey,
		Log:         log.New(os.Stderr, "[admin] ", log.LstdFlags),
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

	log.Printf("nostr indexer relay listening on %s", cfg.Addr)
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("relay stopped: %w", err)
	}
	return nil
}
