# nostr-indexer-relay specification

Status: draft, 2026-10-26.

## Problem

Clients that follow the outbox model need to know, for any pubkey, which relays that user reads from and writes to. That information lives in NIP-65 relay lists (kind 10002), scattered across the network. Indexer relays gather them in one place, but running one today means building it yourself: nothing turnkey exists.

This project is a relay that stores only relay lists, fills itself by crawling the network, and runs from a single binary or Docker image with no required configuration.

## Scope

- Only kind 10002 is stored. Every other kind is rejected with `blocked: only kind 10002 relay lists are accepted`.
- Kind 10002 is replaceable: one version per pubkey, the newest wins, whatever the order of arrival.
- No event is ever served unless its id and signature were verified. This holds for events published directly and for events fetched by the crawler.
- `created_at` is not checked.
- A stored event is at most 128 KB, whatever its path: direct publication, where the max message size enforces it, or the crawler, which applies the same limit. Its content is at most 64 KB, a limit of the LMDB storage format.

Out of scope, by decision:

- Kind 10050 (NIP-17 DM relays).
- Fetching an unknown pubkey on demand when a client asks for it.
- Deletion requests (NIP-09) and requests to vanish (NIP-62). A user changes their list by publishing a newer one.
- Anti-spam (web of trust, proof of work, per-pubkey limits).

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

Federation needs nothing specific: another instance is one more NIP-77 relay for the crawler.

## Crawler

### Discovery

- The crawler starts from a built-in list of seed relays (see Default seeds).
- Every `wss://` URL in the `r` tags of stored 10002 is a candidate relay. Other schemes are ignored.
- Candidate URLs are normalized before entering the registry, so that `wss://x.com` and `wss://x.com/` collapse into one entry. Events themselves are stored as received.
- Candidates are tested in order of popularity: the number of pubkeys whose 10002 lists them.

### Default seeds

Tested on 2026-10-26 with a `COUNT` and a `NEG-OPEN` on `{"kinds":[10002]}`:

| Relay | 10002 held (`COUNT`) | NIP-77 |
| --- | --- | --- |
| `wss://indexer.coracle.social` | 922,435 | Full sync accepted. |
| `wss://relay.ditto.pub` | 1,950,322 | Supported, windowed sync needed. |
| `wss://relay.nos.social` | `COUNT` not supported | Supported, windowed sync needed. |
| `wss://directory.yabu.me` | `COUNT` not supported | Supported, windowed sync needed. |
| `wss://profiles.nostr1.com` | No answer | Supported, windowed sync needed. |

Left out: relay.damus.io, relay.primal.net (193,477 lists) and user.kindpag.es answer `negentropy disabled`; purplepag.es and nos.lol were unreachable; relay.snort.social supports neither `COUNT` nor NIP-77.

### NIP-77 test

NIP-77 support is established by testing, never from the `supported_nips` of NIP-11.

The test sends a `NEG-OPEN` on `{"kinds":[10002]}`:

- `NEG-MSG` in reply: supported.
- `NEG-ERR`: supported, but this query was refused. When the reason is the size of the set, the sync is split into windows (see Sync and revisit).
- `NOTICE`, `CLOSED`, or nothing before a timeout: not supported.

Tests are repeated periodically, since relays get upgraded. Only relays that passed the test are ever queried.

NIP-11 is wrong in both directions: on 2026-10-26, indexer.coracle.social and relay.nos.social did not list 77 but support it.

### Relay registry

For each relay the registry keeps:

- NIP-77 status and date of the last test.
- Activity: new or newer 10002 brought by recent visits.
- Unique contribution: 10002 received from this relay that no indexer had delivered.
- Health: last success, consecutive failures.
- Whether it is an indexer, and whether it has a live subscription.

### Sync and revisit

- Each visit is a negentropy sync on `{"kinds":[10002]}` that downloads what we lack.
- strfry refuses negentropy sessions over 1,000,000 events (`blocked: too many query results`), and the largest indexers hold more 10002 than that. The sync is then split into `since`/`until` windows, halved until each one is accepted. The windows are remembered per relay for the next visits.
- The revisit interval follows activity: shorter when a visit brought new lists, longer when it brought nothing, always between a minimum and a maximum.
- An unreachable relay goes into exponential backoff. If it stays unreachable, it is marked dead and retested rarely.
- Every fetched event has its id and signature verified. Events over 128 KB are skipped, as for direct publications, and so are events tagged `-` (NIP-70: only their author may publish them). The rest goes through the same replace path as direct publications.

