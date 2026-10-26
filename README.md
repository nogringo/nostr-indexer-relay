# nostr-indexer-relay

Turnkey Nostr indexer relay. It stores NIP-65 relay lists (kind 10002) only, and fills itself by crawling the network over NIP-77.

Status: early. The relay accepts and serves kind 10002. The crawler discovers relays from the stored lists, tests their NIP-77 support and syncs from the ones that pass. It detects the indexers and follows them live, along with the relays that bring lists the indexers miss. A public page at `/` looks up any pubkey's relay list, and an admin interface at `/admin` shows the crawl and holds the settings. See [SPEC.md](SPEC.md) for the full design.

Supported NIPs: 1, 11, 40, 42, 45, 65, 70, 77.

## Run

```sh
docker compose up -d
```

or, from source:

```sh
go run ./cmd/relay
```

## Admin

The admin interface is at `/admin`, on the same port as the relay. On first start, the relay logs a one-time setup link: open it and sign with a NIP-07 browser extension to become the admin. To set the admin in advance instead, set `RELAY_ADMIN_PUBKEY`.

The seeds, allowed ports, indexer threshold, live subscription limits and the public search page are changed there and kept in the state database.

Behind a reverse proxy that rewrites the `Host` header, set `RELAY_URL` to the public URL, otherwise the sign-in is refused as coming from another site.

## Tests

```sh
go test -race -gcflags=all=-d=checkptr=0 ./...
```

`checkptr` is turned off because `fiatjaf.com/nostr` serializes events with pointer arithmetic that it rejects, which crashes the tests under `-race`.

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
| `RELAY_ADMIN_PUBKEY` | empty | Admin pubkey, npub or hex. Without it, the first person to open the setup link from the log becomes the admin. |
| `RELAY_LMDB_PATH` | `./data/lmdb` | LMDB directory for events. |
| `RELAY_STATE_PATH` | `./data/state` | LMDB directory for the crawler's relay registry. |
| `RELAY_MAX_LIMIT` | `2000` | Maximum query limit. A REQ for N authors needs at least N. |
