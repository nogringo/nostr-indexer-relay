package web

import (
	"fmt"

	"nostr-indexer-relay/internal/registry"
)

// The SVG is stretched to the width of the page, so its labels are drawn in HTML instead.
const (
	chartWidth    = 720
	chartHeight   = 140
	chartTop      = 4
	chartBarWidth = 14
	chartRadius   = 4
)

// chart is a column per day.
type chart struct {
	Width, Height int
	Bars          []chartBar
	Peak          *chartBar
	First, Last   string
	Days          []registry.DayCount
}

type chartBar struct {
	X, SlotX, SlotWidth float64
	Path                string
	Day                 string
	Lists               int
}

func newChart(days []registry.DayCount) chart {
	c := chart{Width: chartWidth, Height: chartHeight, Days: days}
	if len(days) == 0 {
		return c
	}
	c.First, c.Last = days[0].Day.Format("Jan 2"), days[len(days)-1].Day.Format("Jan 2")

	peak := 0
	for _, day := range days {
		peak = max(peak, day.Lists)
	}
	slot := float64(chartWidth) / float64(len(days))
	plot := float64(chartHeight - chartTop)
	baseline := float64(chartHeight)
	for i, day := range days {
		bar := chartBar{SlotX: float64(i) * slot, SlotWidth: slot, Day: day.Day.Format("Jan 2"), Lists: day.Lists}
		bar.X = bar.SlotX + (slot-chartBarWidth)/2
		if peak > 0 && day.Lists > 0 {
			height := max(1, plot*float64(day.Lists)/float64(peak))
			bar.Path = roundedColumn(bar.X, baseline-height, chartBarWidth, baseline, min(chartRadius, height))
		}
		c.Bars = append(c.Bars, bar)
	}
	for i := range c.Bars {
		if peak > 0 && c.Bars[i].Lists == peak {
			c.Peak = &c.Bars[i]
		}
	}
	return c
}

// roundedColumn draws a column rounded at its top and square on the baseline.
func roundedColumn(x, top, width, baseline, radius float64) string {
	return fmt.Sprintf("M%.1f %.1f V%.1f A%.1f %.1f 0 0 1 %.1f %.1f H%.1f A%.1f %.1f 0 0 1 %.1f %.1f V%.1f Z",
		x, baseline, top+radius, radius, radius, x+radius, top, x+width-radius, radius, radius, x+width, top+radius, baseline)
}
