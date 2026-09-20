package tui

import (
	"strings"
	"testing"
)

// bandSummary is a healthy poll: today, a window, a saving, a cache hit rate and tokens.
func bandSummary() spendSummary {
	return spendSummary{
		TodayUSD: 3.8402, HasToday: true,
		WindowUSD: 4.5462, WindowLabel: "1h", Priced: true,
		SavedUSD: 0.2091, HasSaved: true,
		CacheHitPct: 93, HasCacheHit: true,
		Tokens: 5_600_000, HasSnapshot: true,
	}
}

// Labels above values, each value starting at its label's column.
//
// THE ALIGNMENT IS THE FEATURE. The strip interleaved the two — "$3.8402 today" — which
// reads as a CSV line and is the reason the pane looked unreadable. A band whose columns do
// not line up has spent a row and bought nothing.
func TestRenderSpendBand_AlignsValuesUnderTheirLabels(t *testing.T) {
	lines := renderSpendBand(bandSummary(), 78)
	if len(lines) != spendBandLines {
		t.Fatalf("lines = %d, want %d: %q", len(lines), spendBandLines, lines)
	}
	labels, values := lines[0], lines[1]
	for _, pair := range []struct{ label, value string }{
		{"TODAY", "$3.8402"},
		{"LAST 1H", "$4.5462"},
		{"SAVED", inexactMarker + "$0.2091"},
		{"CACHE HIT", "93%"},
	} {
		li, vi := strings.Index(labels, pair.label), strings.Index(values, pair.value)
		if li < 0 {
			t.Errorf("label %q missing from %q", pair.label, labels)
			continue
		}
		if vi < 0 {
			t.Errorf("value %q missing from %q", pair.value, values)
			continue
		}
		if li != vi {
			t.Errorf("%q starts at column %d but %q starts at %d — the columns do not line up\n"+
				"  %s\n  %s", pair.label, li, pair.value, vi, labels, values)
		}
	}
}

// The saving keeps its marker and never joins the spend figures.
func TestRenderSpendBand_SavingStaysMarkedAndSeparate(t *testing.T) {
	joined := strings.Join(renderSpendBand(bandSummary(), 78), "\n")
	if !strings.Contains(joined, inexactMarker+"$0.2091") {
		t.Errorf("the saving lost its %q marker:\n%s", inexactMarker, joined)
	}
	// 3.8402 + 0.2091 — the sum usage.Counts.AvoidedMicros forbids in either direction.
	if strings.Contains(joined, "$4.0493") {
		t.Errorf("the saving was added to today's spend:\n%s", joined)
	}
}

// Markers survive into the band, which is the whole disclosure it can carry.
//
// moneyFigure's parenthesised prose does not fit two lines, and fitStripFigures already
// drops to the marked form at a narrow width — so markers-only is the established
// degradation rather than a new loss. A band that dropped them would publish a floor as an
// exact total.
func TestRenderSpendBand_CarriesTheFigureMarkers(t *testing.T) {
	s := bandSummary()
	s.TodayIncomplete = 3            // ~ inexact
	s.Unpriced, s.Priceable = 12, 40 // + partial, on the window figure
	s.Clamped = true                 // ! damaged, on the window figure
	joined := strings.Join(renderSpendBand(s, 100), "\n")

	if !strings.Contains(joined, inexactMarker+"$3.8402") {
		t.Errorf("today's figure lost its inexact marker:\n%s", joined)
	}
	if !strings.Contains(joined, damagedMarker) {
		t.Errorf("the window figure lost its damaged marker:\n%s", joined)
	}
	if !strings.Contains(joined, partialMarker) {
		t.Errorf("the window figure lost its partial marker:\n%s", joined)
	}
}

// Two lines at every width, nothing clipped, and whole cells drop from the right.
func TestRenderSpendBand_HeightIsConstantAndFiguresDropWhole(t *testing.T) {
	for _, w := range []int{1, 8, 20, 30, 40, 60, 78, 120, 200} {
		lines := renderSpendBand(bandSummary(), w)
		if len(lines) != spendBandLines {
			t.Fatalf("width %d: %d lines, want %d", w, len(lines), spendBandLines)
		}
		for i, line := range lines {
			if n := len([]rune(line)); n > w {
				t.Errorf("width %d: line %d is %d runes: %q", w, i, n, line)
			}
		}
		// A figure that survives is never half a figure.
		if strings.Contains(lines[1], "$3.84") && !strings.Contains(lines[1], "$3.8402") {
			t.Errorf("width %d: today's figure was clipped: %q", w, lines[1])
		}
	}
}

// TODAY outlives every other reading: it is the figure the band exists to show.
func TestRenderSpendBand_TodaySurvivesLongest(t *testing.T) {
	var lastWidthWithAny int
	for w := 1; w <= 80; w++ {
		lines := renderSpendBand(bandSummary(), w)
		if strings.TrimSpace(lines[0]) == "" {
			continue
		}
		lastWidthWithAny = w
		if !strings.Contains(lines[0], "TODAY") {
			t.Errorf("width %d: a reading survived but TODAY did not: %q", w, lines[0])
		}
	}
	if lastWidthWithAny == 0 {
		t.Fatal("no width produced any reading, so this asserted nothing")
	}
}

// Nothing to report is still two lines: the reservation is fixed, and a short band floats
// the footer — the defect already fixed twice on this surface.
func TestRenderSpendBand_EmptySummaryStillFillsItsHeight(t *testing.T) {
	lines := renderSpendBand(spendSummary{}, 78)
	if len(lines) != spendBandLines {
		t.Errorf("lines = %d for an empty summary, want %d: %q", len(lines), spendBandLines, lines)
	}
}
