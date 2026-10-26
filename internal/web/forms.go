package web

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"nostr-indexer-relay/internal/crawler"
	"nostr-indexer-relay/internal/registry"
)

const day = 24 * time.Hour

// settingsForm holds the settings as typed, so that a form with errors is shown back unchanged.
type settingsForm struct {
	Seeds        string
	Ports        string
	IndexerShare string
	LiveCap      string
	QuietDays    string
	Visitors     bool
}

func newSettingsForm(settings registry.Settings) settingsForm {
	ports := make([]string, len(settings.AllowedPorts))
	for i, port := range settings.AllowedPorts {
		ports[i] = strconv.Itoa(port)
	}
	return settingsForm{
		Seeds:        strings.Join(settings.Seeds, "\n"),
		Ports:        strings.Join(ports, ", "),
		IndexerShare: strconv.FormatFloat(settings.IndexerShare*100, 'f', -1, 64),
		LiveCap:      strconv.Itoa(settings.LiveCap),
		QuietDays:    strconv.Itoa(int(settings.LiveQuietPeriod / day)),
		Visitors:     settings.Visitors,
	}
}

// parse returns current with the form's values, or an error message per invalid field.
func (f settingsForm) parse(current registry.Settings) (registry.Settings, map[string]string) {
	settings := current
	settings.Visitors = f.Visitors
	errs := make(map[string]string)

	settings.AllowedPorts = nil
	for _, field := range strings.FieldsFunc(f.Ports, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
		port, err := strconv.Atoi(field)
		if err != nil || port < 1 || port > 65535 {
			errs["ports"] = fmt.Sprintf("%q is not a port number.", field)
			break
		}
		if port != 443 && !slices.Contains(settings.AllowedPorts, port) {
			settings.AllowedPorts = append(settings.AllowedPorts, port)
		}
	}
	slices.Sort(settings.AllowedPorts)

	settings.Seeds = nil
	for line := range strings.Lines(f.Seeds) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		seed, err := crawler.NormalizeRelayURL(line, settings.AllowedPorts)
		if err != nil {
			errs["seeds"] = fmt.Sprintf("%s cannot be crawled: %v.", line, err)
			break
		}
		if !slices.Contains(settings.Seeds, seed) {
			settings.Seeds = append(settings.Seeds, seed)
		}
	}
	if len(settings.Seeds) == 0 && errs["seeds"] == "" {
		errs["seeds"] = "Add at least one seed relay: the crawler starts from them."
	}

	if share, err := strconv.ParseFloat(strings.TrimSpace(f.IndexerShare), 64); err != nil || share <= 0 || share > 100 {
		errs["indexer_share"] = "Enter a percentage above 0 and up to 100."
	} else {
		settings.IndexerShare = share / 100
	}

	if liveCap, err := strconv.Atoi(strings.TrimSpace(f.LiveCap)); err != nil || liveCap < 0 {
		errs["live_cap"] = "Enter a whole number, 0 or more."
	} else {
		settings.LiveCap = liveCap
	}

	if days, err := strconv.Atoi(strings.TrimSpace(f.QuietDays)); err != nil || days < 1 {
		errs["quiet_days"] = "Enter a whole number of days, 1 or more."
	} else {
		settings.LiveQuietPeriod = time.Duration(days) * day
	}

	return settings, errs
}
