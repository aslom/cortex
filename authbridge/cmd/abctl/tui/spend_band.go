package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
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
//
// lipgloss.Width, never len() and never a rune count — the rule this package states at
// renderSpendStrip and the one footer.go records the cost of breaking. It is not merely style
// here: the values carry markers and an em dash, and the PADDING below measures with the same
// function, so measurement and padding cannot disagree about a cell.
//
// It does NOT fix an East-Asian ambiguous width, and it is worth saying so rather than leaving
// the next reader to assume it did. U+2014 is ambiguous-width, and a terminal under an EA locale
// gives it two columns while lipgloss v1.1.0 reports 1 either way — measured, including with
// go-runewidth's EastAsianWidth forced on, where runewidth says 2 and lipgloss still says 1. So
// an em-dash band under that locale under-counts no matter which of these two functions is used;
// what changes is that the band now under-counts the same way the table, the strip and the footer
// do, instead of in its own private way.
func (c bandCell) width() int {
	if n := lipgloss.Width(c.value); n > lipgloss.Width(c.label) {
		return n
	}
	return lipgloss.Width(c.label)
}

// bandDropOrder is the order cells are given up in as the terminal narrows, FIRST DROPPED
// FIRST. It is deliberately NOT the visual order.
//
// The band reads left to right in ascending span — the hour, the day, the week, the month —
// because that is how a reader zooms out from "right now". Dropping in that direction would
// give up the month first, which is the budget figure and the reason three of these spans
// exist at all. Dropping in reverse would give up the hour, which is the live one.
//
// So the two ends survive and the middle yields: the week goes first, then the hour, leaving
// TODAY and MONTH — "what have I spent today" and "how much of the budget is gone" — as the
// last two readings on a narrow terminal. Every cell still drops WHOLE and nothing is ever
// clipped, which is the rule a truncated money figure breaks.
var bandDropOrder = [numSpendSpans]spendSpan{span7d, spanHour, spanToday, spanMonth}

// renderSpendBand is the always-on spend band: labels ABOVE values, column-aligned, one cell
// per budget span.
//
// FOUR COST CELLS AND NOTHING ELSE, and the exclusion is the design rather than an omission.
// The band used to carry TODAY, LAST 1H, SAVED, CACHE HIT and TOKENS, and three of those five
// had no span on them at all — CACHE HIT and TOKENS were read off the rolling-hour snapshot
// while sitting in a row that opened with TODAY, so an hour's token count read as a day's, with
// nothing on screen to tell a reader otherwise.
//
// IT SUPERSEDES #1074, which fixed the same defect a different way and landed while this was in
// review. That change kept all five cells and suffixed each label with its span — "TOKENS 1H",
// "CACHE HIT 1H" — grouping the row into a day run and a window run. It is a smaller change and
// it does make every cell name its period.
//
// This goes further because the cells were not only mislabelled, they were the wrong cells: an
// operator reads spend against the hour, the day, the week and the month, and the old five could
// reach two of those. Suffixing labels makes a heterogeneous row honest; replacing it with four
// readings of ONE quantity over four periods makes the row comparable, which is what the uniform
// width and right alignment below are for. #1074's divider survives untouched — it is orthogonal
// and closes the top block whatever the band holds.
//
// THE INVARIANT THAT REPLACES THEM: every cell's label names the period its figure covers.
// Cost-only satisfies it trivially, because spendSpanDefs gives each span its label; any cell
// added later has to satisfy it too. The volume readings are not lost — the drawer's tier
// column says the cache story with more detail, the sessions table carries per-session tokens
// and savings, and the Usage pane keeps the full metric set.
//
// MARKERS, NOT PROSE. Each money value comes from moneyAmount, so the three disclosure glyphs
// ride on the figures; moneyFigure's parenthesised caveats do not fit two lines. The marker is
// the fact and the words are the explanation, and `abctl cost` is the surface with room for
// both.
//
// RIGHT-ALIGNED, AT A UNIFORM WIDTH, which is what makes four spans legible. These are four
// readings of the SAME quantity over different periods, so a reader compares them directly —
// and left-flushed in cells of their own widths, "$4.04" and "$703.18" put their decimal points
// four columns apart. One width for every surviving cell with both lines right-aligned puts the
// figures on a fixed stride with their decimal points in one place. Same rule the tables follow,
// for the same reason.
func renderSpendBand(s spendSummary, width int) []string {
	// Cells in VISUAL order, every span present. A span with nothing to say still gets a cell:
	// an em dash under MONTH says "not known here", where a missing column says nothing at all.
	var cells [numSpendSpans]bandCell
	for span := spendSpan(0); span < numSpendSpans; span++ {
		cells[span] = bandSpanCell(spendSpanDefs[span].label, s.Spans[span])
	}

	// DROP BY PRIORITY, RENDER IN VISUAL ORDER. Walk bandDropOrder marking cells gone until
	// what remains fits, then emit the survivors left to right. Dropping from either END is
	// what would cost the month or the hour first; see bandDropOrder.
	var dropped [numSpendSpans]bool
	for i := spendSpan(0); i < numSpendSpans && bandWidth(cells, dropped) > width; i++ {
		dropped[bandDropOrder[i]] = true
	}
	// AND THEN PUT BACK WHAT STILL FITS, most-valued first, because dropping alone is not
	// maximal. Every cell shares one width, so giving up a WIDE cell can leave room for a
	// narrower one that was surrendered earlier — and the loop above has already moved past it.
	//
	// Measured, with a stale TODAY whose label carries an age ("TODAY 12m", nine columns): at any
	// width from 16 to 19 the band drew MONTH alone, while LAST 1H and MONTH together need 16.
	// Three cells' worth of information given up to fit one, with the room for two sitting unused.
	//
	// IN REVERSE DROP ORDER, which is what keeps the priority honest: the last cell dropped is the
	// most valued of those surrendered, so it gets the first chance to come back and a
	// lower-priority cell can never take a higher one's place.
	for i := numSpendSpans - 1; i >= 0; i-- {
		span := bandDropOrder[i]
		if !dropped[span] {
			continue
		}
		dropped[span] = false
		if bandWidth(cells, dropped) > width {
			dropped[span] = true
		}
	}

	// ONE WIDTH FOR EVERY SURVIVOR, measured after the drops: a cell that is gone must not go
	// on widening the ones that remain.
	cw := 0
	for span := spendSpan(0); span < numSpendSpans; span++ {
		if !dropped[span] {
			if w := cells[span].width(); w > cw {
				cw = w
			}
		}
	}

	var labels, values strings.Builder
	first := true
	for span := spendSpan(0); span < numSpendSpans; span++ {
		if dropped[span] {
			continue
		}
		if !first {
			labels.WriteString(strings.Repeat(" ", bandGutter))
			values.WriteString(strings.Repeat(" ", bandGutter))
		}
		first = false
		// padLeft, not Fprintf("%*s"): fmt pads to a RUNE count, so a cell measured in display
		// columns and padded in runes disagree with each other the moment either half of a cell
		// is not plain ASCII.
		labels.WriteString(padLeft(cells[span].label, cw))
		values.WriteString(padLeft(cells[span].value, cw))
	}
	// Two lines whatever happened, including when nothing survived: an empty band is two
	// blank lines, never zero. See spendBandLines.
	return []string{
		strings.TrimRight(labels.String(), " "),
		strings.TrimRight(values.String(), " "),
	}
}

