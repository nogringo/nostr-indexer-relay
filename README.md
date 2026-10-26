# nostr-indexer-relay

Turnkey Nostr indexer relay. It stores NIP-65 relay lists (kind 10002) only, and fills itself by crawling the network over NIP-77.

Status: early. The relay accepts and serves kind 10002. The crawler discovers relays from the stored lists and tests their NIP-77 support, but does not sync from them yet. There is no web interface yet. See [SPEC.md](SPEC.md) for the full design.

Supported NIPs: 1, 11, 40, 42, 45, 65, 70, 77.

## Run

```sh
docker compose up -d
```

or, from source:

```sh
go run ./cmd/relay
```

## Configuration

Every variable is optional.

| Variable | Default | Description |
| --- | --- | --- |
| `RELAY_ADDR` | `:3334` | HTTP/WebSocket listen address. |
| `RELAY_URL` | empty | Public relay URL, for example `wss://indexer.example.com/`. Used for NIP-42 challenges when an author publishes a protected list. |
| `RELAY_NAME` | `Indexer relay` | NIP-11 relay name. |
| `RELAY_DESCRIPTION` | `An indexer relay that stores NIP-65 relay lists (kind 10002) only.` | NIP-11 relay description. |
| `RELAY_ICON` | empty | Optional NIP-11 icon URL. |
| `RELAY_CONTACT` | empty | Optional contact info for the relay operator. |
| `RELAY_PUBKEY` | empty | Optional relay operator pubkey in hex, NIP-11 metadata only. |
| `RELAY_LMDB_PATH` | `./data/lmdb` | LMDB directory for events. |
| `RELAY_STATE_PATH` | `./data/state` | LMDB directory for the crawler's relay registry. |
| `RELAY_MAX_LIMIT` | `2000` | Maximum query limit. A REQ for N authors needs at least N. |
