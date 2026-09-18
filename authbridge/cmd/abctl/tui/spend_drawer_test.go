package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/rossoctl/cortex/authbridge/authlib/usage"
)

// drawerSnap is four series whose cost ORDER differs from their request order, which is the
// whole point of the fixture: claude-haiku-4-5 has the most requests and the least spend, so
// a drawer ranking by requests would put it first and a drawer ranking by cost puts it third.
func drawerSnap() *usage.Snapshot {
	return &usage.Snapshot{
		Window: "1h", Group: usage.GroupModel, Priced: true,
		Totals: usage.Counts{Requests: 40, CostMicros: 11_121_980, Tokens: 8_577_000},
		Buckets: []usage.Bucket{{Series: map[string]usage.Counts{
			"claude-opus-5": {Requests: 17, CostMicros: 11_121_400, Tokens: 7_980_000,
				PricedRequests: 17, PriceableRequests: 17, AvoidedMicros: 180_400},
			"claude-sonnet-5": {Requests: 2, CostMicros: 400, Tokens: 549_000,
				PricedRequests: 2, PriceableRequests: 2},
			"claude-haiku-4-5": {Requests: 120, CostMicros: 120, Tokens: 40_000,
				PricedRequests: 120, PriceableRequests: 120},
			"gpt-4o": {Requests: 9, CostMicros: 60, Tokens: 8_000,
				PricedRequests: 4, PriceableRequests: 9},
		}}},
	}
}

// The rows are ranked by COST, and the tail folds into one "(other)" band.
//
// Ranking is the assertion, not membership. This is the spend drawer: the row worth reading
// first is the one that cost the most, and for mixed traffic that is emphatically not the one
// with the most requests — 120 haiku calls cost a hundredth of 17 opus ones. A drawer that
// ranked by requests or tokens would pass a membership test and put the cheapest row on top.
func TestSpendDrawerRows_RanksByCostAndFoldsTheTail(t *testing.T) {
	rows := spendDrawerRows(drawerSnap(), spendDrawerSeries)

	if len(rows) != spendDrawerSeries+1 {
		t.Fatalf("rows = %d, want %d named plus one (other) band: %v",
			len(rows), spendDrawerSeries+1, rowLabels(rows))
	}
	want := []string{"claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5", tailLabel}
	for i, w := range want {
		if rows[i].label != w {
			t.Errorf("row %d = %q, want %q (full order %v)", i, rows[i].label, w, rowLabels(rows))
		}
	}
	// The band carries the folded series' figures, not a placeholder: gpt-4o's 9 requests
	// and its coverage gap have to survive the fold or the drawer loses them silently.
	band := rows[len(rows)-1]
	if band.counts.Requests != 9 {
		t.Errorf("(other) requests = %d, want 9 — the folded series' counters were dropped",
			band.counts.Requests)
	}
	if band.counts.PriceableRequests-band.counts.PricedRequests != 5 {
		t.Errorf("(other) coverage gap = %d, want 5",
			band.counts.PriceableRequests-band.counts.PricedRequests)
	}
}

// "(other)" is always LAST, whatever it sums to.
//
// Reachable and not hypothetical: the folded tail can outweigh a named row, since the fold
// keeps the top three INDIVIDUALLY and the rest collectively. A band sorted by its total
// would then jump above a named series, and a reader would take the band's position as a
// ranking rather than as a remainder.
func TestSpendDrawerRows_TheOtherBandStaysLastEvenWhenItOutweighsANamedRow(t *testing.T) {
	snap := drawerSnap()
	// Four cheap series so the tail is large: each is individually below the named rows,
	// and together they exceed the third of them.
	for _, name := range []string{"m-a", "m-b", "m-c", "m-d"} {
		snap.Buckets[0].Series[name] = usage.Counts{
			Requests: 1, CostMicros: 5_000, PricedRequests: 1, PriceableRequests: 1,
		}
	}
	rows := spendDrawerRows(snap, spendDrawerSeries)

	last := rows[len(rows)-1]
	if last.label != tailLabel {
		t.Fatalf("last row = %q, want %q: %v", last.label, tailLabel, rowLabels(rows))
	}
	// And it really does outweigh a named row, or this test proves nothing.
	third := rows[spendDrawerSeries-1]
	if last.counts.CostMicros <= third.counts.CostMicros {
		t.Fatalf("(other) = %d is not above the third named row (%d), so the ordering rule was "+
			"never exercised", last.counts.CostMicros, third.counts.CostMicros)
	}
}

