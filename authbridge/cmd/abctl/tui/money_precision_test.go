package tui

import (
	"strings"
	"testing"

	"github.com/rossoctl/cortex/authbridge/authlib/usage"
)

// The cent arithmetic, at the boundaries that made #1042 write it out by hand rather than reach
// for %.2f.
func TestFormatUSDTotal_RoundsHalfUpOnTheInteger(t *testing.T) {
	for _, tc := range []struct {
		name   string
		micros int64
		want   string
	}{
		// THE CASE %.2f GETS WRONG. 1_005_000 micros is exactly $1.005, the nearest float64 is
		// 1.00499999…, and %.2f on it prints $1.00. Half-up on the integer gets $1.01.
		{"an exact half rounds up", 1_005_000, "$1.01"},
		{"just under a half rounds down", 1_004_999, "$1.00"},
		{"a whole cent is exact", 1_010_000, "$1.01"},
		{"zero is zero, not a floor", 0, "$0.00"},
		// Positive but under half a cent: never "$0.00", which would claim the traffic was free.
		{"half a cent is the floor", 4_999, "<$0.01"},
		{"one micro is the floor", 1, "<$0.01"},
		{"exactly half a cent rounds up instead", 5_000, "$0.01"},
		{"dollars carry", 159_548_300, "$159.55"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatUSDTotalMicros(tc.micros); got != tc.want {
				t.Errorf("formatUSDTotalMicros(%d) = %q, want %q", tc.micros, got, tc.want)
			}
			// The float64 entry point must agree, which is the whole reason it converts through
			// MicrosFromUSD rather than multiplying by 100: that conversion rounds, so it
			// recovers the integer the float was derived from.
			if got := formatUSDTotal(float64(tc.micros) / 1e6); got != tc.want {
				t.Errorf("formatUSDTotal(%v) = %q, want %q", float64(tc.micros)/1e6, got, tc.want)
			}
		})
	}
}

// An unrepresentable figure is not this formatter's to name. Every surface showing a total has
// its own word for one — "unavailable", a clamp marker — and a third spelling here would hide
// theirs, so it falls through to the four-decimal form and shows whatever the figure is.
func TestFormatUSDTotal_LeavesTheImpossibleToItsCaller(t *testing.T) {
	if got := formatUSDTotal(-5); !strings.Contains(got, "-5") {
		t.Errorf("formatUSDTotal(-5) = %q; a negative must not be rounded into a cent form", got)
	}
}

// THE BOUNDARY ITSELF, across two surfaces, because this is the regression the rule is for.
//
// Span totals read in cents and per-item figures in four decimals. Both used to come out of one
// function that formatted as well as marked, so moving the band to cents moved the drawer's
// PER-MODEL column with it — silently, since the drawer's own tests assert whole rendered rows
// and a narrower figure still matches most of them.
func TestPrecisionRule_SpanTotalsInCentsPerItemInFourDecimals(t *testing.T) {
	// One figure, both sides of the boundary: $1.0601, which is what the model column showed
	// when this was found.
	const usd = 1.0601

	total := moneyTotal(usd, 0, 0, 0, nil, false)
	if total != "$1.06" {
		t.Errorf("a span total rendered %q, want cents", total)
	}

	item := moneyAmount(usd, 0, 0, 0, nil, false)
	if item != "$1.0601" {
		t.Errorf("a per-item figure rendered %q, want four decimals", item)
	}

	// And through the drawer's own row builder, which is the live caller that regressed.
	figs := drawerFigures(drawerRow{
		label:  "claude-opus-5",
		counts: usage.Counts{CostMicros: 1_060_100, PricedRequests: 11, PriceableRequests: 11},
	})
	var joined string
	for _, f := range figs {
		joined += f.full + " "
	}
	if !strings.Contains(joined, "$1.0601") {
		t.Errorf("the drawer's model row lost its four decimals: %q", joined)
	}
}

// The markers ride on either precision, since they are a claim about the figure rather than a
// part of it. Splitting formatting out of moneyAmount must not have dropped them.
func TestMarkMoney_WrapsEitherPrecision(t *testing.T) {
	deg := &usage.Degraded{}
	if got := moneyTotal(1.0601, 1, 4, 1, deg, true); got != damagedMarker+inexactMarker+"$1.06"+partialMarker {
		t.Errorf("a marked span total = %q", got)
	}
	if got := moneyAmount(1.0601, 1, 4, 1, deg, true); got != damagedMarker+inexactMarker+"$1.0601"+partialMarker {
		t.Errorf("a marked per-item figure = %q", got)
	}
}
