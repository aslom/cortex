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
		{"TODAY", "$3.84"},
		{"LAST 1H", "$4.55"},
		// The value is bare: the marker is on the label now. Matched on the label's PREFIX,
		// as it always was — this fixture has no TodaySaved, so it takes the window fallback
		// and the label is "SAVED 1H~". The marker itself is asserted in
		// TestRenderSpendBand_SavingStaysMarkedAndSeparate.
		{"SAVED", "$0.21"},
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

// The saving is still marked as an estimate — on its LABEL now — and never joins the spend
// figures.
//
// Both halves asserted, because "the marker moved" and "the marker was deleted" produce the same
// band if you only look for its absence on the value.
func TestRenderSpendBand_SavingStaysMarkedAndSeparate(t *testing.T) {
	// THE DAY'S saving, so the label is the bare "SAVED~" rather than the fallback's
	// "SAVED 1H~" — the suffixed form is TestRenderSpendBand_TheWindowSavingFallbackNamesItsSpan's
	// case, and between them both labels are covered. Same figure either way, so the sum check
	// below is unchanged.
	s := bandSummary()
	s.TodaySavedUSD, s.HasTodaySaved = 0.2091, true
	lines := renderSpendBand(s, 78)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(lines[0], "SAVED"+inexactMarker) {
		t.Errorf("the SAVED label lost its %q marker:\n%s", inexactMarker, joined)
	}
	if strings.Contains(lines[1], inexactMarker) {
		t.Errorf("a value carries %q; on this band it belongs on the label:\n%s",
			inexactMarker, joined)
	}
	// 3.8402 + 0.2091 — the sum usage.Counts.AvoidedMicros forbids in either direction.
	if strings.Contains(joined, "$4.05") {
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

	if !strings.Contains(joined, inexactMarker+"$3.84") {
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
		// A figure that survives is never half a figure: the band drops whole cells rather
		// than clipping, so a line mentioning today's figure at all carries all of it.
		//
		// Keyed on "$3", not on the whole figure, and that is what makes it an assertion: a
		// clip produces "$3." or "$3.8", both of which contain "$3" and neither of which
		// contains "$3.84". Written against the figure itself — which is what substituting
		// the cents form into the old expectation produced — it compares a string to itself
		// and cannot fail.
		if strings.Contains(lines[1], "$3") && !strings.Contains(lines[1], "$3.84") {
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

// SAVED is the DAY's saving, because it sits beside TODAY.
//
// The defect #1067 exists to fix, carried into the band: the strip was fixed, but the band is
// what paneView renders, and it read the WINDOW's avoided spend into a cell next to the day's
// cost. Measured on a local proxy: "~$1.0291" beside a day that had really avoided $2.1891,
// understating the figure next to it by 2.1x.
func TestRenderSpendBand_SavedIsTheDaysFigureNotTheWindows(t *testing.T) {
	s := bandSummary()
	s.SavedUSD, s.HasSaved = 1.0291, true           // the window's
	s.TodaySavedUSD, s.HasTodaySaved = 2.1891, true // the day's
	joined := strings.Join(renderSpendBand(s, 120), "\n")

	// Bare figures: the estimate marker is on the label. Which is also why the negative half
	// below has to match the bare figure too — against "~$1.03" it would pass on the marker's
	// absence alone and stop saying anything about which span the cell holds.
	if !strings.Contains(joined, "$2.19") {
		t.Errorf("SAVED is not the day's figure:\n%s", joined)
	}
	if strings.Contains(joined, "$1.03") {
		t.Errorf("SAVED shows the window's figure beside TODAY, understating the day:\n%s", joined)
	}
}

// With no day figure the WINDOW's saving is the fallback — and says so in its own label.
//
// This is the Kubernetes shape: no durable ledger, so there is no day reply to read a saving
// from. An unlabelled fallback here would be the original defect with a different number in it.
func TestRenderSpendBand_TheWindowSavingFallbackNamesItsSpan(t *testing.T) {
	s := bandSummary()
	s.HasToday, s.TodaySavedUSD, s.HasTodaySaved = false, 0, false
	s.SavedUSD, s.HasSaved = 1.0291, true
	s.WindowLabel = "1h"
	lines := renderSpendBand(s, 120)
	joined := strings.Join(lines, "\n")

	if !strings.Contains(joined, "$1.03") {
		t.Errorf("the window saving is missing entirely:\n%s", joined)
	}
	// The span AND the estimate marker, in that order, because the fallback label is where the
	// two compose: "SAVED 1H~". A marker appended before the span suffix would name the span
	// wrongly ("SAVED~ 1H") and still satisfy a test that looked for them separately.
	if !strings.Contains(lines[0], "SAVED 1H"+inexactMarker) {
		t.Errorf("the fallback saving is labelled %q, which does not name its span and mark "+
			"itself as an estimate:\n%s", lines[0], joined)
	}
}

// Neither saving means no cell, rather than a zero one.
func TestRenderSpendBand_NoSavingShowsNoCell(t *testing.T) {
	s := bandSummary()
	s.HasSaved, s.HasTodaySaved = false, false
	joined := strings.Join(renderSpendBand(s, 120), "\n")
	if strings.Contains(joined, "SAVED") {
		t.Errorf("a SAVED cell with no saving to report:\n%s", joined)
	}
}
