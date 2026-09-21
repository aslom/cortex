package tui

import (
	"strings"
	"testing"

	"github.com/rossoctl/cortex/authbridge/authlib/usage"
)

// The cent arithmetic, at the boundaries that made #1042 write it out by hand rather than reach
// for %.2f.
//
// INHERITED FROM #1077 AND RETUNED, not rewritten: that PR's table is the right table and its
// negative cases are the ones review found. What changed under it is the RULE, not the arithmetic
// — abctl now renders every money figure in cents, so the four-decimal form those cases expected
// no longer exists and a negative is shown in cents with its sign.
func TestFormatUSDMicros_RoundsHalfUpOnTheInteger(t *testing.T) {
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
		// NEGATIVES, which the float path cannot reach: MicrosFromUSD rejects one before the
		// arithmetic sees it. Bare, the integer arithmetic truncates toward zero and produced
		// "$0.-15" and "$-12.-34" — and anything above -5_000 rounded to "$0.00", claiming the
		// traffic was free. The sign is kept and the magnitude rendered in cents, with the same
		// floor a positive gets, so an impossible figure is visibly impossible while NAMING it
		// stays the caller's job.
		{"a small negative is not rounded to free", -1, "-<$0.01"},
		{"a negative under half a cent is not free either", -4_999, "-<$0.01"},
		{"a negative does not truncate into $0.-15", -150_000, "-$0.15"},
		{"a negative dollar figure keeps one sign", -12_345_678, "-$12.35"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatUSDMicros(tc.micros); got != tc.want {
				t.Errorf("formatUSDMicros(%d) = %q, want %q", tc.micros, got, tc.want)
			}
			// The float64 entry point must agree for every figure it can express, which is the
			// whole reason it converts through MicrosFromUSD rather than multiplying by 100: that
			// conversion rounds, so it recovers the integer the float came from. It cannot express
			// a negative, so those belong to the integer path alone.
			if tc.micros >= 0 {
				if got := formatUSDCell(float64(tc.micros) / 1e6); got != tc.want {
					t.Errorf("formatUSDCell(%v) = %q, want %q", float64(tc.micros)/1e6, got, tc.want)
				}
			}
		})
	}
}

// An unrepresentable figure is not this formatter's to name. Every surface showing money has its
// own word for one — "unavailable", a clamp marker, an em dash — and a third spelling here would
// hide theirs, so it shows whatever the figure is and leaves the naming alone.
func TestFormatUSDCell_LeavesTheImpossibleToItsCaller(t *testing.T) {
	if got := formatUSDCell(-5); !strings.Contains(got, "5") || !strings.Contains(got, "-") {
		t.Errorf("formatUSDCell(-5) = %q, want the figure and its sign rather than a verdict", got)
	}
	// And it never produces either malformed shape the integer arithmetic can: a second sign
	// inside the number, or a zero that reads as free.
	for _, micros := range []int64{-1, -4_999, -150_000, -12_345_678} {
		got := formatUSDMicros(micros)
		if strings.Contains(got, ".-") {
			t.Errorf("formatUSDMicros(%d) = %q, which puts a sign inside the number", micros, got)
		}
		if got == "$0.00" || got == "-$0.00" {
			t.Errorf("formatUSDMicros(%d) = %q, which claims the traffic was free", micros, got)
		}
	}
}

// ONE PRECISION, ON BOTH SIDES OF A BOUNDARY THAT NO LONGER EXISTS.
//
// #1077 asserted the opposite here — span totals in cents, per-item figures in four decimals — on
// the reasoning that a single cache-read request is $0.000038 and cents would render every one of
// them as nothing. This branch resolves that differently: the floor renders such a charge
// "<$0.01", which says less than "$0.0038" did but says it honestly, and the four-decimal column
// it replaces is the thing an operator asked to be rid of. Measured on a live proxy while
// deciding: no session and no model had a figure under a cent, and the mean per-request cost was
// $0.1159.
//
// The test is kept rather than deleted because its SUBJECT is still live and still worth pinning —
// one figure must render identically wherever it appears. Only the expectation is inverted.
func TestPrecisionRule_OneCentPrecisionEverywhere(t *testing.T) {
	// The figure #1077 used, from the model column where it found its regression.
	const usd = 1.0601

	if got := moneyAmount(usd, 0, 0, 0, nil, false, false); got != "$1.06" {
		t.Errorf("a money figure rendered %q, want cents", got)
	}

	// And through the drawer's own row builder, which is the live caller #1077 found: its
	// per-model row is the surface that used to differ from the band above it.
	figs := drawerFigures(drawerRow{
		label:  "claude-opus-5",
		counts: usage.Counts{CostMicros: 1_060_100, PricedRequests: 11, PriceableRequests: 11},
	})
	var joined string
	for _, f := range figs {
		joined += f.full + " "
	}
	if !strings.Contains(joined, "$1.06") {
		t.Errorf("the drawer's model row does not read in cents: %q", joined)
	}
	if strings.Contains(joined, "$1.0601") {
		t.Errorf("the drawer's model row still carries four decimals: %q", joined)
	}
}

// The markers ride on the amount, since they are a claim ABOUT the figure rather than a part of
// it. Splitting formatting out of moneyAmount must not have dropped them — #1077's point, which
// survives the rule change unaltered.
func TestMarkMoney_WrapsTheFormattedAmount(t *testing.T) {
	deg := &usage.Degraded{}
	want := damagedMarker + inexactMarker + "$1.06" + partialMarker
	if got := moneyAmount(1.0601, 1, 4, 1, deg, true, false); got != want {
		t.Errorf("a marked figure = %q, want %q", got, want)
	}
	// alsoPartial is this branch's second route to the same marker — a window reaching past the
	// ledger's retention — and it must compose identically with no unpriced gap in sight.
	if got := moneyAmount(1.0601, 0, 0, 0, nil, false, true); got != "$1.06"+partialMarker {
		t.Errorf("a retention-shortfall figure = %q, want the partial marker", got)
	}
}
