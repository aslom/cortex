package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/rossoctl/cortex/authbridge/authlib/usage"
)

// bandSummary is a healthy four-span answer, with figures of four different magnitudes so the
// alignment assertions have something to line up. The numbers are the shape a live proxy
// produced: an hour inside a day inside a week inside a month.
func bandSummary() spendSummary {
	var s spendSummary
	for span, usd := range map[spendSpan]float64{
		spanHour:  4.04,
		spanToday: 18.7994,
		span7d:    216.44,
		spanMonth: 703.18,
	} {
		s.Spans[span] = spanReading{

			USD:       usd,
			Priced:    true,
			Priceable: 100,
		}
	}
	return s
}

// bandSpan pulls one cell's reading out for mutation in a test.
func withSpan(s spendSummary, span spendSpan, mutate func(*spanReading)) spendSummary {
	r := s.Spans[span]
	mutate(&r)
	s.Spans[span] = r
	return s
}

// EVERY SPAN IS ON THE BAND, AND EVERY ONE NAMES ITSELF. That is the invariant the band exists
// to hold: the defect it replaces had CACHE HIT and TOKENS read off the rolling-hour snapshot
// while sitting in a row that opened with TODAY, so an hour's figures read as a day's.
func TestRenderSpendBand_EverySpanIsLabelledWithItsPeriod(t *testing.T) {
	lines := renderSpendBand(bandSummary(), 200)
	if len(lines) != spendBandLines {
		t.Fatalf("%d lines, want %d", len(lines), spendBandLines)
	}
	for span := spendSpan(0); span < numSpendSpans; span++ {
		label := spendSpanDefs[span].label
		if !strings.Contains(lines[0], label) {
			t.Errorf("label row %q is missing %q", lines[0], label)
		}
	}
	// And the figures are all there, under them.
	for span := spendSpan(0); span < numSpendSpans; span++ {
		want := formatUSDCell(bandSummary().Spans[span].USD)
		if !strings.Contains(lines[1], want) {
			t.Errorf("value row %q is missing %s's figure %q",
				lines[1], spendSpanDefs[span].label, want)
		}
	}
}

// ASCENDING SPAN, LEFT TO RIGHT, because that is how a reader zooms out from "right now" — and
// because the drop order depends on it being the visual order and nothing else.
func TestRenderSpendBand_ReadsOutwardsFromTheLiveHour(t *testing.T) {
	lines := renderSpendBand(bandSummary(), 200)
	at := -1
	for span := spendSpan(0); span < numSpendSpans; span++ {
		label := spendSpanDefs[span].label
		i := strings.Index(lines[0], label)
		if i < 0 {
			t.Fatalf("label row %q is missing %q", lines[0], label)
		}
		if i <= at {
			t.Errorf("%q appears at %d, not after the previous span at %d — the band must read "+
				"in ascending span order:\n%s", label, i, at, lines[0])
		}
		at = i
	}
}

// THE FIGURES LINE UP, which is the whole reason four spans are legible together.
//
// They are four readings of the same quantity over different periods, so a reader compares them
// directly — and left-flushed in cells of their own widths, "$4.04" and "$703.18" put their
// decimal points four columns apart. Asserted as a fixed STRIDE rather than by eye: every cell
// is one width, so each figure's last character sits a constant distance from the previous.
func TestRenderSpendBand_FiguresShareAColumnStride(t *testing.T) {
	lines := renderSpendBand(bandSummary(), 200)
	var ends []int
	for span := spendSpan(0); span < numSpendSpans; span++ {
		fig := formatUSDCell(bandSummary().Spans[span].USD)
		i := strings.Index(lines[1], fig)
		if i < 0 {
			t.Fatalf("value row %q is missing %q", lines[1], fig)
		}
		ends = append(ends, i+len([]rune(fig)))
	}
	stride := ends[1] - ends[0]
	for i := 2; i < len(ends); i++ {
		if got := ends[i] - ends[i-1]; got != stride {
			t.Errorf("figure %d ends %d columns after the previous one, but figure 1 ended %d "+
				"after figure 0 — the cells are not one width, so the decimal points do not "+
				"line up:\n%s\n%s", i, got, stride, lines[0], lines[1])
		}
	}
	// And the labels share it too: both lines are right-aligned in the same cells, so a label
	// sits over the digits of its own figure rather than over the column's left edge.
	for span := spendSpan(0); span < numSpendSpans; span++ {
		label := spendSpanDefs[span].label
		li := strings.Index(lines[0], label) + len([]rune(label))
		if li != ends[span] {
			t.Errorf("%q ends at column %d but its figure ends at %d — the heading is not over "+
				"its own value:\n%s\n%s", label, li, ends[span], lines[0], lines[1])
		}
	}
}

