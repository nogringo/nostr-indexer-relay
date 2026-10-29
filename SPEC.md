# nostr-indexer-relay specification

Status: draft, 2026-10-26.

## Problem

Clients that follow the outbox model need to know, for any pubkey, which relays that user reads from and writes to. That information lives in NIP-65 relay lists (kind 10002), scattered across the network. Indexer relays gather them in one place, but running one today means building it yourself: nothing turnkey exists.

This project is a relay that stores only relay lists, the ones published to it, and runs from a single binary or Docker image with no required configuration.

## Scope

- Only kind 10002 is stored. Every other kind is rejected with `blocked: only kind 10002 relay lists are accepted`.
- Kind 10002 is replaceable: one version per pubkey, the newest wins, whatever the order of arrival. A list older than ours, or one we already have, is acknowledged as a duplicate: nothing is stored, broadcast or counted.
- No event is ever served unless its id and signature were verified.
- `created_at` is not checked.
- A stored event is at most 128 KB, the max message size. Its content is at most 64 KB, a limit of the LMDB storage format.

Out of scope, by decision:

- Kind 10050 (NIP-17 DM relays).
- Fetching an unknown pubkey on demand when a client asks for it.
- Deletion requests (NIP-09) and requests to vanish (NIP-62). A user changes their list by publishing a newer one.
- Anti-spam (web of trust, proof of work, per-pubkey limits).
- Crawling other relays. The relay stores only the lists published to it. The crawler (discovery, NIP-77 sync, live subscriptions, indexer detection) was removed on 2026-10-29.

## Serving

| NIP | Role |
| --- | --- |
| 1 | Base protocol. |
| 11 | Relay information document. |
| 40 | Handled by khatru: a list with an `expiration` tag is deleted when it expires. |
| 42, 70 | Handled by khatru, so that authors can publish protected (`-` tagged) lists themselves. |
| 45 | `COUNT`, so that other instances can tell this relay is an indexer. |
| 65 | The only stored kind. |
| 77 | Negentropy sync, for clients and for other indexers. |

Large `authors` filters are expected: to follow the outbox model, a client asks for the 10002 of hundreds or thousands of authors in a single REQ. Each author has at most one 10002, so a filter with N authors needs a limit of at least N.

- Max limit: 2000 by default (`RELAY_MAX_LIMIT`).
- Max message size: 128 KB, like indexer.coracle.social. It bounds what clients send. In a REQ, each entry of `authors` is a 64-character hex pubkey, about 67 bytes of JSON, so one message holds about 1950 authors.
- The max limit covers every author that fits in one message. A client whose message is too big is disconnected and sees the error, whereas a filter beyond the max limit would get a truncated answer without the client knowing.
- Both are advertised in NIP-11 (`max_limit`, `max_message_length`).

## Web interface

The interface is embedded in the binary and served on the same port as the relay. khatru answers WebSocket upgrades and `Accept: application/nostr+json` (NIP-11); every other HTTP request goes to the web interface.

Pages are rendered on the server, with a single script for NIP-07 signing. They load nothing from other sites, and a content security policy holds them to that.

### Admin (`/admin`)

- Dashboard: indexed pubkeys, new or updated 10002 stored per day (UTC, last 30 days).
- Settings: the switch for the visitor interface. Settings are stored in the database, so nothing has to be configured through files.

### Visitors (`/`, can be turned off)

- Search by npub, hex or NIP-05: shows the pubkey's relay list (read and write markers) and its date. nprofile and the `nostr:` prefix of NIP-21 are accepted too.
- Each list has its own page, `/p/<npub>`, which also shows the raw event. Other forms of the pubkey redirect to it.
- NIP-05 is resolved in the visitor's browser, never by the server, which would open another SSRF entry point. Redirects are refused, as NIP-05 requires. The content security policy of the public pages lets scripts reach any `https://` origin for this, and nothing else. Without JavaScript, a NIP-05 search asks for an npub or a hex pubkey instead.
- Public stats: number of indexed pubkeys, computed at most once a minute, since counting a full index takes a while.
- The relay URL, to add it as an indexer in a client: `RELAY_URL` when set, otherwise the address the page was reached at.
- `robots.txt` keeps crawlers away from `/p/` and `/admin`.

When the admin turns the visitor interface off, the relay, NIP-11 and the admin keep working, and the public pages answer 404.

### Admin authentication

- The admin is a pubkey and logs in by signing with NIP-07. The server then issues a session cookie.
- The signed event is a NIP-98 HTTP auth event (kind 27235) naming the login URL and `POST`. It must be at most a minute off and works once.
- The session cookie holds the admin pubkey and an expiry 30 days out, signed with a key kept in the state database. It is `HttpOnly` and `SameSite=Strict`, and requests sent from another origin are refused.
- On first start with no admin, the binary logs a one-time setup link. Whoever opens it and signs becomes the admin. While there is no admin, each start logs a new link.
- `RELAY_ADMIN_PUBKEY` (npub or hex) sets the admin in advance, for automated deployments. It takes precedence over an admin claimed through the setup link.

## Storage

- Events: LMDB, through `fiatjaf.com/nostr/eventstore/lmdb`.
- State: LMDB too, in a separate environment (`RELAY_STATE_PATH`, `./data/state` by default). It keeps the settings, the admin pubkey, the session key and the lists stored per day, the last written at most once a minute. A state written before the crawler was removed keeps its `relays` table, which nothing reads any more.

The LMDB backend allocates for the whole limit of a query up front, so the negentropy sessions served to peers, which read the full index, go through a paged scan, 5000 events at a time. When more than 5000 lists share one `created_at`, the rest of that second is skipped.

Negentropy sessions served to peers:

- khatru alone caps a session at 20 times the max limit (40,000 events with the defaults) and truncates the rest silently. The relay serves sessions in full instead.
- Beyond 1,000,000 events, like strfry, a session is refused with `blocked: too many query results`, so that peers know to split their sync. The size is checked on the LMDB index before the session is built.
- khatru labels this refusal `NEG-ERROR` instead of NIP-77's `NEG-ERR`, which a strict NIP-77 client will not recognize. This needs a fix in khatru.

## Stack

- Go, with `fiatjaf.com/nostr` (khatru, negentropy, NIP-45) and LMDB, like nostr-private-relay.
- Docker image on GHCR for linux/amd64 and linux/arm64, built by GitHub Actions.

## Milestones

1. Relay base: kind 10002 only, NIP-11, NIP-45, NIP-77, high limits, full-index negentropy serving, Docker image. Done.
2. Admin interface and authentication. Done.
3. Visitor interface. Done.
