package registry

import (
	"bytes"
	"slices"
	"testing"
	"time"

	"fiatjaf.com/nostr"
)

func TestSettingsStartAtTheDefaultsAndSurviveReopening(t *testing.T) {
	path := t.TempDir()
	r := openTestRegistry(t, path)
	if got := r.Settings(); !slices.Equal(got.Seeds, DefaultSettings().Seeds) || got.LiveCap != 50 || got.IndexerShare != 0.5 {
		t.Fatalf("expected the defaults, got %+v", got)
	}
	if _, ok := r.Admin(); ok {
		t.Fatal("expected no admin on a new registry")
	}

	settings := r.Settings()
	settings.Seeds, settings.AllowedPorts, settings.LiveCap = []string{"wss://seed.example.com"}, []int{7777}, 10
	if err := r.SetSettings(settings); err != nil {
		t.Fatal(err)
	}
	admin := nostr.Generate().Public()
	if err := r.SetAdmin(admin); err != nil {
		t.Fatal(err)
	}
	secret := r.SessionSecret()
	r.Close()

	reopened := openTestRegistry(t, path)
	got := reopened.Settings()
	if !slices.Equal(got.Seeds, settings.Seeds) || !slices.Equal(got.AllowedPorts, settings.AllowedPorts) || got.LiveCap != 10 || got.LiveQuietPeriod != settings.LiveQuietPeriod {
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

func TestDueSkipsBannedRelays(t *testing.T) {
	r := openTestRegistry(t, t.TempDir())
	if err := r.SetPopularity(map[string]int{"wss://banned.example.com": 5, "wss://other.example.com": 1}, nil); err != nil {
		t.Fatal(err)
	}
	if err := r.Update("wss://banned.example.com", func(relay *Relay) { relay.Banned, relay.NIP77 = true, NIP77Supported }); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	if due := r.Due(now, 10); len(due) != 1 || due[0].URL != "wss://other.example.com" {
		t.Errorf("expected only the other relay to be tested, got %+v", due)
	}
	if due := r.DueForSync(now, 10); len(due) != 0 {
		t.Errorf("expected the banned relay not to be synced, got %+v", due)
	}
}