// Each row's caveats come from ITS OWN counters, never the window's.
//
// A per-series total that is 4-of-9 priced has to say so on its own line. Borrowing the
// window's coverage would attach a caveat measured over other traffic — the misattribution
// moneyFigure exists to prevent — and here the window is fully priced except for that one
// series, so the two answers differ.
func TestRenderSpendDrawer_EachRowCarriesItsOwnCoverage(t *testing.T) {
	lines := renderSpendDrawer(drawerSnap(), usage.GroupModel, "1h", 200)
	joined := strings.Join(lines, "\n")

	// gpt-4o's gap folds into the band, which is where the caveat must appear.
	if !strings.Contains(joined, "unpriced") {
		t.Errorf("no coverage caveat anywhere in the drawer:\n%s", joined)
	}
	// The fully priced rows must NOT carry one.
	for _, line := range lines {
		if strings.Contains(line, "claude-opus-5") && strings.Contains(line, "unpriced") {
			t.Errorf("a fully priced row carries a coverage caveat: %q", line)
		}
	}
}

// The saving rides on the row that earned it, wearing its marker, and never joins the cost.
func TestRenderSpendDrawer_ShowsAPerSeriesSavingWithoutAddingItToCost(t *testing.T) {
	lines := renderSpendDrawer(drawerSnap(), usage.GroupModel, "1h", 200)
	var opus string
	for _, l := range lines {
		if strings.Contains(l, "claude-opus-5") {
			opus = l
		}
	}
	if opus == "" {
		t.Fatal("no row for claude-opus-5")
	}
	if !strings.Contains(opus, inexactMarker+"$0.1804") {
		t.Errorf("row %q is missing the saving or its %q marker", opus, inexactMarker)
	}
	if !strings.Contains(opus, "$11.1214") {
		t.Errorf("row %q lost its cost", opus)
	}
	// 11.1214 + 0.1804.
	if strings.Contains(opus, "$11.3018") {
		t.Errorf("row %q added the saving to the cost", opus)
	}
}

// The hint line names both keys and says which axis is current, because it is the only place
// either is written down. A drawer whose controls are undiscoverable is a drawer nobody
// changes the axis of.
func TestRenderSpendDrawer_HintLineNamesTheKeysAndTheCurrentAxis(t *testing.T) {
	lines := renderSpendDrawer(drawerSnap(), usage.GroupEndpoint, "6h", 200)
	hint := lines[len(lines)-1]
	// "[a]", not "[g]": g is globally "go to top" and the drawer stays open alongside the table,
	// so it must not shadow that motion. See cycleSpendAxis.
	for _, want := range []string{"[a]", "[w]", "6h", "esc"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint %q is missing %q", hint, want)
		}
	}
	// The CURRENT axis is bracketed and the others are not, so the line says where the
	// cycle is as well as what it offers.
	if !strings.Contains(hint, "["+string(usage.GroupEndpoint)+"]") {
		t.Errorf("hint %q does not mark endpoint as current", hint)
	}
	if strings.Contains(hint, "["+string(usage.GroupModel)+"]") {
		t.Errorf("hint %q marks model as current when the axis is endpoint", hint)
	}
}