// Each of the three disclosure glyphs still rides on the figure it qualifies.
func TestRenderSpendBand_CarriesTheFigureMarkers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*spanReading)
		want   string
	}{
		{"inexact", func(r *spanReading) { r.Incomplete = 3 }, inexactMarker},
		{"partial", func(r *spanReading) { r.Unpriced, r.Priceable = 40, 100 }, partialMarker},
		{"damaged", func(r *spanReading) { r.Clamped = true }, damagedMarker},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := withSpan(bandSummary(), spanToday, tc.mutate)
			joined := strings.Join(renderSpendBand(s, 200), "\n")
			if !strings.Contains(joined, tc.want) {
				t.Errorf("band lost the %q marker:\n%s", tc.want, joined)
			}
		})
	}
}

// A SPAN THIS DEPLOYMENT CANNOT ANSWER READS AS AN EM DASH, never as a number.
//
// With no cost ledger the server answers a symbolic window from the six-hour ring instead and
// reports the window it actually served. Drawing that under a MONTH label would understate the
// month by about 120x while looking perfectly well-formed, which is the one thing every money
// surface here refuses.
func TestRenderSpendBand_AnUnanswerableSpanIsNotANumber(t *testing.T) {
	s := withSpan(bandSummary(), spanMonth, func(r *spanReading) {
		r.Unanswerable = true
		r.USD, r.Priced = 703.18, true // the figure is there and must NOT be drawn
	})
	lines := renderSpendBand(s, 200)
	if !strings.Contains(lines[0], "MONTH") {
		t.Errorf("label row %q dropped MONTH; silence about a span is worse than saying it is "+
			"unavailable:\n%s", lines[0], strings.Join(lines, "\n"))
	}
	if strings.Contains(lines[1], "$703.18") {
		t.Errorf("value row %q drew a figure for a span the deployment cannot answer:\n%s",
			lines[1], strings.Join(lines, "\n"))
	}
	if !strings.Contains(lines[1], emptyCell) {
		t.Errorf("value row %q has no %q for the unanswerable span", lines[1], emptyCell)
	}
	// The spans that CAN be answered are untouched: one degraded cell must not blank the band.
	if !strings.Contains(lines[1], formatUSDCell(4.04)) {
		t.Errorf("value row %q lost the hour figure over the month's degradation", lines[1])
	}
}

// A failed poll and an unpriced span read the same way, and neither blanks its neighbours —
// which is what the per-span chains were split for.
func TestRenderSpendBand_OneChainsFailureLeavesTheOthers(t *testing.T) {
	s := withSpan(bandSummary(), span7d, func(r *spanReading) {
		r.Failed, r.Priced = true, false
	})
	lines := renderSpendBand(s, 200)
	if !strings.Contains(lines[1], emptyCell) {
		t.Errorf("value row %q has no %q for the failed span", lines[1], emptyCell)
	}
	for _, span := range []spendSpan{spanHour, spanToday, spanMonth} {
		want := formatUSDCell(bandSummary().Spans[span].USD)
		if !strings.Contains(lines[1], want) {
			t.Errorf("value row %q lost %s over another span's failure",
				lines[1], spendSpanDefs[span].label)
		}
	}
}

