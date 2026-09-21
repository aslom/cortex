package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rossoctl/cortex/authbridge/authlib/pricing"
	"github.com/rossoctl/cortex/authbridge/authlib/usage"
)

// numTierRows is the height of the tier column, in every state.
//
// FOUR, FIXED, because the panel lives in a reservation layout() computes from the terminal
// height and paneView pads to. A renderer whose height follows its data is exactly what
// overflowed this surface by five rows and under-filled it by six, so the count is a
// constant and every state returns exactly this many rows — enforced by the return type
// rather than by a guard, see renderTierRows.
//
// Four and not five: reasoning is a subset of output, not a sibling tier, so a row for it
// would double-count the same money at the most expensive rate there is. It stays in
// `abctl cost`'s token line.
const numTierRows = pricing.NumTiers

// tierBarWidth is the widest a bar may be. Bars are decoration over a figure that is
// already printed, so they yield their space before the figures do.
const tierBarWidth = 12

// tierLabelWidth is the label column. Sized to "cache-write", the longest of the four, so
// the bars start at one column whatever the mix.
const tierLabelWidth = 11

// tierLabels names the rate tiers for a reader.
//
// FIXED, not derived through seriesLetter: that helper exists because model names are
// open-ended and need de-duplication, while the tier set is closed and stable. Deriving
// would also collide cache-read with cache-write and leave the dedupe to pick.
var tierLabels = map[pricing.Tier]string{
	pricing.TierInput:      "input",
	pricing.TierCacheWrite: "cache-write",
	pricing.TierCacheRead:  "cache-read",
	pricing.TierOutput:     "output",
}

// tierOrder is the declaration order, which is deliberately NOT the display order.
var tierOrder = [numTierRows]pricing.Tier{
	pricing.TierInput, pricing.TierCacheWrite, pricing.TierCacheRead, pricing.TierOutput,
}

// renderTierRows is the "where the money went" column: one row per rate tier, ranked by what
// it cost, with a proportional bar and the apportioned figure.
//
// SOLID BLOCKS, not the letter marks usage_stacked.go uses. That file rejected shaded blocks
// because adjacent segments of ONE stacked bar were indistinguishable at a glance; here each
// bar occupies its own line beside its own label, so there is no neighbour to disambiguate
// from and a block is both unambiguous and legible. Colour stays decoration — the label
// carries the identity, so this survives a monochrome terminal and a screenshot.
//
// NO FIGURE WEARS inexactMarker, and it is worth saying what was given up. Every figure here IS
// inexact — the mix is the rate table's while the total may be the gateway's — and each one used
// to carry the glyph saying so. It was dropped deliberately: unlike the sessions table and the
// band, this panel has no money column HEADER to move the caveat onto ("WHERE IT WENT" names the
// column, not the figures), so the choice was a glyph on every row or nothing, and nothing won.
//
// What is left to carry it is this comment and the README. If a reader needs to know these are
// apportioned rather than measured, a header for the money column is the thing to add — not the
// per-row marker back.
//
// Figures read in CENTS, like every other scanned money surface; see the precision rule beside
// formatUSDTotal.
func renderTierRows(c usage.Counts, width int) []string {
	tiers, ok := c.ApportionTiers()

	// Ranked by cost, descending, ties broken on the label so two equal tiers do not swap
	// places between polls and make the panel flicker under a reader comparing rows — the
	// same rule rankSeriesByCost states for the model column.
	order := tierOrder
	sort.SliceStable(order[:], func(i, j int) bool {
		a, b := tiers[order[i]], tiers[order[j]]
		if a != b {
			return a > b
		}
		return tierLabels[order[i]] < tierLabels[order[j]]
	})

	var peak int64
	for _, v := range tiers {
		if v > peak {
			peak = v
		}
	}
	budget := tierBarBudget(width)
	// A FIXED-SIZE ARRAY, so the height is guaranteed by the type rather than by a runtime
	// guard. The first version built a slice, padded it to length, and capped it with
	// out[:numTierRows] — and the pad was dead code: the cap re-extended the slice within
	// its preallocated capacity, filling the gap with empty strings on its own. That is a
	// trap rather than a guarantee, because dropping the preallocation turns the cap into a
	// panic. Indexed assignment into an array of the declared length cannot be wrong.
	var out [numTierRows]string
	for i, tier := range order {
		label := tierLabels[tier]
		var row string
		switch {
		case !ok, tiers[tier] == 0:
			// NOT KNOWN HERE, which is what emptyCell means — never $0.00, and never a figure
			// apportioned from a mix that does not exist.
			//
			// THE ZERO CASE IS THE SAME CASE. A tier carrying real money always apportions to
			// at least one micro, so zero means this tier is absent from the modelled mix —
			// and formatUSDCell(0) prints "$0.0000", which asserts the tier was FREE. That is
			// the "$0.00 for a figure that might be unknown" lie this package refuses in
			// sessionMoneyCell and in `abctl cost`'s headline, arriving through a third door.
			// Found by rendering the panel rather than by a test: the fixture populated all
			// four tiers, so no assertion could see it.
			row = fmt.Sprintf("%-*s %s", tierLabelWidth, label, emptyCell)
		case budget > 0:
			row = fmt.Sprintf("%-*s %-*s %s", tierLabelWidth, label,
				budget, tierBar(tiers[tier], peak, budget),
				formatUSDTotalMicros(tiers[tier]))
		default:
			row = fmt.Sprintf("%-*s %s", tierLabelWidth, label,
				formatUSDTotalMicros(tiers[tier]))
		}
		out[i] = clipRow(row, width)
	}
	return out[:]
}

// tierBarBudget is how many cells a bar may use at this width: the full bar on a roomy
// terminal, half on a tight one, none when the figures themselves are all that fit.
//
// Bars go before figures because a bar without its number says only "this one is bigger",
// which the row ORDER already says.
func tierBarBudget(width int) int {
	switch {
	case width >= tierLabelWidth+tierBarWidth+12:
		return tierBarWidth
	case width >= tierLabelWidth+tierBarWidth/2+12:
		return tierBarWidth / 2
	default:
		return 0
	}
}

// tierBar is a proportional bar with an eighth-block tail, so a small non-zero share stays
// visible rather than rounding away to nothing — the reason sessionMoneyCell skips a rung
// that would render a real charge as zero.
func tierBar(v, peak int64, budget int) string {
	if budget <= 0 || peak <= 0 || v <= 0 {
		return ""
	}
	eighths := int(float64(v) / float64(peak) * float64(budget) * 8)
	full, rem := eighths/8, eighths%8
	if full > budget {
		full, rem = budget, 0
	}
	bar := strings.Repeat("█", full)
	if full < budget && rem > 0 {
		bar += string([]rune("▏▎▍▌▋▊▉")[rem-1])
	}
	if bar == "" {
		// A tier with real money in it gets at least a sliver: an empty bar beside a
		// non-zero figure reads as a rendering fault.
		bar = "▏"
	}
	return bar
}

// clipRow is the last resort at a width narrower than one label. Rows here are built from
// fixed-width parts rather than the strip's droppable figures, so there is nothing to give
// up whole — and a reservation that overflows its terminal is the worse failure.
func clipRow(row string, width int) string {
	if width <= 0 {
		return ""
	}
	r := []rune(row)
	if len(r) <= width {
		return row
	}
	return string(r[:width])
}