// The zero value of the state must be what the strip requested before the drawer existed.
//
// windowStep is an offset rather than an index precisely because of this: spendWindow sits in
// the MIDDLE of an ascending span slice, so a bare index of zero would have moved the
// strip's own poll to 15m — which is what the first version did, under a comment claiming it
// could not.
func TestSpendState_ZeroValueRequestsThePreDrawerDefaults(t *testing.T) {
	var s spendState
	if got := s.window(); got != spendWindow {
		t.Errorf("window() = %v on a zero-value state, want %v", got, spendWindow)
	}
	if got := s.axis(); got != usage.GroupModel {
		t.Errorf("axis() = %q on a zero-value state, want %q", got, usage.GroupModel)
	}
}

// Both cycles wrap and return to where they started, and every stop is distinct — a cycle
// that repeated a value would waste a keypress, and one that never returned to the default
// would trap the user away from it.
func TestSpendDrawer_CyclesWrapThroughEveryDistinctStop(t *testing.T) {
	m := &model{}
	seenSpans := map[time.Duration]bool{}
	for range spendDrawerWindows {
		seenSpans[m.spend.window()] = true
		m.spend.windowStep = (m.spend.windowStep + 1) % len(spendDrawerWindows)
	}
	if len(seenSpans) != len(spendDrawerWindows) {
		t.Errorf("the span cycle visited %d distinct spans, want %d", len(seenSpans),
			len(spendDrawerWindows))
	}
	if got := m.spend.window(); got != spendWindow {
		t.Errorf("after a full cycle window() = %v, want back at %v", got, spendWindow)
	}

	seenAxes := map[usage.Group]bool{}
	for range spendDrawerAxes {
		seenAxes[m.spend.axis()] = true
		m.spend.groupIdx = (m.spend.groupIdx + 1) % len(spendDrawerAxes)
	}
	if len(seenAxes) != len(spendDrawerAxes) {
		t.Errorf("the axis cycle visited %d distinct axes, want %d", len(seenAxes),
			len(spendDrawerAxes))
	}
	if got := m.spend.axis(); got != usage.GroupModel {
		t.Errorf("after a full cycle axis() = %q, want back at %q", got, usage.GroupModel)
	}
}

// A terminal too short refuses to open, and SAYS SO. A key that silently does nothing reads
// as a broken key.
//
// Both directions asserted: refusing everywhere would satisfy the short half on its own and
// silently cost the feature.
func TestToggleSpendDrawer_RefusesOnAShortTerminalAndExplains(t *testing.T) {
	tall := &model{width: 100, height: spendDrawerMinHeight}
	tall.pane = paneEvents
	tall.toggleSpendDrawer()
	if !tall.spend.expanded {
		t.Errorf("a %d-row terminal refused to open the drawer, which is its stated floor",
			spendDrawerMinHeight)
	}
	if !tall.spendDrawerVisible() {
		t.Error("expanded but not visible at the floor height")
	}

	short := &model{width: 100, height: spendDrawerMinHeight - 1}
	short.pane = paneEvents
	short.toggleSpendDrawer()
	if short.spend.expanded {
		t.Error("a terminal one row below the floor opened the drawer")
	}
	if short.flash == "" {
		t.Error("the refusal was silent; a key that does nothing reads as a broken key")
	}

	// And a second press closes it again.
	tall.toggleSpendDrawer()
	if tall.spend.expanded {
		t.Error("a second press did not close the drawer")
	}
}

// The drawer never draws without the strip above it. A breakdown under a bare title, with no
// summary it is breaking down, is a pane — which is the one thing this is not.
func TestSpendDrawerVisible_RequiresTheStrip(t *testing.T) {
	// Tall enough for the drawer, but on a pane the strip does not draw on.
	m := &model{width: 100, height: 40}
	m.pane = panePods
	m.spend.expanded = true
	if m.spendDrawerVisible() {
		t.Error("the drawer draws on the pods picker, where the strip does not")
	}
	m.pane = paneEvents
	if !m.spendDrawerVisible() {
		t.Error("the drawer does not draw on a pane where the strip does")
	}
}

