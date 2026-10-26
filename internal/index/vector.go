package index

import (
	"bytes"
	"iter"
	"sort"

	"fiatjaf.com/nostr/nip77/negentropy"
	"fiatjaf.com/nostr/nip77/negentropy/storage"
)

var _ negentropy.Storage = (*Vector)(nil)

// Vector is negentropy storage over items already in negentropy order. Unlike the vector of
// fiatjaf.com/nostr, it is allocated once at its final size.
type Vector struct {
	items []negentropy.Item
	acc   storage.Accumulator
}

func (v *Vector) Size() int { return len(v.items) }

func (v *Vector) Range(begin, end int) iter.Seq2[int, negentropy.Item] {
	return func(yield func(int, negentropy.Item) bool) {
		for i := begin; i < end; i++ {
			if !yield(i, v.items[i]) {
				return
			}
		}
	}
}

func (v *Vector) FindLowerBound(begin, end int, bound negentropy.Bound) int {
	return begin + sort.Search(end-begin, func(i int) bool {
		item := v.items[begin+i]
		return item.Timestamp > bound.Timestamp ||
			(item.Timestamp == bound.Timestamp && bytes.Compare(item.ID[:], bound.IDPrefix) >= 0)
	})
}

func (v *Vector) GetBound(idx int) negentropy.Bound {
	if idx < len(v.items) {
		return negentropy.Bound{Timestamp: v.items[idx].Timestamp, IDPrefix: v.items[idx].ID[:]}
	}
	return negentropy.InfiniteBound
}

func (v *Vector) Fingerprint(begin, end int) [negentropy.FingerprintSize]byte {
	v.acc.Reset()
	for _, item := range v.items[begin:end] {
		v.acc.AddBytes(item.ID[:])
	}
	return v.acc.GetFingerprint(end - begin)
}
