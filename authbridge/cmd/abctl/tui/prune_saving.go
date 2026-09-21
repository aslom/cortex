package tui

import (
	"fmt"
	"math"

	"github.com/rossoctl/cortex/authbridge/authlib/costevent"
	"github.com/rossoctl/cortex/authbridge/authlib/pipeline"
)

// This file used to turn tool-prune's byte saving into tokens and dollars.
//
// It no longer does any of that arithmetic. The proxy prices the saving where both halves
// are in hand — the byte delta from the request, the tier and the token ratio from the
// response — and publishes the result in the cost record. Doing it here meant abctl held a
// rate table's worth of assumptions, applied them to base-tier rates that were wrong past a
// long-context threshold, and could disagree with the server after a hot reload swapped the
// table between the event and the render.
//
// What is left is reading a figure and formatting it.

// savingFor returns the saving the proxy attributed to a component, off the response event
// that carries the cost record.
//
// The response event, not the request one: the saving is only priceable once the response
// reveals which prompt tier it came out of, so that is where the priced figure lands. A
// request row reaches it through the same pairing it already does to show token totals.
func savingFor(resp *pipeline.SessionEvent, component string) (costevent.Saving, bool) {
	ev, ok := costevent.Record(resp)
	if !ok {
		return costevent.Saving{}, false
	}
	for _, s := range ev.Avoided {
		if s.Component == component {
			return s, true
		}
	}
	return costevent.Saving{}, false
}

// pruneSavingFor is the tool-prune saving specifically, which is the only one rendered today.
func pruneSavingFor(resp *pipeline.SessionEvent) (costevent.Saving, bool) {
	return savingFor(resp, "tool-prune")
}

// formatCompact renders a token count tersely enough for a table cell: 10577
// becomes "10.6k". Exact below 1000, where the extra digits still fit.
func formatCompact(v float64) string {
	// Thresholds sit where the ROUNDING carries, not at the round number. %.1f turns
	// 999,999,999 into "1000.0M" — seven runes of four-digit millions, one wider than
	// any value on either side of it, and the same carry happens at every boundary
	// ("1000.0k"). Promoting at 999.95 of a tier keeps the widest output six runes
	// ("999.9M"), which is what lets the column's fit guarantee be stated at all.
	switch {
	case v >= 999_950_000:
		// A billion-token session is not hypothetical: the picker showed 547.9M on a
		// day-old one, and the figure is a sum over turns that keeps climbing. Without
		// this tier it reads "10000.0M" — eight runes of five-digit millions, which is
		// both the shape this function exists to avoid and the one that overflows the
		// TOKENS column once fitTableColumns squeezes it on a narrow terminal.
		return fmt.Sprintf("%.1fB", v/1_000_000_000)
	case v >= 999_950:
		return fmt.Sprintf("%.1fM", v/1_000_000)
	case v >= 999.95:
		return fmt.Sprintf("%.1fk", v/1_000)
	default:
		return fmt.Sprintf("%.0f", math.Round(v))
	}
}

// formatUSDAmount renders a dollar amount at FIXED precision, without the "$" — the
// caller places that, since a saving needs it inside the parentheses.
//
// Fixed rather than varied by magnitude, because these amounts are stacked in one column
// and compared down it: "$0.255" above "−$0.0037" misaligns the decimal point and reads as
// though the two figures were measured to different accuracy.
//
// TWO DECIMALS, not four. Four is finer than any decision made from this screen — nobody
// acts on the fourth decimal of a dollar — and it cost width in every money column while
// making the figures harder to compare at a glance. A magnitude-varying ladder was the
// other candidate and is why the previous `formatUSD` existed; it was already dead code by
// the time this changed, for the alignment reason above.
// ROUNDED FROM MICROS, HALF-UP, not by %.2f — because the headline does, and the two disagreed
// about the same money. %.2f rounds the BINARY float, which is a shade under the exact half for
// figures like $1.005, and then rounds half-to-even: renderCostSummary printed "COST $1.01" for
// 1_005_000 micros while this printed "$1.00", and "$10.00" against "$9.99" for 9_995_000.
// usage_render.go's own comment claimed these surfaces "cannot disagree"; they agreed about
// negatives and not about rounding.
//
// The ledger and the aggregator both count in micros, so the integer is the real figure and the
// float is a lossy copy of it. Recovering it with math.Round and rounding half-up from there is
// what makes every money surface answer identically.
//
// BEYOND int64 MICROS IT FALLS BACK, which is reachable rather than theoretical: a saturated
// aggregate carries a near-int64 micros total, and multiplying that dollar figure back up by 1e6
// overflows. %.2f is wrong by less than a cent on a figure already marked as a floor.
func formatUSDAmount(v float64) string {
	const maxMicroDollars = 9e12 // 1e6 x this stays inside int64
	if v > maxMicroDollars || v < -maxMicroDollars {
		return fmt.Sprintf("%.2f", v)
	}
	micros := int64(math.Round(v * 1e6))
	neg := micros < 0
	if neg {
		micros = -micros
	}
	cents := micros / 10_000
	if micros%10_000 >= 5_000 {
		cents++
	}
	if neg {
		return fmt.Sprintf("-%d.%02d", cents/100, cents%100)
	}
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

// usdFloor is the smallest amount two decimal places can state. Anything
// positive below half of it rounds to "0.00".
const usdFloor = 0.01

// formatUSDCell renders a dollar amount for a table cell, with the "$" attached
// and a floor below which it says so rather than rounding to zero.
//
// The floor exists because %.2f renders anything under $0.005 as "$0.00", which reads as
// "this was free" — the exact reading decodeCostEvent (declining a cost of 0) and
// promptCost (declining an unpriced model rather than showing $0.00) both go out of their
// way to avoid. Reintroducing it at the formatting layer would undo both. Reachable on a
// small cache-read-only request: 100 cache-read tokens at a typical rate is $0.000038.
//
// THE FLOOR CARRIES MORE WEIGHT AT TWO DECIMALS THAN IT DID AT FOUR. It used to catch only
// amounts under $0.00005, which is close to nothing; it now catches everything under half a
// cent, which on cache-read-dominated agent traffic is a real share of requests. That is a
// deliberate trade — "<$0.01" says less than "$0.0038" did, but it says it honestly, and
// the alternative is a column of four-decimal figures nobody reads to the end.
func formatUSDCell(v float64) string {
	if v > 0 && v < usdFloor/2 {
		return "<$" + formatUSDAmount(usdFloor)
	}
	return "$" + formatUSDAmount(v)
}