// The drawer's rows appear in the rendered view, under the strip and above the body, with the
// table still on screen — which is the entire argument for a drawer over a pane.
func TestPaneView_DrawsTheDrawerUnderTheStripAndKeepsTheBody(t *testing.T) {
	m := &model{width: 160, height: 40, endpoint: "http://x"}
	m.pane = paneSessions
	m.sessionsTbl = newSessionsTable()
	m.spend.snap = drawerSnap()
	m.spend.expanded = true
	m.layout()

	out := m.paneView()
	stripAt := strings.Index(out, stripLabel)
	rowAt := strings.Index(out, "claude-opus-5")
	if stripAt < 0 {
		t.Fatalf("no strip in the view:\n%s", out)
	}
	if rowAt < 0 {
		t.Fatalf("no drawer row in the view:\n%s", out)
	}
	if rowAt < stripAt {
		t.Errorf("the drawer renders above the strip it expands")
	}
	// The body survives: the header row of the sessions table must still be there.
	if !strings.Contains(out, "UPDATED") {
		t.Errorf("the table is gone from the view; a drawer that displaces the body has "+
			"become a pane:\n%s", out)
	}
}

func rowLabels(rows []drawerRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.label)
	}
	return out
}

// An "(other)" the AGGREGATOR produced must merge into the tail, not appear beside it.
//
// usage caps how many labels it tracks per bucket and folds the rest into its own "(other)"
// entry, so that label can arrive in the snapshot's Series map — and rank inside the top three,
// since it is a sum of everything the server dropped. foldTailSeries merges it; the drawer
// truncated the ranked list by hand and appended a band unconditionally, so the row appeared
// TWICE, each copy carrying the same merged total. The rows then summed past the strip's
// headline above them by the whole tail.
//
// THE SUM IS THE ASSERTION, not just the row count. Two bands with the same label is a visible
// oddity; two bands with the same TOTAL is a wrong number, and it is the one a reader would act
// on. Every existing case here exercises a fold-produced "(other)" only, which is why this
// survived.
func TestSpendDrawerRows_AnAggregatorOtherMergesRatherThanDuplicating(t *testing.T) {
	snap := &usage.Snapshot{
		Window: "1h", Group: usage.GroupModel, Priced: true,
		Buckets: []usage.Bucket{{Series: map[string]usage.Counts{
			"opus":   {Requests: 17, CostMicros: 10_000_000, PricedRequests: 17, PriceableRequests: 17},
			"sonnet": {Requests: 2, CostMicros: 8_000_000, PricedRequests: 2, PriceableRequests: 2},
			// The server's own band, ranking SECOND by cost — inside the top three.
			tailLabel: {Requests: 30, CostMicros: 9_000_000, PricedRequests: 30, PriceableRequests: 30},
			// And a genuine tail for the fold to merge into it.
			"haiku":  {Requests: 40, CostMicros: 1_900_000, PricedRequests: 40, PriceableRequests: 40},
			"gpt-4o": {Requests: 9, CostMicros: 1_000_000, PricedRequests: 9, PriceableRequests: 9},
		}}},
	}
	// The snapshot's own total, which the rows must not exceed.
	var snapTotal int64
	for _, b := range snap.Buckets {
		for _, c := range b.Series {
			snapTotal += c.CostMicros
		}
	}

	rows := spendDrawerRows(snap, spendDrawerSeries)

	seen := 0
	var rowTotal int64
	for _, r := range rows {
		rowTotal += r.counts.CostMicros
		if r.label == tailLabel {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("%q appears %d times in %v, want once: two bands both meaning \"the rest\" is "+
			"indefensible, which is why foldTailSeries merges them", tailLabel, seen, rowLabels(rows))
	}
	if rowTotal != snapTotal {
		t.Errorf("the rows sum to %d micros against a snapshot holding %d (delta %+d): the drawer "+
			"is reporting more spend than the strip's headline above it",
			rowTotal, snapTotal, rowTotal-snapTotal)
	}
}

// The refusal must name the REAL reason. A pane that cannot host the drawer has nothing to do
// with height, and a height message there sends the reader to resize a terminal that was never
// the problem — on the file's own standard, "a key that explains the height requirement is a key
// the user stops pressing for a reason".
func TestToggleSpendDrawer_RefusesPerPaneWithTheRightReason(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pane    paneID
		wantSub string
	}{
		// Before a connection exists there is no spend at all, let alone a breakdown.
		{name: "pods picker", pane: panePods, wantSub: "until a pod is connected"},
		{name: "namespaces picker", pane: paneNamespaces, wantSub: "until a pod is connected"},
		// That pane IS the breakdown, and it owns the keys the drawer's hint line would
		// advertise.
		{name: "usage pane", pane: paneUsage, wantSub: "usage pane is the breakdown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A terminal with ample room, so height cannot be the cause of any refusal.
			m := &model{width: 200, height: 60}
			m.pane = tc.pane
			m.toggleSpendDrawer()

			if m.spend.expanded {
				t.Fatalf("the drawer opened on %v", tc.pane)
			}
			if !strings.Contains(m.flash, tc.wantSub) {
				t.Errorf("flash = %q, want it to mention %q — a 60-row terminal is not short, and "+
					"a height complaint here is a wrong reason rather than a missing one",
					m.flash, tc.wantSub)
			}
			if strings.Contains(m.flash, "short") {
				t.Errorf("flash = %q blames the terminal height on a pane that cannot host the "+
					"drawer at any height", m.flash)
			}
		})
	}

	// And a pane that CAN host it still opens, or the refusals above would be indistinguishable
	// from a drawer that never opens anywhere.
	m := &model{width: 200, height: 60}
	m.pane = paneSessions
	m.toggleSpendDrawer()
	if !m.spend.expanded {
		t.Errorf("the drawer refused on the sessions pane too (flash %q)", m.flash)
	}
}