### Indexer detection

- A relay is an indexer when it holds a large share of the 10002 we know, 50 % by default, once our own index is filled. What the relay says about itself does not count.
- First signal, before any sync: a NIP-45 `COUNT` on `{"kinds":[10002]}`, for relays that support it.
- After a sync, negentropy gives the exact figure: their count = our count - ids only we have + ids only they have.
- Seed relays are treated as indexers until measured.

### Live subscriptions

- One REQ per relay: `{"kinds":[10002],"since":<now>}`.
- Indexers: always live, no cap.
- Non-indexers: live only while they bring 10002 that no indexer delivered. A relay that brings none for 7 days loses its live subscription and keeps only negentropy visits. Cap of 50 by default, keeping the relays with the largest unique contribution.
- After a disconnection, the gap is filled by the next negentropy sync, not by the live subscription.

### SSRF protection

The crawler connects to URLs taken from untrusted events.

- Only `wss://` URLs are dialed, on port 443 by default; other ports can be allowed in the settings.
- `localhost`, `.onion` and any host that resolves to a loopback, private, link-local, CGNAT, multicast or unspecified address are rejected.
- The resolved IP is checked at dial time, not only the hostname, to defeat DNS rebinding.

## Web interface

The interface is embedded in the binary and served on the same port as the relay. khatru answers WebSocket upgrades and `Accept: application/nostr+json` (NIP-11); every other HTTP request goes to the web interface.

### Admin (`/admin`)

- Dashboard: indexed pubkeys, 10002 received per day, active live subscriptions, detected indexers.
- Relay registry: for each relay its NIP-77 status, activity, unique contribution, and live state. Actions: rerun the test, force a sync, ban the relay, force live.
- Settings: seeds, live cap, thresholds, allowed ports, and the switch for the visitor interface. Settings are stored in the database, so nothing has to be configured through files.

### Visitors (`/`, can be turned off)

- Search by npub, hex or NIP-05: shows the pubkey's relay list (read and write markers) and its date.
- NIP-05 is resolved in the visitor's browser, never by the server, which would open another SSRF entry point.
- Public stats: number of indexed pubkeys, most listed relays.
- The relay URL, to add it as an indexer in a client.

When the admin turns the visitor interface off, the relay, NIP-11 and the admin keep working.

### Admin authentication

- The admin is a pubkey and logs in by signing with NIP-07. The server then issues a session cookie.
- On first start with no admin, the binary logs a one-time setup link. Whoever opens it and signs becomes the admin.
- `RELAY_ADMIN_PUBKEY` sets the admin in advance, for automated deployments.

## Storage

- Events: LMDB, through `fiatjaf.com/nostr/eventstore/lmdb`.
- Crawler state and settings: LMDB too, in a separate environment under the data directory.

Known constraint: khatru caps a negentropy session at 20 times the max limit (40,000 events with the defaults), and the LMDB backend decodes every matching event to build it. Serving a negentropy sync of the whole index to other instances needs a dedicated scan that reads only `(created_at, id)` pairs. Beyond its own limit, the relay must answer `NEG-ERR` with `blocked: too many query results`, like strfry, instead of truncating silently, so that peers know to split their sync.

## Stack

- Go, with `fiatjaf.com/nostr` (khatru, negentropy, NIP-45) and LMDB, like nostr-private-relay.
- Docker image on GHCR for linux/amd64 and linux/arm64, built by GitHub Actions.

## Milestones

1. Relay base: kind 10002 only, NIP-11, NIP-45, NIP-77, high limits, Docker image.
2. Relay registry, SSRF-safe dialer, NIP-77 test.
3. Negentropy crawler: discovery, adaptive revisit, health, full-index negentropy serving.
4. Indexer detection and live subscriptions.
5. Admin interface and authentication.
6. Visitor interface.

## Open questions

- Starting values (revisit minimum and maximum, 7 days, cap of 50, 50 % threshold) are to be tuned on real data.
