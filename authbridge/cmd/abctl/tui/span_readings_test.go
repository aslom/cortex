package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/rossoctl/cortex/authbridge/authlib/usage"
)

// These pin behaviour that spanReadings carries and that was, until now, only asserted through
// spendSummary's dead half — TestSpendSummary_ANegativeWindowTotalIsUnpricedNotARefund and
// friends read WindowUSD and Priced, which no renderer touches. A test exercising a write-only
// field proves the arithmetic and nothing about the screen.

// A NEGATIVE TOTAL IS REFUSED, not clamped and not drawn.
//
// The aggregator sums non-negative per-request figures, so a negative can only come from a broken
// producer — and "-$5.00" on a spend band reads as a refund nobody issued. Treated as unpriced,
// which is the honest reading: we do not know what this span cost.
func TestSpanReadings_ANegativeTotalIsUnpricedNotARefund(t *testing.T) {
	for span := spendSpan(0); span < numSpendSpans; span++ {
		def := spendSpanDefs[span]
		t.Run(def.label, func(t *testing.T) {
			m := &model{}
			m.spend.chains[span].snap = &usage.Snapshot{
				Window: def.window,
				Totals: usage.Counts{
					Requests: 10, CostMicros: -5_000_000,
					PricedRequests: 10, PriceableRequests: 10,
				},
				Priced: true,
			}
			got := m.spanReadings()[span]
			if got.Priced {
				t.Errorf("%s: Priced = true on a negative total, so the band would draw it", def.label)
			}
			if got.USD != 0 {
				t.Errorf("%s: USD = %v carried off a negative total", def.label, got.USD)
			}
			// And on screen it is the em dash, never a minus sign.
			line := strings.Join(renderSpendBand(spendSummary{Spans: m.spanReadings()}, 200), "\n")
			if strings.Contains(line, "-$") || strings.Contains(line, "−$") {
				t.Errorf("%s: band drew a negative figure, which reads as a refund:\n%s",
					def.label, line)
			}
			if !strings.Contains(line, emptyCell) {
				t.Errorf("%s: band has no %q for a total it refused:\n%s", def.label, emptyCell, line)
			}
		})
	}
}

// AND A ZERO TOTAL IS CARRIED, which is the other side of the same guard and the easier one to
// "fix" into a bug.
//
// usage.Snapshot.Priced is PricedRequests > 0 and explicitly NOT CostMicros > 0: a window whose
// every request the gateway SETTLED AT ZERO has priced requests and no dollars, and snapshot.go
// requires it to render as a zero figure rather than as "cost unavailable" — those are different
// answers, and only one of them is true here. Tightening this guard to CostMicros > 0 reads like
// defensive hygiene and silently converts a known answer into "not known here".
//
// AT THE READING, not only at the band: the band takes a spanReading, so a test that builds one by
// hand cannot see this guard at all. That is exactly how the first version of this assertion
// passed against the change it was written to reject.
func TestSpanReadings_ASettledFreeWindowIsPricedAtZero(t *testing.T) {
	for span := spendSpan(0); span < numSpendSpans; span++ {
		def := spendSpanDefs[span]
		t.Run(def.label, func(t *testing.T) {
			m := &model{}
			m.spend.chains[span].snap = &usage.Snapshot{
				Window: def.window,
				Totals: usage.Counts{
					Requests: 10, CostMicros: 0,
					PricedRequests: 10, PriceableRequests: 10,
				},
				Priced: true,
			}
			got := m.spanReadings()[span]
			if !got.Priced {
				t.Errorf("%s: Priced = false for a window the gateway settled at zero, so the "+
					"band reports \"cost unavailable\" for traffic whose cost is known to be "+
					"nothing", def.label)
			}
			if got.USD != 0 {
				t.Errorf("%s: USD = %v, want 0", def.label, got.USD)
			}
			// And on screen it is a figure, not the em dash.
			line := strings.Join(renderSpendBand(spendSummary{Spans: m.spanReadings()}, 200), "\n")
			if !strings.Contains(line, "$0.00") {
				t.Errorf("%s: band drew no $0.00 for settled-free traffic:\n%s", def.label, line)
			}
		})
	}
}

// An unpriced answer is not a zero one, which is the distinction every money surface here rests
// on: "nothing could be priced" and "this cost nothing" are different claims and only one of them
// is ever knowable from an empty figure.
func TestSpanReadings_NothingPricedIsNotZero(t *testing.T) {
	m := &model{}
	m.spend.chains[spanToday].snap = &usage.Snapshot{
		Window: usage.WindowToday,
		Totals: usage.Counts{Requests: 10, PriceableRequests: 10},
		Priced: false,
	}
	got := m.spanReadings()[spanToday]
	if got.Priced {
		t.Error("Priced = true on an unpriced answer")
	}
	if got.Unpriced != 10 || got.Priceable != 10 {
		t.Errorf("coverage gap = %d of %d, want 10 of 10", got.Unpriced, got.Priceable)
	}
	line := strings.Join(renderSpendBand(spendSummary{Spans: m.spanReadings()}, 200), "\n")
	if strings.Contains(line, "$0.00") {
		t.Errorf("band rendered $0.00 for an unpriced span, asserting the traffic was free:\n%s", line)
	}
}

// The coverage gap is PRICEABLE minus priced, never Requests minus priced.
//
// Requests counts every proxied response — MCP tool calls, health checks, tunnels — none of which
// can carry a price, so that denominator makes a correctly configured deployment report itself
// permanently incomplete.
func TestSpanReadings_CoverageGapUsesThePriceableDenominator(t *testing.T) {
	m := &model{}
	m.spend.chains[spanHour].snap = &usage.Snapshot{
		Window: "1h",
		Totals: usage.Counts{
			Requests: 318, CostMicros: 4_170_000,
			PricedRequests: 306, PriceableRequests: 318,
		},
		Priced: true,
	}
	got := m.spanReadings()[spanHour]
	if got.Unpriced != 12 {
		t.Errorf("Unpriced = %d, want 12 (318 priceable - 306 priced)", got.Unpriced)
	}
	if got.Priceable != 318 {
		t.Errorf("Priceable = %d, want 318", got.Priceable)
	}
	// A fully priced answer reports no gap rather than a zero one.
	m.spend.chains[spanHour].snap.Totals.PricedRequests = 318
	if got := m.spanReadings()[spanHour]; got.Unpriced != 0 {
		t.Errorf("Unpriced = %d on a fully priced answer, want 0", got.Unpriced)
	}
}

// A failed poll and a snapshot that has not arrived are DIFFERENT STATES, and neither is zero.
// They render alike — there is no room in a seven-column cell to say which — but the reading has
// to keep them apart, because only one of them is worth an operator's attention.
func TestSpanReadings_FailureAndEmptinessAreDistinct(t *testing.T) {
	m := &model{}
	m.spend.chains[spanHour].err = errors.New("dial tcp: connection refused")
	got := m.spanReadings()

	if !got[spanHour].Failed {
		t.Error("Failed = false on a chain whose poll errored")
	}
	if got[spanToday].Failed {
		t.Error("Failed = true on a chain that has simply not answered yet")
	}
	for _, span := range []spendSpan{spanHour, spanToday} {
		if got[span].Priced {
			t.Errorf("%s: Priced = true with no answer", spendSpanDefs[span].label)
		}
	}
}
