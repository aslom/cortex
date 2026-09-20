package tui

import (
	"fmt"
	"strings"
)

// spendBandLines is the band's height: labels, then values.
//
// Constant in every state, for the same reason numTierRows is — layout() reserves it from
// the terminal height and paneView fills it, and a renderer whose height follows its data
// floats the footer or overflows the terminal.
const spendBandLines = 2

// bandGutter separates cells. Two spaces: the columns already separate the readings, and a
// third space costs a whole cell at the widths where cells start dropping.
const bandGutter = 2

// bandCell is one labelled figure. Label and value travel together because the whole point
// of the band is that they occupy the same column.
type bandCell struct{ label, value string }

// width is what the pair needs: the wider of its halves, since they share a column.
func (c bandCell) width() int {
	if n := len([]rune(c.value)); n > len([]rune(c.label)) {
		return n
	}
	return len([]rune(c.label))
}

// renderSpendBand is the always-on spend band: labels ABOVE values, column-aligned.
//
// The strip it replaces interleaved the two — "$3.8402 today" — which reads as a CSV line
// and is why the pane looked unreadable. Stacking costs one row against the strip's single
// line and is the change that fixes legibility.
//
// MARKERS, NOT PROSE. Each money value comes from moneyAmount, so the three disclosure
// glyphs ride on the figures exactly as they do on the strip; moneyFigure's parenthesised
// caveats do not fit two lines. That is not a new loss — fitStripFigures already falls back
// to this same marked form on a narrow terminal, and the marker is the fact while the words
// are the explanation. The Usage pane and `abctl cost` remain the surfaces with room to
// spell it out.
//
// WHOLE CELLS DROP, RIGHT TO LEFT, and nothing is ever clipped: the strip's rule, because a
// truncated figure is a wrong figure. Today is first and so outlives the rest — it is the
// figure the band exists to show.
func renderSpendBand(s spendSummary, width int) []string {
	var cells []bandCell
	if s.HasToday {
		cells = append(cells, bandCell{"TODAY", moneyAmount(s.TodayUSD,
			s.TodayUnpriced, s.TodayPriceable, s.TodayIncomplete, s.TodayDegraded, s.TodayClamped)})
	}
	// Gated on Priced, matching the strip: the window figure exists when the snapshot could
	// price something, and there is no separate HasWindow to consult.
	if s.Priced {
		cells = append(cells, bandCell{
			"LAST " + strings.ToUpper(s.WindowLabel),
			moneyAmount(s.WindowUSD, s.Unpriced, s.Priceable, s.Incomplete, nil, s.Clamped),
		})
	}
	// THE SAVING MUST MATCH THE SPAN OF THE FIGURE IT SITS BESIDE, which is the whole point of
	// spendSummary.TodaySavedUSD. SAVED renders next to TODAY here, so reading the WINDOW's
	// avoided spend into it made the two a pair that spanned two spans — measured on a local
	// proxy at "$64.1765 today" beside "~$1.0291" for a day that had really avoided $2.1891,
	// understating the figure next to it by 2.1x.
	//
	// The window's saving is the FALLBACK, for a deployment with no durable ledger — Kubernetes
	// by design — and it says so in its own label rather than borrowing the day's. An unlabelled
	// fallback here is the original defect with a different number in it.
	//
	// The marker is part of the value, and the value never joins the spend figures:
	// usage.Counts.AvoidedMicros forbids any consumer adding it, in either direction.
	switch {
	case s.HasTodaySaved:
		cells = append(cells, bandCell{"SAVED", inexactMarker + formatUSDCell(s.TodaySavedUSD)})
	case s.HasSaved:
		cells = append(cells, bandCell{
			"SAVED " + strings.ToUpper(s.WindowLabel),
			inexactMarker + formatUSDCell(s.SavedUSD),
		})
	}
	if s.HasCacheHit {
		cells = append(cells, bandCell{"CACHE HIT", fmt.Sprintf("%.0f%%", s.CacheHitPct)})
	}
	if s.Tokens > 0 {
		cells = append(cells, bandCell{"TOKENS", humanizeCount(s.Tokens)})
	}

	// Drop from the right until what remains fits. The LAST cell's gutter is trimmed off the
	// rendered line, so it is not counted against the budget — otherwise a cell that exactly
	// fills the terminal would be dropped for trailing space nobody sees.
	keep := len(cells)
	for keep > 0 {
		total := -bandGutter
		for _, c := range cells[:keep] {
			total += c.width() + bandGutter
		}
		if total <= width {
			break
		}
		keep--
	}
	cells = cells[:keep]

	var labels, values strings.Builder
	for i, c := range cells {
		pad := c.width()
		if i < len(cells)-1 {
			pad += bandGutter
		}
		fmt.Fprintf(&labels, "%-*s", pad, c.label)
		fmt.Fprintf(&values, "%-*s", pad, c.value)
	}
	// Two lines whatever happened, including when nothing survived: an empty band is two
	// blank lines, never zero. See spendBandLines.
	return []string{
		strings.TrimRight(labels.String(), " "),
		strings.TrimRight(values.String(), " "),
	}
}
