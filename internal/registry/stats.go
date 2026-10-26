package registry

import (
	"strconv"
	"time"

	"github.com/PowerDNS/lmdb-go/lmdb"
)

const statsFlushInterval = time.Minute

type DayCount struct {
	Day   time.Time
	Lists int
}

func dayKey(t time.Time) string {
	return t.UTC().Format(time.DateOnly)
}

func (r *Registry) loadStats() error {
	return r.env.View(func(txn *lmdb.Txn) error {
		cursor, err := txn.OpenCursor(r.stats)
		if err != nil {
			return err
		}
		defer cursor.Close()

		for {
			key, value, err := cursor.Get(nil, nil, lmdb.Next)
			if lmdb.IsNotFound(err) {
				return nil
			}
			if err != nil {
				return err
			}
			count, err := strconv.Atoi(string(value))
			if err != nil {
				return err
			}
			r.stored[string(key)] = count
		}
	})
}

// CountStored adds a stored list to the count of its day.
func (r *Registry) CountStored(at time.Time) {
	r.statsMu.Lock()
	defer r.statsMu.Unlock()

	day := dayKey(at)
	r.stored[day]++
	r.unflushed[day] = true
	if at.Sub(r.flushedAt) >= statsFlushInterval {
		r.flushStats()
		r.flushedAt = at
	}
}

// StoredPerDay returns the number of lists stored on each of the last days, today included, oldest first.
func (r *Registry) StoredPerDay(now time.Time, days int) []DayCount {
	r.statsMu.Lock()
	defer r.statsMu.Unlock()

	today := now.UTC().Truncate(24 * time.Hour)
	counts := make([]DayCount, days)
	for i := range counts {
		day := today.AddDate(0, 0, i-days+1)
		counts[i] = DayCount{Day: day, Lists: r.stored[dayKey(day)]}
	}
	return counts
}

// flushStats writes the days counted since the last flush. A failure keeps them for the next one.
func (r *Registry) flushStats() {
	if len(r.unflushed) == 0 {
		return
	}
	if err := r.env.Update(func(txn *lmdb.Txn) error {
		for day := range r.unflushed {
			if err := txn.Put(r.stats, []byte(day), []byte(strconv.Itoa(r.stored[day])), 0); err != nil {
				return err
			}
		}
		return nil
	}); err == nil {
		clear(r.unflushed)
	}
}
