# nostr-indexer-relay specification

Status: draft, 2026-10-26.

## Problem

Clients that follow the outbox model need to know, for any pubkey, which relays that user reads from and writes to. That information lives in NIP-65 relay lists (kind 10002), scattered across the network. Indexer relays gather them in one place, but running one today means building it yourself: nothing turnkey exists.

This project is a relay that stores only relay lists, fills itself by crawling the network, and runs from a single binary or Docker image with no required configuration.

## Scope

- Only kind 10002 is stored. Every other kind is rejected with `blocked: only kind 10002 relay lists are accepted`.
- Kind 10002 is replaceable: one version per pubkey, the newest wins, whatever the order of arrival. A list older than ours, or one we already have, is acknowledged as a duplicate: nothing is stored, broadcast or counted.
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
- Candidate URLs are normalized before entering the registry, so that `wss://x.com` and `wss://x.com/` collapse into one entry. URLs over 255 bytes are ignored. Events themselves are stored as received.
- Candidates are tested in order of popularity: the number of pubkeys whose 10002 lists them, seeds first.
- Popularity is recounted by a full scan of the stored lists at startup and every 15 minutes. A relay that no list mentions any more is no longer tested, unless it is a seed.

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

Tests run 16 at a time, with a 10 second timeout each. A relay that answered is retested every 7 days, since relays get upgraded. Only relays that passed the test are ever queried.

NIP-11 is wrong in both directions: on 2026-10-26, indexer.coracle.social and relay.nos.social did not list 77 but support it.

### Relay registry

For each relay the registry keeps:

- NIP-77 status and date of the last test.
- Activity: new or newer 10002 brought by recent visits.
- Unique contribution: 10002 received from this relay that no indexer had delivered.
- Health: last success, consecutive failures.
- Whether it is an indexer, and whether it has a live subscription.

### Sync and revisit

- Each visit is a download-only negentropy sync on `{"kinds":[10002]}`. Only relays whose last test passed are visited, 4 at a time.
- strfry refuses negentropy sessions over 1,000,000 events (`blocked: too many query results`), and the largest indexers hold more 10002 than that. The sync is then split into `since`/`until` windows: a refused window is cut halfway between its bounds (2020-01-01 for the first window, now for the last one) until each one is accepted. The windows are remembered per relay for the next visits, even when a sync is interrupted by a shutdown.
- A negentropy round may take up to 2 minutes: directory.yabu.me took 35 s to open a window of about a million lists.
- Missing lists are fetched by id, 500 per REQ. A relay may return fewer than asked for (its `max_limit`), so missing ids are asked again until a pass brings none.
- The revisit interval follows activity: it starts at 1 hour, is halved after a visit that brought new lists and doubled otherwise, between 10 minutes and 24 hours.
- An unreachable relay goes into exponential backoff, from 1 hour doubling at each failure. After 8 consecutive failures, about 5 days, it is marked dead and retested every 30 days. A sync that cannot connect hands the relay back to this backoff.
- Every fetched event has its id and its signature verified; the signature check alone trusts the announced id. Events over 128 KB are skipped, as for direct publications, and so are events tagged `-` (NIP-70: only their author may publish them). A list is stored only when it is newer than ours, through the same path as direct publications, and pushed to our live subscribers.
- Measured on 2026-10-26 from an empty index: about 1,500 lists per second, 615,000 lists after 7 minutes.

### Indexer detection

- A relay is an indexer when it holds a large share of the 10002 we know, 50 % by default, once our own index is filled. What the relay says about itself does not count.
- Our index counts as filled once every seed that passed the NIP-77 test has been visited at least once. Until then, the seeds are the only indexers.
- First signal, before any sync: a NIP-45 `COUNT` on `{"kinds":[10002]}`, for relays that support it. It is sent on the connection of the NIP-77 test, as long as no sync has measured the relay.
- After a sync, negentropy gives the exact figure: their count = our count - ids only we have + ids only they have. A sync that does not reach its last window measures nothing.
- Seed relays are treated as indexers until measured.
- Relays are classified again every minute, against the current number of lists in our index. A relay that holds no list is never an indexer.

### Live subscriptions

- One REQ per relay: `{"kinds":[10002],"since":<now>}`.
- Indexers: always live, no cap.
- Non-indexers: live only while they bring 10002 that no indexer delivered. A relay that brings none for 7 days loses its live subscription and keeps only negentropy visits. Cap of 50 by default, keeping the relays with the largest unique contribution.
- A list counts toward the unique contribution of the non-indexer that delivered it first, through a sync or live, when no indexer delivers it within 1 minute: a new list often reaches several relays at once. Nothing counts before our index is filled, since the indexers have not delivered everything yet.
- The unique contribution is a running total, and the 7 days run from the last unique list.
- The set of live relays is recomputed every minute. A subscription that drops is reopened at the next check. A relay that cannot be reached is handed back to the tester's backoff, as for syncs.
- After a disconnection, the gap is filled by the next negentropy sync, not by the live subscription.

### SSRF protection

