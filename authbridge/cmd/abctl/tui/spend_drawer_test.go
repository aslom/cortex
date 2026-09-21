package tui

import (
	"math"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"strings"
	"testing"

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
	// "saved $0.18", in cents and with no marker — see renderTierRows for why this panel no
	// longer marks its figures. The WORD is asserted along with the figure because it is what
	// keeps the saving apart from the cost now that the marker is gone: a bare "$0.18" beside
	// "$11.12" is two spend figures with nothing telling them apart.
	if !strings.Contains(opus, "saved $0.18") {
		t.Errorf("row %q is missing the saving or the word that identifies it", opus)
	}
	if strings.Contains(opus, inexactMarker) {
		t.Errorf("row %q carries %q; this panel states no per-row caveat", opus, inexactMarker)
	}
	if !strings.Contains(opus, "$11.12") {
		t.Errorf("row %q lost its cost", opus)
	}
	if strings.Contains(opus, "$11.1214") {
		t.Errorf("row %q kept four decimals; this column reads in cents", opus)
	}
	// 11.1214 + 0.1804, in cents.
	if strings.Contains(opus, "$11.30") {
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
	// THROUGH THE REAL CYCLERS, not a copy of their arithmetic. Re-implementing
	// `(step + 1) % len(...)` inline meant the test asserted that its own expression wraps.
	// fetchSpend returns nil on a nil client, so the returned tea.Cmd can be discarded here.
	//
	// TWO FULL LAPS, and the exact sequence at each step. One lap is where this was blind:
	// with the counter incremented without bound, a resolution that CLAMPED out-of-range to the
	// first entry gave the right answer on the step immediately after the end — the only step a
	// single lap checks — and the wrong one on every step after that. Both resolutions wrap now
	// (see wrapIndex), and two laps is what proves it.
	// The expected sequence WRITTEN OUT, not derived from wrapIndex — deriving it from the
	// helper under test would make the assertion agree with whatever that helper does. It starts
	// at the DEFAULT rather than at the slice's first entry, which is the point of windowStep
	// being an offset: a fresh model requests the span the strip has always requested.
	wantSpans := []time.Duration{spendWindow, 6 * time.Hour, 15 * time.Minute}
	m := &model{}
	for lap := 0; lap < 2; lap++ {
		for i, want := range wantSpans {
			if got := m.spend.window(); got != want {
				t.Errorf("lap %d step %d: window() = %v, want %v", lap, i, got, want)
			}
			_ = m.cycleSpendWindow()
		}
	}
	if got := m.spend.window(); got != spendWindow {
		t.Errorf("after two full laps window() = %v, want back at %v", got, spendWindow)
	}

	for lap := 0; lap < 2; lap++ {
		for i, want := range spendDrawerAxes {
			if got := m.spend.axis(); got != want {
				t.Errorf("lap %d step %d: axis() = %q, want %q", lap, i, got, want)
			}
			_ = m.cycleSpendAxis()
		}
	}
	if got := m.spend.axis(); got != usage.GroupModel {
		t.Errorf("after two full laps axis() = %q, want back at %q", got, usage.GroupModel)
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
	// The band's label row, which is where the strip's "SPEND" used to be: the column labels
	// are the region's identity now.
	bandAt := strings.Index(out, "LAST 1H")
	rowAt := strings.Index(out, "claude-opus-5")
	if bandAt < 0 {
		t.Fatalf("no band in the view:\n%s", out)
	}
	if rowAt < 0 {
		t.Fatalf("no drawer row in the view:\n%s", out)
	}
	if rowAt < bandAt {
		t.Errorf("the drawer renders above the band it expands")
	}
	// The body survives: the header row of the sessions table must still be there.
	if !strings.Contains(out, "UPDATED") {
		t.Errorf("the table is gone from the view; a drawer that displaces the body has "+
			"become a pane:\n%s", out)
	}
}

// THE VIEW MUST FIT THE TERMINAL, open or closed.
//
// The check this test file was missing, and the defect it let through: layout() reserved a row
// for the strip and nothing for the drawer, so pressing `$` rendered spendDrawerLines lines more
// than the terminal has — measured at 45 lines in a 40-row terminal — and the footer went off the
// bottom on every pane. Substring presence and ordering, which is all the test above checks, is
// blind to it: every line it looks for was present, just not on screen.
//
// Both states and several heights, because the reservation is height-gated: below
// spendDrawerMinHeight the drawer must not draw AND must not reserve, and the floor itself is
// where an off-by-one would show.
func TestPaneView_FitsTheTerminalWithTheDrawerOpen(t *testing.T) {
	// THE SNAPSHOT VARIES TOO, and its absence is the case the first version could not see: it
	// only ever set drawerSnap(), so "the renderer pads a nil snapshot" was covered while "the
	// VIEW fills its reservation for one" was not — which reads as coverage. Before the first poll
	// answers renderSpendStrip returns "" on purpose, and the reservations are height-gated, so
	// nothing filled them and the footer sat six rows up.
	for _, snap := range []*usage.Snapshot{drawerSnap(), nil} {
		for _, h := range []int{spendDrawerMinHeight - 1, spendDrawerMinHeight, 40, 60} {
			for _, open := range []bool{false, true} {
				m := &model{width: 160, height: h, endpoint: "http://x"}
				m.pane = paneSessions
				m.sessionsTbl = newSessionsTable()
				m.spend.snap = snap
				m.spend.expanded = open
				m.layout()

				lines := strings.Count(m.paneView(), "\n") + 1
				// EXACTLY the terminal height, not merely within it. "> height" is blind to the
				// other direction, and that direction shipped twice: the reservation is
				// unconditional while the render emitted only what it had, so one model left the
				// footer three rows above the bottom and no snapshot at all left it six.
				if lines != m.height {
					t.Errorf("height %d, open=%v, snapshot=%v: the view is %d lines (%+d) — the "+
						"footer is not at the bottom of the terminal",
						h, open, snap != nil, lines, lines-m.height)
				}
			}
		}
	}
}

// The hint line describes the DATA, not the next request. m.spend.axis() and .window() are what
// the next poll will ask for, so reading them at render time repainted the label the instant `a`
// or `w` was pressed — one poll ahead of rows still grouped and spanned the old way.
//
// The strip already follows this rule for its window figure ("print the window the SERVER
// reported"), and a label describing something other than the figures beside it is the mislabel
// the whole surface is written against.
func TestDrawerLabels_DescribeTheSnapshotNotTheNextRequest(t *testing.T) {
	m := &model{width: 200, height: 60}
	m.pane = paneSessions
	// The snapshot in hand was grouped by model over an hour.
	m.spend.snap = drawerSnap()
	m.spend.snap.Window = "1h0m0s"
	m.spend.snap.Group = usage.GroupModel

	// The operator presses `a` and `w`: the NEXT poll will ask for endpoint over 6h.
	m.spend.groupIdx, m.spend.windowStep = 1, 1
	if m.spend.axis() == usage.GroupModel {
		t.Fatal("setup: the requested axis did not move")
	}

	axis, window := m.drawerLabels()
	if axis != usage.GroupModel {
		t.Errorf("axis label = %q while the rows on screen are grouped by %q: the label leads the "+
			"data by one poll", axis, usage.GroupModel)
	}
	if window != "1h" {
		t.Errorf("window label = %q, want 1h — the span the answer covers, not the one queued",
			window)
	}

	// With no snapshot there is nothing to describe, so the requested values are the honest
	// fallback: a blank axis would read as a rendering fault.
	m.spend.snap = nil
	if axis, window := m.drawerLabels(); axis == "" || window == "" {
		t.Errorf("labels = %q/%q with no snapshot; want the requested values rather than blanks",
			axis, window)
	}
}

// AND THROUGH THE VIEW, because the unit above cannot see the call site. Asserting drawerLabels in
// isolation leaves paneView free to go on reading m.spend.axis() directly — verified: that mutation
// passed the unit test. The rendered hint line is what a user reads, so that is what has to be
// pinned.
func TestPaneView_TheHintLineLabelsTheSnapshotNotTheNextRequest(t *testing.T) {
	m := &model{width: 200, height: 60, endpoint: "http://x"}
	m.pane = paneSessions
	m.sessionsTbl = newSessionsTable()
	m.spend.snap = drawerSnap()
	m.spend.snap.Window = "1h0m0s"
	m.spend.snap.Group = usage.GroupModel
	m.spend.expanded = true
	// `a` and `w` pressed: the next poll will ask for endpoint over 6h, the rows on screen are
	// still model over an hour.
	m.spend.groupIdx, m.spend.windowStep = 1, 1
	m.layout()

	out := m.paneView()
	if !strings.Contains(out, "["+string(usage.GroupModel)+"]") {
		t.Errorf("the hint line does not bracket %q, the axis the rows on screen are grouped by:\n%s",
			usage.GroupModel, out)
	}
	if !strings.Contains(out, "[w] 1h") {
		t.Errorf("the hint line does not report 1h, the span the answer covers:\n%s", out)
	}
	// The queued values must not be on screen as though they described the data.
	if strings.Contains(out, "["+string(usage.GroupEndpoint)+"]") || strings.Contains(out, "[w] 6h") {
		t.Errorf("the hint line reports the axis or span the NEXT poll will ask for:\n%s", out)
	}
}

// And the reservation has to be the size the renderer actually emits, or the fit above holds by
// luck. Asserted against renderSpendDrawer's own output rather than against the number 5.
func TestSpendDrawerLines_MatchesWhatTheRendererEmits(t *testing.T) {
	// EVERY shape, not just the full one. The reservation is a fixed maximum, so a snapshot with
	// one series or none at all has to occupy it too — a drawer that emits what it happens to have
	// leaves the footer floating.
	for _, tc := range []struct {
		name string
		snap *usage.Snapshot
	}{
		// More series than the drawer keeps: named rows, the band, and the hint.
		{name: "full", snap: drawerSnap()},
		{name: "one series", snap: &usage.Snapshot{
			Window: "1h", Group: usage.GroupModel, Priced: true,
			Buckets: []usage.Bucket{{Series: map[string]usage.Counts{
				"opus": {Requests: 1, CostMicros: 5000, PricedRequests: 1, PriceableRequests: 1},
			}}},
		}},
		// Before the first poll answers: the hint line and nothing to break down.
		{name: "no snapshot", snap: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(renderSpendDrawer(tc.snap, usage.GroupModel, "1h", 200)); got != spendDrawerLines {
				t.Errorf("renderSpendDrawer emits %d lines but layout() reserves %d: the difference "+
					"is either a footer off the bottom or a footer floating above it",
					got, spendDrawerLines)
			}
		})
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

// A NEGATIVE SERIES TOTAL must not print, on the rule every other money surface in this package
// already follows through negativeCost: spendSummary refuses it for the window figure,
// applyTodayFigure for the day, renderCostSummary for the pane, sessionMoneyCell for the column.
// The drawer was the newest money surface and the only one that did not inherit the guard, so a
// series with priced requests and an impossible sum rendered "$-5.0000" — a credit nobody issued
// in a column of costs.
//
// The sum is what is refused, not each bucket: a positive bucket and a negative one can cancel to
// something plausible, and it is the PUBLISHED figure that has to be refusable.
func TestRenderSpendDrawer_ANegativeSeriesTotalIsUnpricedNotARefund(t *testing.T) {
	snap := &usage.Snapshot{
		Window: "1h", Group: usage.GroupModel, Priced: true,
		Buckets: []usage.Bucket{{Series: map[string]usage.Counts{
			// Priced, and impossible — which the gate on PricedRequests admitted.
			"broken-producer": {Requests: 4, CostMicros: -5_000_000,
				PricedRequests: 4, PriceableRequests: 4},
			"claude-opus-5": {Requests: 17, CostMicros: 11_121_400,
				PricedRequests: 17, PriceableRequests: 17},
		}}},
	}
	lines := renderSpendDrawer(snap, usage.GroupModel, "1h", 200)
	joined := strings.Join(lines, "\n")

	if strings.Contains(joined, "$-5") || strings.Contains(joined, "-$5") {
		t.Errorf("the drawer prints a negative cost:\n%s", joined)
	}
	var row string
	for _, l := range lines {
		if strings.Contains(l, "broken-producer") {
			row = l
		}
	}
	if row == "" {
		t.Fatal("no row for the series with the impossible total")
	}
	if !strings.Contains(row, "cost unavailable") {
		t.Errorf("row %q shows no figure and does not say the cost is unknown either", row)
	}
	// Coverage is NOT the complaint here — every request priced — so naming a gap would send a
	// reader to the rate table for a producer bug.
	if strings.Contains(row, "unpriced") {
		t.Errorf("row %q blames coverage for an impossible figure", row)
	}
	// And the healthy series in the same drawer still shows its figure.
	if !strings.Contains(joined, "$11.12") {
		t.Errorf("the good row lost its figure:\n%s", joined)
	}
}

// runeKey builds the KeyMsg handleKey sees for an ordinary character.
func runeKey(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }

// THROUGH handleKey, which nothing in this suite did — and that is the gap the esc defect
// slipped through. Every other drawer test calls toggleSpendDrawer, cycleSpendAxis and the
// renderer directly, so the bindings themselves, their guards and their ORDER against the rest of
// the key handler were untested.
func TestHandleKey_TheDrawersBindings(t *testing.T) {
	newModel := func(pane paneID) *model {
		m := &model{width: 200, height: 60}
		m.pane = pane
		m.sessionsTbl = newSessionsTable()
		m.spend.snap = drawerSnap()
		return m
	}

	t.Run("$ toggles", func(t *testing.T) {
		m := newModel(paneSessions)
		m.handleKey(runeKey('$'))
		if !m.spendDrawerVisible() {
			t.Fatal("$ did not open the drawer")
		}
		m.handleKey(runeKey('$'))
		if m.spend.expanded {
			t.Error("a second $ did not close it")
		}
	})

	t.Run("a and w only act while it is open", func(t *testing.T) {
		m := newModel(paneSessions)
		// Closed: both must be inert, or they take letters from the rest of the UI.
		before := m.spend
		m.handleKey(runeKey('a'))
		m.handleKey(runeKey('w'))
		if m.spend.groupIdx != before.groupIdx || m.spend.windowStep != before.windowStep {
			t.Error("a or w changed the drawer's state while it was closed")
		}

		m.handleKey(runeKey('$'))
		m.handleKey(runeKey('a'))
		if m.spend.axis() != spendDrawerAxes[1] {
			t.Errorf("axis = %q after one `a`, want %q", m.spend.axis(), spendDrawerAxes[1])
		}
		m.handleKey(runeKey('w'))
		if m.spend.window() == spendWindow {
			t.Errorf("window = %v after one `w`, want it moved off the default", m.spend.window())
		}
	})

	// THE REGRESSION. esc used to be gated on the flag rather than on what is on screen, so a
	// drawer left open on one pane swallowed esc on a pane that cannot host it — the Usage pane
	// then needed a second press to exit.
	t.Run("esc is not swallowed where the drawer cannot show", func(t *testing.T) {
		m := newModel(paneSessions)
		m.handleKey(runeKey('$'))
		if !m.spend.expanded {
			t.Fatal("setup: the drawer did not open")
		}
		// Move to a pane that cannot host it. The flag survives, deliberately.
		m.pane = paneUsage
		if m.spendDrawerVisible() {
			t.Fatal("setup: the drawer is visible on the usage pane")
		}
		m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
		if !m.spend.expanded {
			t.Error("esc closed a drawer that is not on screen, so it never reached the pane — " +
				"the Usage pane needs a second press to exit")
		}
		// THE POSITIVE HALF, and the reason this subtest was not a control for its own name.
		// Asserting only that the flag survived says nothing about where esc went: a handler
		// that swallowed the key entirely, or returned before the pane got it, passed. What the
		// regression was actually about is the back-out, so assert the back-out.
		if m.pane == paneUsage {
			t.Errorf("esc did not leave the usage pane (pane = %v) — the flag surviving only "+
				"means the drawer ignored the key, not that the pane received it", m.pane)
		}
	})

	t.Run("esc closes it where it does show", func(t *testing.T) {
		m := newModel(paneSessions)
		m.handleKey(runeKey('$'))
		m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
		if m.spend.expanded {
			t.Error("esc did not close a drawer that was on screen")
		}
	})

	// A resize below the floor is the other way the flag and the screen part company.
	//
	// ON paneEvents, whose esc backs out to Sessions, so the key's arrival at the pane is
	// observable without disturbing the handler under test. An open filter looked like the
	// cheaper signal and was inert: keys.go gates the whole spend block on `!m.filtering`, so
	// `m.filtering = true` skipped the esc case entirely — which made the flag assertion below
	// vacuous too, since nothing could have cleared it. The first version of this subtest
	// asserted less than the one it replaced.
	t.Run("esc is not swallowed below the height floor", func(t *testing.T) {
		m := newModel(paneEvents)
		m.handleKey(runeKey('$'))
		m.height = spendDrawerMinHeight - 1
		if m.spendDrawerVisible() {
			t.Fatal("setup: still visible below the floor")
		}
		m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
		if m.pane == paneEvents {
			t.Error("esc never reached the pane below the height floor — still on Events, so the " +
				"off-screen drawer swallowed the key rather than passing it on")
		}
		if !m.spend.expanded {
			t.Error("esc closed an off-screen drawer after a resize")
		}
	})
}

// EVERY pane, with its host decision stated — so adding a pane forces a choice here rather than
// inheriting one, and so the README's scope column and the `?` overlay have a single list to be
// checked against. Both have already drifted from it once.
func TestSpendDrawerHost_CoversEveryPane(t *testing.T) {
	// Keyed by pane, and the switch in spendDrawerHost lists the refusals — so a new paneID
	// appears here as a missing map entry rather than silently defaulting to "hosts it". It
	// caught panePluginDetail missing from this list on the first run, which is the same service
	// TestPaneKeysCoverAllPanes performs for the help overlay and for the same reason: paneUsage
	// once shipped reachable by `u` and named nowhere.
	want := map[paneID]bool{
		paneNamespaces:   false, // nothing connected yet
		panePods:         false, // likewise
		paneUsage:        false, // already a breakdown, and it owns w/b/m
		paneSessions:     true,
		paneEvents:       true,
		paneDetail:       true,
		panePipeline:     true,
		panePluginDetail: true,
		paneCatalog:      true,
	}
	for p := paneID(0); p <= lastPaneID; p++ {
		expect, listed := want[p]
		if !listed {
			t.Errorf("pane %d is not listed here: a new pane must have its drawer decision made "+
				"deliberately, and the README's scope column has to be updated with it", p)
			continue
		}
		if ok, why := m0().withPane(p).spendDrawerHost(); ok != expect {
			t.Errorf("pane %d: hosts = %v, want %v (reason %q)", p, ok, expect, why)
		}
	}
	// A refusal without a reason is what produced the wrong-reason bug; none may be silent.
	for p, expect := range want {
		if expect {
			continue
		}
		if _, why := m0().withPane(p).spendDrawerHost(); why == "" {
			t.Errorf("pane %d refuses with no reason to show the user", p)
		}
	}
}

func m0() *model { return &model{width: 200, height: 60} }

func (m *model) withPane(p paneID) *model { m.pane = p; return m }

// AN UNPRICED TAIL MUST STILL APPEAR. foldTailSeries appends its "(other)" to the kept list only
// when the tail's METRIC total is positive, and this drawer's metric is COST — so a tail of models
// with no rate produced folded buckets carrying the band and a ranked list that never named it,
// and the row was dropped. 100 requests and 2.7M tokens went missing with every figure above them
// unchanged.
//
// The inverse of TestSpendDrawerRows_AnAggregatorOtherMergesRatherThanDuplicating: that one
// catches rows summing PAST the snapshot, this one catches them summing under it. The quiet
// direction needed its own test, which is why it shipped.
//
// Reachable rather than exotic: a gateway without a full rate card leaves models permanently
// unpriced, and the Usage pane never met this because its metric is tokens or requests — positive
// whenever the tail exists at all.
func TestSpendDrawerRows_AnUnpricedTailStillGetsItsBand(t *testing.T) {
	snap := &usage.Snapshot{
		Window: "1h", Group: usage.GroupModel, Priced: true,
		Buckets: []usage.Bucket{{Series: map[string]usage.Counts{
			"opus":   {Requests: 1, CostMicros: 5000, Tokens: 1000, PricedRequests: 1, PriceableRequests: 1},
			"sonnet": {Requests: 1, CostMicros: 4000, Tokens: 1000, PricedRequests: 1, PriceableRequests: 1},
			"haiku":  {Requests: 1, CostMicros: 3000, Tokens: 1000, PricedRequests: 1, PriceableRequests: 1},
			// The tail: real traffic, no rate, so zero cost.
			"unpriced-a": {Requests: 60, Tokens: 1_000_000, PriceableRequests: 60},
			"unpriced-b": {Requests: 40, Tokens: 700_000, PriceableRequests: 40},
		}}},
	}
	// What the snapshot holds, so the assertion is against the data and not a copied constant.
	var wantReq, wantTok int64
	for _, b := range snap.Buckets {
		for _, c := range b.Series {
			wantReq += c.Requests
			wantTok += c.Tokens
		}
	}

	rows := spendDrawerRows(snap, spendDrawerSeries)

	if !func() bool {
		for _, r := range rows {
			if r.label == tailLabel {
				return true
			}
		}
		return false
	}() {
		t.Fatalf("no %q row in %v: the tail priced nothing, so its requests and tokens are "+
			"invisible while the figures above are unchanged", tailLabel, rowLabels(rows))
	}

	var gotReq, gotTok int64
	for _, r := range rows {
		gotReq += r.counts.Requests
		gotTok += r.counts.Tokens
	}
	if gotReq != wantReq || gotTok != wantTok {
		t.Errorf("rows account for %d requests and %d tokens, snapshot holds %d and %d — the "+
			"breakdown sums UNDER the answer it breaks down", gotReq, gotTok, wantReq, wantTok)
	}
	// The band carries no cost, and must not invent one.
	for _, r := range rows {
		if r.label == tailLabel && r.counts.CostMicros != 0 {
			t.Errorf("%q reports %d micros for a tail that priced nothing", tailLabel, r.counts.CostMicros)
		}
	}
}

// Ranking is money, so it saturates like money.
//
// The drawer displays Counts.Add's saturating totals and used to RANK on a raw `+=`, which
// makes the two disagree exactly where int64 runs out. The consequence is not a wrong figure
// but a missing row: a negative rank sorts below every real series, foldTailSeries folds the
// window's most expensive model into (other), and the reader sees four cheap models and a
// band. Unreachable in practice at $9.2T per label — pinned because the fix is the package's
// own accumulator and the next money surface should copy this one, not the old one.
func TestRankSeriesByCost_SaturatesInsteadOfWrapping(t *testing.T) {
	half := int64(math.MaxInt64/2) + 100
	buckets := []usage.Bucket{
		{Series: map[string]usage.Counts{"whale": {Requests: 1, CostMicros: half}}},
		{Series: map[string]usage.Counts{"whale": {Requests: 1, CostMicros: half}}},
		{Series: map[string]usage.Counts{"minnow": {Requests: 1, CostMicros: 10}}},
	}
	ranked := rankSeriesByCost(buckets)
	if len(ranked) == 0 {
		t.Fatal("no series ranked")
	}
	if ranked[0].label != "whale" {
		t.Errorf("ranked first = %q (total %d), want whale — the overflowing series must rank "+
			"above a 10-micro one, not below it", ranked[0].label, ranked[0].total)
	}
	for _, s := range ranked {
		if s.total < 0 {
			t.Errorf("series %q ranks at %d: a money total wrapped negative", s.label, s.total)
		}
	}
}

// Traffic that can never carry a price says so, in its own words.
//
// Requests with no priceable one among them is what an MCP-only endpoint or agent looks like,
// and it took the branch nothing matched: no cost cell, no caveat. "cost unavailable" is the
// wrong word for it — the cost is known to be nothing — so the row needs a third spelling, and
// this test pins both halves of that: the row says something, and it does not say the thing
// that would send a reader to the rate table.
func TestDrawerFigures_NamesTrafficThatCannotBePriced(t *testing.T) {
	snap := &usage.Snapshot{
		Window: "1h", Group: usage.GroupEndpoint, Priced: true,
		Buckets: []usage.Bucket{{Series: map[string]usage.Counts{
			"inference.svc": {Requests: 5, Tokens: 100, CostMicros: 900,
				PricedRequests: 5, PriceableRequests: 5},
			"mcp-tools.svc": {Requests: 9, Tokens: 8000},
		}}},
	}
	var row string
	for _, line := range renderSpendDrawer(snap, usage.GroupEndpoint, "1h", 200) {
		if strings.Contains(line, "mcp-tools.svc") {
			row = line
		}
	}
	if row == "" {
		t.Fatal("the unpriceable series has no row at all")
	}
	if !strings.Contains(row, "not priceable") {
		t.Errorf("row %q leaves the cost slot blank for traffic that cannot carry a price", row)
	}
	if strings.Contains(row, "cost unavailable") {
		t.Errorf("row %q says the cost is unavailable, but nothing here is priceable — that "+
			"sends a reader to the rate table for traffic that has no rate", row)
	}
	// The priced row beside it is untouched: this branch is reached only when nothing priced
	// AND nothing could have.
	for _, line := range renderSpendDrawer(snap, usage.GroupEndpoint, "1h", 200) {
		if strings.Contains(line, "inference.svc") && strings.Contains(line, "not priceable") {
			t.Errorf("priced row %q picked up the unpriceable caveat", line)
		}
	}
}

// tierSnap is drawerSnap with a modelled mix on the totals, so the tier column has something
// to say.
func tierSnap() *usage.Snapshot {
	s := drawerSnap()
	s.Totals.InputCostMicros = 3000
	s.Totals.CacheWriteCostMicros = 7500
	s.Totals.CacheReadCostMicros = 30000
	s.Totals.OutputCostMicros = 45000
	return s
}

// Two columns, headed, tiers left and series right.
func TestRenderSpendDrawer_ShowsBothColumnsWithHeaders(t *testing.T) {
	lines := renderSpendDrawer(tierSnap(), usage.GroupModel, "1h", 100)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"WHERE IT WENT", "BY MODEL", "cache-read", "claude-opus-5"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the panel is missing %q:\n%s", want, joined)
		}
	}
	// The header names the CURRENT axis, which is what the dropped tree glyphs were
	// gesturing at and what the hint line otherwise says only in brackets.
	endpoint := strings.Join(renderSpendDrawer(tierSnap(), usage.GroupEndpoint, "1h", 100), "\n")
	if !strings.Contains(endpoint, "BY ENDPOINT") {
		t.Errorf("the header does not follow the axis:\n%s", endpoint)
	}
}

// The right column's header sits in the right column.
//
// Measured, not eyeballed: fitStripFigures prepends its own `label + "  "` indent, so the
// series text sat three columns right of the header naming it — "BY MODEL" at column 36
// against "claude-opus-5" at 39. A header over the wrong column is worse than none.
func TestRenderSpendDrawer_TheSeriesHeaderSitsOverItsColumn(t *testing.T) {
	for _, width := range []int{80, 100, 160, 200} {
		lines := renderSpendDrawer(tierSnap(), usage.GroupModel, "1h", width)
		hdr, row := lines[0], lines[1]
		hi, ri := strings.Index(hdr, "BY MODEL"), strings.Index(row, "claude-opus-5")
		if hi < 0 || ri < 0 {
			t.Fatalf("width %d: header or first row missing:\n%s", width, strings.Join(lines, "\n"))
		}
		hcol := len([]rune(hdr[:hi]))
		rcol := len([]rune(row[:ri]))
		if hcol != rcol {
			t.Errorf("width %d: header starts at column %d, its column starts at %d:\n%s",
				width, hcol, rcol, strings.Join(lines, "\n"))
		}
	}
}

// The figures the band already shows are not repeated here.
//
// With one model in the window the old drawer restated the window total and the saving
// verbatim, which is what made it useless on a single-model deployment — the common case.
func TestRenderSpendDrawer_DoesNotRestateTheBandsFigures(t *testing.T) {
	snap := &usage.Snapshot{
		Window: "1h", Group: usage.GroupModel, Priced: true,
		Totals: usage.Counts{
			Requests: 35, CostMicros: 4_546_200, AvoidedMicros: 209_100,
			InputCostMicros: 3000, OutputCostMicros: 45000,
		},
		Buckets: []usage.Bucket{{Series: map[string]usage.Counts{
			"claude-opus-5": {Requests: 35, CostMicros: 4_546_200, AvoidedMicros: 209_100,
				PricedRequests: 35, PriceableRequests: 35},
		}}},
	}
	joined := strings.Join(renderSpendDrawer(snap, usage.GroupModel, "1h", 100), "\n")
	// The window total appears once — on the model row that earned it — and the tier column
	// carries shares of it rather than the figure again.
	if n := strings.Count(joined, "$4.5462"); n > 1 {
		t.Errorf("the window total appears %d times in the panel:\n%s", n, joined)
	}
}

// The tree glyphs are gone: they implied a parent row that does not exist.
func TestRenderSpendDrawer_HasNoOrphanTreeGlyph(t *testing.T) {
	joined := strings.Join(renderSpendDrawer(tierSnap(), usage.GroupModel, "1h", 100), "\n")
	for _, glyph := range []string{"└", "├"} {
		if strings.Contains(joined, glyph) {
			t.Errorf("the panel still draws %q, which implies a parent row:\n%s", glyph, joined)
		}
	}
}

// Too narrow for two columns and the TIER column yields, so the panel degrades to exactly
// the per-model drawer that shipped before this feature. The addition gives way to the
// existing contract, never the reverse.
func TestRenderSpendDrawer_NarrowDropsTheTierColumnNotTheModels(t *testing.T) {
	joined := strings.Join(
		renderSpendDrawer(tierSnap(), usage.GroupModel, "1h", spendDrawerTwoColumnMin-1), "\n")
	if !strings.Contains(joined, "claude-opus-5") {
		t.Errorf("the model column dropped below the two-column width:\n%s", joined)
	}
	if strings.Contains(joined, "cache-read") {
		t.Errorf("both columns drawn below the two-column width:\n%s", joined)
	}
}
