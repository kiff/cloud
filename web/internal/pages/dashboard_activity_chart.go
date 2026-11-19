package pages

import (
	"fmt"
	"strings"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
)

// activityChartOutcomes is the stacking order (bottom→top) and legend
// order for the requests-over-time chart. Mirrors the outcome tiles so
// the colours read the same across the Activity view.
var activityChartOutcomes = []struct {
	Key   string
	Label string
}{
	{"allowed", "Allowed"},
	{"blocked", "Blocked"},
	{"approval_required", "Held"},
	{"limit_exceeded", "Rate-limited"},
	{"invalid", "Invalid"},
}

// LegendItem is one entry in the chart legend.
type LegendItem struct {
	Key   string
	Label string
}

// activityChartHasData reports whether the series carries any decisions
// in the window (so the panel can render an honest empty state).
func activityChartHasData(s *apiclient.ActivitySeriesResponse) bool {
	if s == nil {
		return false
	}
	for _, b := range s.Buckets {
		if b.Total > 0 {
			return true
		}
	}
	return false
}

// activityChartLegend returns only the outcomes that actually appear in
// the window, in the canonical order — so the legend never lists an
// outcome the chart doesn't draw.
func activityChartLegend(s *apiclient.ActivitySeriesResponse) []LegendItem {
	present := map[string]bool{}
	if s != nil {
		for _, b := range s.Buckets {
			for k, n := range b.Counts {
				if n > 0 {
					present[k] = true
				}
			}
		}
	}
	out := make([]LegendItem, 0, len(activityChartOutcomes))
	for _, oc := range activityChartOutcomes {
		if present[oc.Key] {
			out = append(out, LegendItem{Key: oc.Key, Label: oc.Label})
		}
	}
	return out
}

// activityChartRangeLabel is the human range shown beside the chart
// title, derived from the resolved window bounds.
func activityChartRangeLabel(v DashboardDomainEvidenceView) string {
	if v.Series == nil || v.Series.From.IsZero() || v.Series.To.IsZero() {
		return "selected window"
	}
	from, to := v.Series.From, v.Series.To
	if to.Sub(from) <= 24*time.Hour {
		return from.Format("Jan 2 15:04") + " – " + to.Format("15:04")
	}
	return from.Format("Jan 2") + " – " + to.Format("Jan 2")
}

// densifyBuckets expands the API's sparse (non-empty-only) buckets into
// a dense, evenly-spaced series from the window start to end, so the
// chart's x-axis is even and gaps read as zero. Aligns the first slot to
// the bucket width, matching the store's alignment.
func densifyBuckets(s *apiclient.ActivitySeriesResponse) []apiclient.ActivityBucket {
	if s == nil || s.BucketSeconds <= 0 {
		return nil
	}
	step := int64(s.BucketSeconds)
	byStart := make(map[int64]apiclient.ActivityBucket, len(s.Buckets))
	for _, b := range s.Buckets {
		byStart[b.Start.Unix()] = b
	}
	start := (s.From.Unix() / step) * step
	end := s.To.Unix()
	// Guard against a pathological (huge) span producing an unbounded
	// loop — cap the number of slots. pickBucket keeps real windows well
	// under this, so hitting the cap only happens on malformed input.
	const maxSlots = 1000
	out := make([]apiclient.ActivityBucket, 0, 128)
	for t := start; t <= end && len(out) < maxSlots; t += step {
		if b, ok := byStart[t]; ok {
			out = append(out, b)
		} else {
			out = append(out, apiclient.ActivityBucket{Start: time.Unix(t, 0).UTC(), Counts: map[string]int{}})
		}
	}
	return out
}