// A WEDGED CHAIN SAYS SO, on the label of the span that wedged.
//
// This is the disclosure that went missing when the band replaced the strip: spendSummary
// computed Age and Stale and no renderer read either, so a poll chain that stopped answering
// looked exactly like a current reading. It is per span because the four poll fifteen times
// apart — one age for the band would alarm on a healthy month or stay silent on a wedged hour.
func TestRenderSpendBand_AStaleSpanIsDated(t *testing.T) {
	s := withSpan(bandSummary(), spanToday, func(r *spanReading) {
		r.Stale, r.Age = true, 7*time.Minute
	})
	lines := renderSpendBand(s, 200)
	if !strings.Contains(lines[0], "TODAY "+formatSpendAge(7*time.Minute)) {
		t.Errorf("label row %q does not date the stale span; a wedged chain is indistinguishable "+
			"from a current reading without it:\n%s", lines[0], strings.Join(lines, "\n"))
	}
	// The figure itself is unchanged: the answer is old, not wrong.
	if !strings.Contains(lines[1], formatUSDCell(18.7994)) {
		t.Errorf("value row %q dropped a figure that was merely stale", lines[1])
	}
	// And a fresh band carries no timestamp at all — a permanent one is noise.
	fresh := strings.Join(renderSpendBand(bandSummary(), 200), "\n")
	if strings.Contains(fresh, "ago") || strings.Contains(fresh, formatSpendAge(7*time.Minute)) {
		t.Errorf("a healthy band carries an age:\n%s", fresh)
	}
}

// Height is constant, nothing is ever clipped, and no line exceeds the terminal.
func TestRenderSpendBand_HeightIsConstantAndFiguresDropWhole(t *testing.T) {
	for _, w := range []int{200, 120, 78, 60, 40, 34, 30, 20, 8, 1} {
		lines := renderSpendBand(bandSummary(), w)
		if len(lines) != spendBandLines {
			t.Fatalf("width %d: %d lines, want %d", w, len(lines), spendBandLines)
		}
		for i, line := range lines {
			if n := lipgloss.Width(line); n > w {
				t.Errorf("width %d: line %d is %d columns: %q", w, i, n, line)
			}
		}
		// A figure that survives is never half a figure.
		//
		// THROUGH clipsFigure rather than spelled out here, because the detector is the part
		// that can be silently wrong: this assertion once read Contains("$3.84") &&
		// !Contains("$3.8402"), and retuning the literals for a two-decimal formatter collapsed
		// both sides onto "$3.84" — an `x && !x` that could never fire. The detector has its own
		// guard now; see TestClipsFigure_CanActuallyFail.
		for span := spendSpan(0); span < numSpendSpans; span++ {
			whole := formatUSDCell(bandSummary().Spans[span].USD)
			if clipsFigure(lines[1], whole) {
				t.Errorf("width %d: %s's figure was clipped: %q (probe %q, want whole %q)",
					w, spendSpanDefs[span].label, lines[1], figureProbe(whole), whole)
			}
		}
	}
}

// THE THREE ANSWERS A MONEY CELL CAN GIVE, pinned together because each one is a different truth
// and two of them look alike.
//
//	unpriced            an em dash      nothing here carried a cost figure
//	priced at zero      "$0.00"         every request was settled free
//	priced below a cent "<$0.01"        a real charge, too small to state to the cent
//
// THE MIDDLE ONE IS DELIBERATE AND IS NOT THE $0.00 THIS PACKAGE REFUSES ELSEWHERE.
// usage.Snapshot.Priced is PricedRequests > 0 and explicitly NOT CostMicros > 0, because a window
// whose every request the gateway SETTLED AT ZERO has priced requests and no dollars: snapshot.go
// requires that to render as a zero figure rather than as "cost unavailable", and they are
// different answers. sessionMoneyCell and the tier column do use the em dash for their own zeros,
// but those are per-session and per-tier apportionments where a zero means "absent from the mix" —
// an aggregate of settled-free traffic means the traffic was free, and withholding it would report
// an answer we have as one we do not.
//
// The reading that WOULD be a lie is the third row, and it is refused one layer down:
// formatUSDCell floors anything positive under half a cent to "<$0.01". That is why the cell can
// print $0.00 only for an exact zero — and why both are asserted here, since a reader probing a
// $0.00 cell cannot otherwise tell which of the two produced it.
//
// The expected strings are LITERALS, not formatUSDCell calls: this is a contract about what an
// operator reads, and deriving the expectation from the formatter would let a formatter change
// take both sides with it. That is the vacuity TestClipsFigure_CanActuallyFail documents.
func TestRenderSpendBand_UnpricedZeroAndSubCentAreThreeDifferentCells(t *testing.T) {
	cell := func(s spendSummary) string { return renderSpendBand(s, 200)[1] }

	unpriced := cell(withSpan(bandSummary(), spanHour, func(r *spanReading) {
		r.USD, r.Priced = 0, false
	}))
	if !strings.Contains(unpriced, emptyCell) {
		t.Errorf("an unpriced hour rendered %q with no %q: priced:false means cost unavailable, "+
			"and a figure in its place claims knowledge the snapshot disclaims",
			unpriced, emptyCell)
	}
	if strings.Contains(unpriced, "$0.00") {
		t.Errorf("an unpriced hour rendered %q, which reads as free traffic", unpriced)
	}

	free := cell(withSpan(bandSummary(), spanHour, func(r *spanReading) {
		r.USD, r.Priced = 0, true
	}))
	if !strings.Contains(free, "$0.00") {
		t.Errorf("a settled-free hour rendered %q, want $0.00: the gateway priced every request "+
			"at nothing, which is an answer and not an absence", free)
	}

	subCent := cell(withSpan(bandSummary(), spanHour, func(r *spanReading) {
		r.USD, r.Priced = 0.003, true
	}))
	if !strings.Contains(subCent, "<$0.01") {
		t.Errorf("a $0.003 hour rendered %q, want <$0.01", subCent)
	}
	if strings.Contains(subCent, "$0.00") {
		t.Errorf("a $0.003 hour rendered %q: a known non-zero charge shown as free is the one "+
			"rounding this band is not allowed to do", subCent)
	}
}

