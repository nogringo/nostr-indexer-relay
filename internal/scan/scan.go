package scan

import (
	"iter"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore"
)

const PageSize = 5000

// Events yields every stored event matching filter, newest first, pageSize at a time,
// because the LMDB backend allocates for the whole limit up front. When more than a
// page of events share one created_at, the rest of that second is skipped.
func Events(store eventstore.Store, filter nostr.Filter, pageSize int) iter.Seq[nostr.Event] {
	return func(yield func(nostr.Event) bool) {
		var seenAtUntil map[nostr.ID]bool
		for {
			read := 0
			var oldest nostr.Timestamp
			idsAtOldest := make(map[nostr.ID]bool)
			for event := range store.QueryEvents(filter, pageSize) {
				read++
				if event.CreatedAt != oldest {
					oldest = event.CreatedAt
					clear(idsAtOldest)
				}
				idsAtOldest[event.ID] = true
				if seenAtUntil[event.ID] {
					continue
				}
				if !yield(event) {
					return
				}
			}

			switch {
			case read < pageSize || oldest == 0:
				return
			case oldest == filter.Until:
				filter.Until, seenAtUntil = oldest-1, nil
			default:
				filter.Until, seenAtUntil = oldest, idsAtOldest
			}
		}
	}
}