The crawler connects to URLs taken from untrusted events.

- Only `wss://` URLs are dialed, on port 443 by default; other ports can be allowed in the settings.
- `localhost`, `.onion` and any host that resolves to a loopback, private, link-local, CGNAT, multicast or unspecified address are rejected.
- The resolved IP is checked at dial time, not only the hostname, to defeat DNS rebinding. A relay that fails this check is marked forbidden and rechecked every 30 days, since DNS can change.
- HTTP redirects are not followed.

## Web interface

The interface is embedded in the binary and served on the same port as the relay. khatru answers WebSocket upgrades and `Accept: application/nostr+json` (NIP-11); every other HTTP request goes to the web interface.

Pages are rendered on the server, with a single script for NIP-07 signing. They load nothing from other sites, and a content security policy holds them to that.

### Admin (`/admin`)

- Dashboard: indexed pubkeys, new or updated 10002 stored per day (UTC, last 30 days, from publications and from the crawler), active live subscriptions, detected indexers.
- Relay registry: for each relay its NIP-77 status, activity, unique contribution, and live state, filtered by state and searched by URL, 100 relays per page. Actions: rerun the test, force a sync, ban the relay, force live.
- A banned relay is no longer tested, synced or followed live. A relay forced live is followed whether it is an indexer or not, outside the live cap, as long as its NIP-77 test passed. The crawler picks up an action within a minute.
- Settings: seeds, allowed ports, the indexer threshold, the live cap and the days before a quiet relay stops being followed live. The switch for the visitor interface comes with that interface. Settings are stored in the database, so nothing has to be configured through files. A change starts a discovery right away, so that new seeds and ports apply.

### Visitors (`/`, can be turned off)

- Search by npub, hex or NIP-05: shows the pubkey's relay list (read and write markers) and its date.
- NIP-05 is resolved in the visitor's browser, never by the server, which would open another SSRF entry point.
- Public stats: number of indexed pubkeys, most listed relays.
- The relay URL, to add it as an indexer in a client.

When the admin turns the visitor interface off, the relay, NIP-11 and the admin keep working.

### Admin authentication

- The admin is a pubkey and logs in by signing with NIP-07. The server then issues a session cookie.
- The signed event is a NIP-98 HTTP auth event (kind 27235) naming the login URL and `POST`. It must be at most a minute off and works once.
- The session cookie holds the admin pubkey and an expiry 30 days out, signed with a key kept in the state database. It is `HttpOnly` and `SameSite=Strict`, and requests sent from another origin are refused.
- On first start with no admin, the binary logs a one-time setup link. Whoever opens it and signs becomes the admin. While there is no admin, each start logs a new link.
- `RELAY_ADMIN_PUBKEY` (npub or hex) sets the admin in advance, for automated deployments. It takes precedence over an admin claimed through the setup link.

## Storage

- Events: LMDB, through `fiatjaf.com/nostr/eventstore/lmdb`.
- Crawler state and settings: LMDB too, in a separate environment (`RELAY_STATE_PATH`, `./data/state` by default). The registry is held in memory and every change is written through. The same environment keeps the settings, the admin pubkey, the session key and the lists stored per day, the last written at most once a minute.

The LMDB backend allocates for the whole limit of a query up front, so every full read of the index (discovery, negentropy vectors, negentropy sessions served to peers) goes through a paged scan, 5000 events at a time. When more than 5000 lists share one `created_at`, the rest of that second is skipped.

Negentropy sessions served to peers:

- khatru alone caps a session at 20 times the max limit (40,000 events with the defaults) and truncates the rest silently. The relay serves sessions in full instead.
- Beyond 1,000,000 events, like strfry, a session is refused with `blocked: too many query results`, so that peers know to split their sync. The size is checked on the LMDB index before the session is built.
- khatru labels this refusal `NEG-ERROR` instead of NIP-77's `NEG-ERR`. Our own crawler accepts both, but a strict NIP-77 client will not recognize it. This needs a fix in khatru.

## Stack

- Go, with `fiatjaf.com/nostr` (khatru, negentropy, NIP-45) and LMDB, like nostr-private-relay.
- Docker image on GHCR for linux/amd64 and linux/arm64, built by GitHub Actions.

## Milestones

1. Relay base: kind 10002 only, NIP-11, NIP-45, NIP-77, high limits, Docker image. Done.
2. Relay registry, SSRF-safe dialer, NIP-77 test. Done.
3. Negentropy crawler: discovery, adaptive revisit, health, full-index negentropy serving. Done.
4. Indexer detection and live subscriptions. Done.
5. Admin interface and authentication. Done.
6. Visitor interface.

## Open questions

- Starting values (revisit minimum and maximum, 7 days, cap of 50, 50 % threshold, 16 parallel tests, 10 second timeout, 15 minute discovery, backoff and dead thresholds, 1 minute grace for unique lists) are to be tuned on real data.
- With a running total, a relay that brought many unique lists long ago outranks a recent contributor once the cap of 50 is reached. A total over a recent period would fix this, if it matters on real data.
