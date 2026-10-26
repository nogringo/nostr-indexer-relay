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

	"fiatjaf.com/nostr/eventstore/lmdb"

	"nostr-indexer-relay/internal/crawler"
	"nostr-indexer-relay/internal/registry"
	indexer "nostr-indexer-relay/internal/relay"
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

	relays, err := registry.Open(cfg.StatePath)
	if err != nil {
		return fmt.Errorf("relay registry error: %w", err)
	}
	defer relays.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	server := &http.Server{Addr: cfg.Addr, Handler: indexer.New(cfg, store)}
	go func() {
		<-ctx.Done()
		server.Shutdown(context.Background())
	}()

	crawlerDone := make(chan struct{})
	go func() {
		defer close(crawlerDone)
		crawler.New(store, relays, log.New(os.Stderr, "[crawler] ", log.LstdFlags)).Run(ctx)
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
