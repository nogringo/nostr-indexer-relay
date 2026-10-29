package registry

import (
	"bytes"
	"testing"
	"time"

	"fiatjaf.com/nostr"
)

func TestSettingsStartAtTheDefaultsAndSurviveReopening(t *testing.T) {
	path := t.TempDir()
	r := openTestRegistry(t, path)
	if got := r.Settings(); !got.Visitors || got.MinPoW != 0 {
		t.Fatalf("expected the defaults, got %+v", got)
	}
	if _, ok := r.Admin(); ok {
		t.Fatal("expected no admin on a new registry")
	}

	if err := r.SetSettings(Settings{Visitors: false, MinPoW: 20}); err != nil {
		t.Fatal(err)
	}
	admin := nostr.Generate().Public()
	if err := r.SetAdmin(admin); err != nil {
		t.Fatal(err)
	}
	secret := r.SessionSecret()
	r.Close()

	reopened := openTestRegistry(t, path)
	if got := reopened.Settings(); got.Visitors || got.MinPoW != 20 {
		t.Errorf("expected the saved settings, got %+v", got)
	}
	if stored, ok := reopened.Admin(); !ok || stored != admin {
		t.Errorf("expected the admin to survive reopening, got %v %v", stored, ok)
	}
	if !bytes.Equal(reopened.SessionSecret(), secret) || len(secret) != 32 {
		t.Error("expected the session secret to survive reopening")
	}
}

func TestStoredPerDayCountsEachUTCDay(t *testing.T) {
	path := t.TempDir()
	r := openTestRegistry(t, path)
	today := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	for _, at := range []time.Time{today, today.Add(time.Hour), today.AddDate(0, 0, -2)} {
		r.CountStored(at)
	}
	r.Close()

	reopened := openTestRegistry(t, path)
	days := reopened.StoredPerDay(today, 3)
	want := []int{1, 0, 2}
	for i, day := range days {
		if day.Lists != want[i] || !day.Day.Equal(today.Truncate(24*time.Hour).AddDate(0, 0, i-2)) {
			t.Errorf("day %d: got %+v, want %d lists", i, day, want[i])
		}
	}
}

func openTestRegistry(t *testing.T, path string) *Registry {
	t.Helper()
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	return r
}