// TODAY AND MONTH OUTLIVE THE OTHER TWO, which is the drop order the band needs and NOT the
// order it reads in.
//
// Dropping right-to-left would give up MONTH first — the budget figure, and the reason three of
// these spans exist. Dropping left-to-right would give up the live hour. So the middle yields:
// the week first, then the hour.
func TestRenderSpendBand_TodayAndMonthSurviveLongest(t *testing.T) {
	// Narrow until only two cells remain, then check which two.
	var twoLeft []string
	for w := 60; w >= 1; w-- {
		lines := renderSpendBand(bandSummary(), w)
		n := 0
		for span := spendSpan(0); span < numSpendSpans; span++ {
			if strings.Contains(lines[0], spendSpanDefs[span].label) {
				n++
			}
		}
		if n == 2 {
			twoLeft = lines
			break
		}
	}
	if twoLeft == nil {
		t.Fatal("no width leaves exactly two cells, so the drop order asserted nothing")
	}
	for _, want := range []spendSpan{spanToday, spanMonth} {
		if !strings.Contains(twoLeft[0], spendSpanDefs[want].label) {
			t.Errorf("the last two cells are %q, and %q is not among them", twoLeft[0],
				spendSpanDefs[want].label)
		}
	}
	for _, gone := range []spendSpan{spanHour, span7d} {
		if strings.Contains(twoLeft[0], spendSpanDefs[gone].label) {
			t.Errorf("the last two cells are %q, which still includes %q", twoLeft[0],
				spendSpanDefs[gone].label)
		}
	}
	// The week goes FIRST of all, which is the other half of the order.
	for w := 200; w >= 1; w-- {
		lines := renderSpendBand(bandSummary(), w)
		if !strings.Contains(lines[0], spendSpanDefs[span7d].label) {
			for _, still := range []spendSpan{spanHour, spanToday, spanMonth} {
				if !strings.Contains(lines[0], spendSpanDefs[still].label) {
					t.Errorf("at width %d, %q went before or with the week: %q", w,
						spendSpanDefs[still].label, lines[0])
				}
			}
			break
		}
	}
}

// An empty summary still fills its reserved height, because layout() has already held the rows
// back — see spendBandLines. Blank lines, never zero lines.
func TestRenderSpendBand_EmptySummaryStillFillsItsHeight(t *testing.T) {
	lines := renderSpendBand(spendSummary{}, 200)
	if len(lines) != spendBandLines {
		t.Fatalf("%d lines, want %d", len(lines), spendBandLines)
	}
	// Every span reports "not known here" rather than a zero, and still names itself: a band
	// of em dashes is an honest answer, a band of "$0.00" is a claim that nothing was spent.
	for span := spendSpan(0); span < numSpendSpans; span++ {
		if !strings.Contains(lines[0], spendSpanDefs[span].label) {
			t.Errorf("label row %q dropped %q on an empty summary", lines[0],
				spendSpanDefs[span].label)
		}
	}
	if strings.Contains(lines[1], "$0.00") {
		t.Errorf("value row %q renders $0.00 for spans nothing has answered yet", lines[1])
	}
}