// bandWidth is what these cells render at, at a uniform width, skipping the dropped ones.
//
// The last cell's gutter is not charged, because the rendered line trims it — otherwise a set
// that exactly filled the terminal would lose a cell to trailing space nobody sees.
func bandWidth(cells [numSpendSpans]bandCell, dropped [numSpendSpans]bool) int {
	cw, n := 0, 0
	for span := spendSpan(0); span < numSpendSpans; span++ {
		if dropped[span] {
			continue
		}
		n++
		if w := cells[span].width(); w > cw {
			cw = w
		}
	}
	if n == 0 {
		return 0
	}
	return n*cw + (n-1)*bandGutter
}

// bandSpanCell is one span's cell: its label, and its figure or an em dash.
//
// THE LABEL CARRIES THE STALENESS, when there is any. A wedged chain holding a good old figure
// is otherwise indistinguishable from a current reading — the failure spendStaleAfter exists to
// name, and one this band could not report at all between the strip's deletion and this change:
// spendSummary computed Age and Stale and no renderer read either.
//
// ON THE LABEL RATHER THAN THE VALUE, and not as a fourth marker glyph. The value's markers all
// qualify the FIGURE — it is a floor, it is inexact, it is short — while an age qualifies the
// ANSWER, and it is a duration rather than a claim. A word beside the period it belongs to says
// that better than a symbol: "TODAY 7m" reads as a day figure polled seven minutes ago, which is
// exactly what it is.
//
// PER SPAN, because the four poll fifteen times apart. One age for the whole band would either
// alarm on a healthy month chain between its own five-minute polls, or stay silent while the
// hour chain wedged.
// The label comes from spendSpanDefs via the caller rather than from the reading, because it
// belongs to the span and not to one poll's answer — a reading built anywhere else would
// otherwise render a nameless column.
func bandSpanCell(label string, r spanReading) bandCell {
	if r.Stale {
		label += " " + formatSpendAge(r.Age)
	}
	if r.Unanswerable || r.Failed || !r.Priced {
		// ONE RENDERING FOR THREE CAUSES, deliberately. "this deployment cannot answer this
		// span", "the poll failed" and "nothing here was priced" differ in WHY and not at all
		// in what a reader may conclude: the figure is not known. A cell seven columns wide has
		// no room to distinguish them, and the only alternative to an em dash is a number that
		// is not one. The drawer and `abctl cost` are the surfaces with room to say which.
		return bandCell{label: label, value: emptyCell}
	}
	return bandCell{
		label: label,
		// A SPAN TOTAL, so it reads in cents — every cell here answers "how much has this span
		// cost", which is the side of main's precision rule that compares rows to each other.
		value: markMoneyTotal(r.USD, r.Unpriced, r.Priceable, r.Incomplete, r.Degraded, r.Clamped,
			r.DaysOutsideRetention > 0),
	}
}