// THE ROW MOST IN NEED OF A CAVEAT IS THE ONE THAT PRICED NOTHING, and it was the only row that
// could not carry one: the money figure was gated on PricedRequests or CostMicros being
// non-zero, so a 0-of-40-priced series rendered its request and token counts with nothing
// anywhere on the line saying its cost was unknown.
//
// "Each row's caveats come from its own counters" held only for rows that managed to price
// something — which is the inverse of what a caveat is for.
func TestRenderSpendDrawer_AFullyUnpricedSeriesSaysItsCostIsUnknown(t *testing.T) {
	snap := &usage.Snapshot{
		Window: "1h", Group: usage.GroupModel, Priced: true,
		Buckets: []usage.Bucket{{Series: map[string]usage.Counts{
			"claude-opus-5": {Requests: 17, CostMicros: 11_121_400, Tokens: 7_980_000,
				PricedRequests: 17, PriceableRequests: 17},
			// Priceable and priced by nothing: a model with no rate in the table.
			"mystery-model": {Requests: 40, Tokens: 900_000, PriceableRequests: 40},
		}}},
	}
	var row string
	for _, l := range renderSpendDrawer(snap, usage.GroupModel, "1h", 200) {
		if strings.Contains(l, "mystery-model") {
			row = l
		}
	}
	if row == "" {
		t.Fatal("no row for the unpriced series")
	}
	if !strings.Contains(row, "cost unavailable") {
		t.Errorf("row %q shows volume with no word about its cost being unknown", row)
	}
	if !strings.Contains(row, "40 of 40 unpriced") {
		t.Errorf("row %q does not name the size of the gap", row)
	}
	// Never a zero: a zero cost and an unknown cost are different answers, and this row is the
	// second kind.
	if strings.Contains(row, "$0.0000") {
		t.Errorf("row %q renders $0.0000 for a cost nobody produced", row)
	}
	// And a priced row in the same drawer is NOT annotated, or the caveat means nothing.
	for _, l := range renderSpendDrawer(snap, usage.GroupModel, "1h", 200) {
		if strings.Contains(l, "claude-opus-5") && strings.Contains(l, "unavailable") {
			t.Errorf("a fully priced row carries the unknown-cost caveat: %q", l)
		}
	}
}
