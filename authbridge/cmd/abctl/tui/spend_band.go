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
// GROUPED BY THE SPAN EACH FIGURE COVERS: the day's figures first, then the rolling window's,
// and every window cell says which window it is.
//
// The order used to be TODAY, LAST 1H, SAVED, CACHE HIT, TOKENS — day, window, day, window,
// window — with only the money cells labelled. An operator reading it left to right had no way
// to know that TOKENS and CACHE HIT covered the last hour while TODAY covered the day, and the
// question that produced this change was exactly that: why does TOKENS say 6.4M when the
// sessions below it each show a hundred times more. They were an hour against a lifetime.
//
// Both halves of the fix are needed. Grouping alone still relies on the reader inferring where
// one span ends; suffixing alone leaves a reader's eye crossing spans twice on its way along
// the row. Together the row reads as two runs, each labelled.
func renderSpendBand(s spendSummary, width int) []string {
	// THE SUFFIX, COMPUTED ONCE, and EMPTY when there is no label to suffix with.
	//
	// spendSummary.WindowLabel is set only when parseWindowSpan could read snap.Window, so an
	// unreadable window — or the Failed path, which builds a summary with no label at all —
	// leaves it empty while every other field is populated. renderSpendStrip guards its own use
	// with `s.WindowLabel != ""` and says an unlabelled group is the honest answer there.
	//
	// Concatenated unguarded, each label gained a TRAILING SPACE: "LAST ", "SAVED ", "TOKENS ",
	// "CACHE HIT ". bandCell.width() is max(label, value), so that space costs a column in
	// exactly the cells whose LABEL is the wider half — TOKENS (6 against "5.6M") and CACHE HIT
	// (9 against "93%"). LAST and SAVED concatenated unguarded too, since before the band was
	// grouped by span, but their money values are wider than their labels and absorbed it. All
	// four go through one variable so the distinction stops mattering.
	//
	// Alignment survived either way — labels and values share the pad — and the final cell's
	// trailing space is trimmed off the line. That is why this was invisible: the cost is wasted
	// width, never a misplaced figure.
	suffix := ""
	if s.WindowLabel != "" {
		suffix = " " + strings.ToUpper(s.WindowLabel)
	}
	var cells []bandCell
	if s.HasToday {
		cells = append(cells, bandCell{"TODAY", moneyAmount(s.TodayUSD,
			s.TodayUnpriced, s.TodayPriceable, s.TodayIncomplete, s.TodayDegraded, s.TodayClamped)})
	}
	// THE SAVING MUST MATCH THE SPAN OF THE FIGURE IT SITS BESIDE, which is the whole point of
	// spendSummary.TodaySavedUSD. Reading the WINDOW's avoided spend into a cell beside TODAY
	// made the two a pair that spanned two spans — measured on a local proxy at "$64.1765
	// today" beside "~$1.0291" for a day that had really avoided $2.1891, understating the
	// figure next to it by 2.1x.
	//
	// So the day's saving joins the day's group HERE, and the window's fallback is appended
	// below with the window's — its span decides where it sits, not its name. The fallback
	// exists for a deployment with no durable ledger (Kubernetes, by design).
	//
	// The marker is part of the value, and the value never joins the spend figures:
	// usage.Counts.AvoidedMicros forbids any consumer adding it, in either direction.
	if s.HasTodaySaved {
		cells = append(cells, bandCell{"SAVED", inexactMarker + formatUSDCell(s.TodaySavedUSD)})
	}
	// Gated on Priced, matching the strip: the window figure exists when the snapshot could
	// price something, and there is no separate HasWindow to consult.
	if s.Priced {
		cells = append(cells, bandCell{
			"LAST" + suffix,
			moneyAmount(s.WindowUSD, s.Unpriced, s.Priceable, s.Incomplete, nil, s.Clamped),
		})
	}
	if !s.HasTodaySaved && s.HasSaved {
		cells = append(cells, bandCell{
			"SAVED" + suffix,
			inexactMarker + formatUSDCell(s.SavedUSD),
		})
	}
	// TOKENS before CACHE HIT, so the two widest window cells are not adjacent at the end where
	// the drop loop reaches first: the volume is what a reader checks the money against, and the
	// hit rate is the one figure here that can be inferred from the tiers in the drawer.
	if s.Tokens > 0 {
		cells = append(cells, bandCell{"TOKENS" + suffix, humanizeCount(s.Tokens)})
	}
	if s.HasCacheHit {
		cells = append(cells, bandCell{"CACHE HIT" + suffix, fmt.Sprintf("%.0f%%", s.CacheHitPct)})
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