// spanReadings is where the disclosure is decided, so it is asserted directly too: the renderer
// can only draw an em dash if this says the span is unanswerable.
func TestSpanReadings_DetectTheNoLedgerDegradation(t *testing.T) {
	m := &model{}
	// A proxy with no cost ledger: every symbolic window comes back as the ring's span.
	for _, span := range []spendSpan{spanToday, span7d, spanMonth} {
		m.spend.chains[span].snap = &usage.Snapshot{
			Window: "6h0m0s",
			Totals: usage.Counts{Requests: 10, CostMicros: 1_120_000, PricedRequests: 10, PriceableRequests: 10},
			Priced: true,
		}
	}
	// The hour is ring-served and comes back stringified, which is the SAME span and must not
	// be mistaken for a degradation.
	m.spend.chains[spanHour].snap = &usage.Snapshot{
		Window: "1h0m0s",
		Totals: usage.Counts{Requests: 10, CostMicros: 404_000, PricedRequests: 10, PriceableRequests: 10},
		Priced: true,
	}

	got := m.spanReadings()

	for _, span := range []spendSpan{spanToday, span7d, spanMonth} {
		if !got[span].Unanswerable {
			t.Errorf("%s: Unanswerable = false though the server served 6h — a six-hour figure "+
				"under this label understates the span", spendSpanDefs[span].label)
		}
	}
	if got[spanHour].Unanswerable {
		t.Error("the hour is marked unanswerable, but \"1h\" and \"1h0m0s\" are the same span " +
			"spelled by the caller and by Go")
	}
	if !got[spanHour].Priced || got[spanHour].USD != 0.404 {
		t.Errorf("hour reading = %+v, want the priced 0.404 figure", got[spanHour])
	}
}

// servedAsRequested is the whole basis of that disclosure, so its boundary is pinned directly.
func TestServedAsRequested(t *testing.T) {
	for _, tc := range []struct {
		want, served string
		ok           bool
	}{
		{"1h", "1h0m0s", true},   // a duration window, stringified by Go
		{"1h", "1h", true},       // and spelled back verbatim
		{"today", "today", true}, // symbolic, answered
		{"month", "month", true},
		{"7d", "7d", true},
		{"month", "6h0m0s", false}, // the no-ledger degradation
		{"today", "6h0m0s", false},
		{"7d", "6h0m0s", false},
		{"1h", "6h0m0s", false},   // a duration served as a DIFFERENT duration
		{"month", "today", false}, // never equal across symbolic windows
	} {
		if got := servedAsRequested(tc.want, tc.served); got != tc.ok {
			t.Errorf("servedAsRequested(%q, %q) = %v, want %v", tc.want, tc.served, got, tc.ok)
		}
	}
}