// activityChartSVG renders the requests-over-time series as a native
// stacked-column SVG (the web-stack design: no charting lib, no JS).
// Colours come from CSS classes (ac-bar-<outcome>) so the chart
// respects the dark theme. All emitted strings are server-controlled
// (numeric geometry + formatted timestamps), so the raw embed carries
// no untrusted input.
func activityChartSVG(s *apiclient.ActivitySeriesResponse) string {
	buckets := densifyBuckets(s)
	if len(buckets) == 0 {
		return ""
	}
	const (
		w    = 720.0
		h    = 180.0
		padL = 34.0
		padR = 8.0
		padT = 10.0
		padB = 22.0
	)
	plotW := w - padL - padR
	plotH := h - padT - padB

	maxTotal, grand := 0, 0
	for _, b := range buckets {
		if b.Total > maxTotal {
			maxTotal = b.Total
		}
		grand += b.Total
	}
	if maxTotal == 0 {
		maxTotal = 1
	}

	n := len(buckets)
	slotW := plotW / float64(n)
	barW := slotW * 0.72
	if barW < 1 {
		barW = slotW
	}
	gap := (slotW - barW) / 2

	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="ac-chart" viewBox="0 0 %g %g" preserveAspectRatio="none" role="img" aria-label="Requests over time: %d decisions across %d intervals">`, w, h, grand, n)

	// Horizontal gridlines + y labels at 0 / mid / max.
	yBottom := padT + plotH
	for _, frac := range []float64{0, 0.5, 1} {
		y := padT + plotH*(1-frac)
		val := int(float64(maxTotal)*frac + 0.5)
		fmt.Fprintf(&b, `<line class="ac-grid" x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f"/>`, padL, y, w-padR, y)
		fmt.Fprintf(&b, `<text class="ac-axis ac-axis-y" x="%.1f" y="%.1f">%d</text>`, padL-4, y+3, val)
	}

	// Stacked bars, bottom→top in outcome order.
	for i, bk := range buckets {
		x := padL + float64(i)*slotW + gap
		acc := 0
		for _, oc := range activityChartOutcomes {
			c := bk.Counts[oc.Key]
			if c == 0 {
				continue
			}
			segH := plotH * float64(c) / float64(maxTotal)
			acc += c
			yTop := yBottom - plotH*float64(acc)/float64(maxTotal)
			fmt.Fprintf(&b, `<rect class="ac-bar ac-bar-%s" x="%.2f" y="%.2f" width="%.2f" height="%.2f"><title>%s</title></rect>`,
				oc.Key, x, yTop, barW, segH, chartSegTitle(bk.Start, oc.Label, c, s.BucketSeconds))
		}
	}

	// x-axis time labels at start / middle / end.
	labelIdx := []int{0, n / 2, n - 1}
	seen := map[int]bool{}
	for _, idx := range labelIdx {
		if idx < 0 || idx >= n || seen[idx] {
			continue
		}
		seen[idx] = true
		x := padL + float64(idx)*slotW + slotW/2
		anchor := "middle"
		switch idx {
		case 0:
			anchor = "start"
			x = padL
		case n - 1:
			anchor = "end"
			x = w - padR
		}
		fmt.Fprintf(&b, `<text class="ac-axis ac-axis-x" x="%.1f" y="%.1f" text-anchor="%s">%s</text>`,
			x, h-6, anchor, chartXLabel(buckets[idx].Start, s.BucketSeconds))
	}

	b.WriteString(`</svg>`)
	return b.String()
}

// chartXLabel formats a bucket start for the x-axis: time-of-day for
// sub-day buckets, calendar date for coarser ones.
func chartXLabel(t time.Time, bucketSeconds int) string {
	if bucketSeconds < 24*3600 {
		return t.Format("15:04")
	}
	return t.Format("Jan 2")
}

// chartSegTitle is the hover tooltip on a stacked segment: the interval
// start, the outcome, and the count.
func chartSegTitle(t time.Time, label string, count, bucketSeconds int) string {
	stamp := t.Format("Jan 2 15:04")
	if bucketSeconds >= 24*3600 {
		stamp = t.Format("Jan 2")
	}
	return fmt.Sprintf("%s · %s: %d", stamp, label, count)
}
