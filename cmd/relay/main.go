package main

import (
	"log"
	"net/http"

	indexer "nostr-indexer-relay/internal/relay"
)

func main() {
	cfg, err := indexer.LoadConfig()
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}

	relay, closeStore, err := indexer.New(cfg)
	if err != nil {
		log.Fatalf("relay setup error: %v", err)
	}
	defer closeStore()

	log.Printf("nostr indexer relay listening on %s", cfg.Addr)
	if err := http.ListenAndServe(cfg.Addr, relay); err != nil {
		log.Fatalf("relay stopped: %v", err)
	}
}