// TestSpanReadings_StalenessIsMeasuredAgainstEachSpansOwnCadence.
//
// THE THRESHOLD HAS TO BE PER SPAN, exactly like the readings are, and the plumbing landed
// without it: spanReadings compared every chain's age to the global spendStaleAfter, which is
// twice the HOUR's twenty-second cadence — forty seconds. The month and the week poll every
// five minutes, so for about 87% of every healthy polling cycle they were dated "MONTH 3m",
// which reads as a wedged chain on a chain that answered three minutes ago and is not due for
// another two.
//
// It also cost width. The age widens the label, the band is one uniform cell width, so two
// permanently-dated cells pushed the whole band from 34 columns to 42 — dropping 7 DAYS at a
// width where all four had fit.
//
// bandSpanCell's own doc already argued for this ("PER SPAN, because the four poll fifteen
// times apart. One age for the whole band would … alarm on a healthy month chain between its
// own five-minute polls"). This is that rationale made true.
func TestSpanReadings_StalenessIsMeasuredAgainstEachSpansOwnCadence(t *testing.T) {
	now := time.Now()
	for span := spendSpan(0); span < numSpendSpans; span++ {
		def := spendSpanDefs[span]
		t.Run(def.label, func(t *testing.T) {
			// One interval old: a chain answering on schedule, never stale.
			m := &model{}
			m.spend.chains[span].snap = &usage.Snapshot{
				Window: def.window,
				Totals: usage.Counts{Requests: 1, CostMicros: 1_000_000, PricedRequests: 1, PriceableRequests: 1},
				Priced: true,
			}
			m.spend.chains[span].lastFetch = now.Add(-def.interval)
			if got := m.spanReadings()[span]; got.Stale {
				t.Errorf("%s: stale after one poll interval (%v) — a chain answering on its own "+
					"cadence is healthy, and dating it reads as wedged", def.label, def.interval)
			}
			// Just inside twice its own interval: still healthy, so one dropped reply is not
			// an alarm.
			m.spend.chains[span].lastFetch = now.Add(-2*def.interval + time.Second)
			if got := m.spanReadings()[span]; got.Stale {
				t.Errorf("%s: stale just inside 2x its %v interval; one missed reply must not "+
					"alarm", def.label, def.interval)
			}
			// Past twice its own interval: now it is worth saying.
			m.spend.chains[span].lastFetch = now.Add(-2*def.interval - time.Second)
			got := m.spanReadings()[span]
			if !got.Stale {
				t.Errorf("%s: not stale past 2x its %v interval — a wedged chain is "+
					"indistinguishable from a current reading without this", def.label, def.interval)
			}
			if got.Age < 2*def.interval {
				t.Errorf("%s: Age = %v, want at least 2x the %v interval", def.label, got.Age, def.interval)
			}
		})
	}
}

// And the consequence the global threshold had on the band: a healthy month and week must not
// widen it, because the age rides on the label and the cells share one width.
func TestRenderSpendBand_HealthySlowSpansDoNotWidenTheBand(t *testing.T) {
	now := time.Now()
	m := &model{}
	for span := spendSpan(0); span < numSpendSpans; span++ {
		def := spendSpanDefs[span]
		m.spend.chains[span].snap = &usage.Snapshot{
			Window: def.window,
			Totals: usage.Counts{Requests: 1, CostMicros: 4_040_000, PricedRequests: 1, PriceableRequests: 1},
			Priced: true,
		}
		// Three minutes since each answered: overdue for the hour and today, well inside the
		// five-minute cadence of the week and the month.
		m.spend.chains[span].lastFetch = now.Add(-3 * time.Minute)
	}
	got := m.spanReadings()
	if !got[spanHour].Stale {
		t.Error("the hour is not dated three minutes after a 20s-cadence poll; it IS wedged")
	}
	for _, span := range []spendSpan{span7d, spanMonth} {
		if got[span].Stale {
			t.Errorf("%s is dated three minutes after its own five-minute-cadence poll, which "+
				"reads as wedged on a chain that is not even due yet", spendSpanDefs[span].label)
		}
	}
	// AND THE WIDTH CONSEQUENCE, on a band where nothing is wedged. The fixture above dates the
	// hour and the day legitimately — three minutes IS overdue on a 20s and a 60s cadence — and
	// two genuinely stale cells SHOULD widen the band. What must not widen it is the pair that
	// answered on schedule, so here every chain is inside its own interval.
	healthy := &model{}
	for span := spendSpan(0); span < numSpendSpans; span++ {
		def := spendSpanDefs[span]
		healthy.spend.chains[span].snap = &usage.Snapshot{
			Window: def.window,
			Totals: usage.Counts{Requests: 1, CostMicros: 4_040_000, PricedRequests: 1, PriceableRequests: 1},
			Priced: true,
		}
		healthy.spend.chains[span].lastFetch = now.Add(-def.interval)
	}
	spans := healthy.spanReadings()
	for span := spendSpan(0); span < numSpendSpans; span++ {
		if spans[span].Stale {
			t.Fatalf("%s is dated one interval after its own poll, so this width check is "+
				"measuring the wrong thing", spendSpanDefs[span].label)
		}
	}
	lines := renderSpendBand(spendSummary{Spans: spans}, 40)
	for span := spendSpan(0); span < numSpendSpans; span++ {
		if !strings.Contains(lines[0], spendSpanDefs[span].label) {
			t.Errorf("at width 40, %s was dropped from a band where every chain answered on "+
				"schedule:\n%s", spendSpanDefs[span].label, strings.Join(lines, "\n"))
		}
	}
}
