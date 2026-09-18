package tui

import (
	"fmt"
	"sort"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rossoctl/cortex/authbridge/authlib/usage"
)

// The spend drawer is the strip EXPANDED IN PLACE, not a pane.
//
// WHY NOT A PANE. The breakdown behind the strip's figures — which models, which
// endpoints, which agents — was designed as a seventh pane, and a pane is the wrong
// container for it twice over. Seven panes is already a lot to hold in your head, and cost
// read IN CONTEXT beats cost read by navigating away from whatever you were looking at:
// the whole point of the strip is that spend arrives before the data rather than after a
// keypress. Expanding under the strip keeps the table on screen, which a pane cannot.
//
// Overlays and drawers are the established precedent for "more detail without leaving" —
// help_overlay.go and edit_overlay.go are both non-panes. This is smaller than either: a
// few rows, no cursor, no scroll, no focus.
//
// IT HOLDS NO STATE OF ITS OWN beyond a bool, an axis and a span. Everything it renders
// comes from the snapshot the strip already polled, so opening it costs no request and
// closing it loses nothing. Changing the axis or the span does trigger a refetch, because
// the server does the folding — a client-side regroup would be a second implementation of
// the aggregator's fold, which is the one thing this package refuses to duplicate.
const (
	// spendDrawerSeries is how many series get their own row before the rest are folded
	// into an "(other)" band.
	//
	// Three, because the drawer's whole justification is that it fits under the strip
	// without displacing the table: three rows plus the band plus the key hints is five,
	// which a 26-row terminal can spare and a 20-row one cannot. A scrolling drawer would
	// be a pane wearing a smaller name.
	spendDrawerSeries = 3

	// spendDrawerMinHeight is the terminal height at which the drawer may open.
	//
	// The strip's own floor is spendStripMinHeight (20) and the drawer adds five rows on
	// top of it plus a separator, so 26 is the first height where opening it leaves the
	// table more than a couple of rows. Below that the answer is "no", not "a table with
	// two visible rows": the drawer exists to be read ALONGSIDE the data, and a drawer that
	// squeezes the data out has defeated its own reason for not being a pane.
	spendDrawerMinHeight = 26
)

// spendDrawerAxes are the breakdown axes `g` cycles through.
//
// NO GroupNone in the cycle, unlike the Usage pane's grouping. This drawer's only content
// IS the breakdown, so "no breakdown" is an empty drawer — a state the user can already
// reach, more directly, by pressing `$` again. The cycle offers the three axes that answer
// a different question about the same spend: which MODEL cost this, which ENDPOINT it was
// billed through, and which AGENT asked for it.
//
// GroupSession is deliberately absent too: the sessions picker is the surface with a row
// per session, and it now carries COST and SAVED columns of its own, summed server-side
// over each session's whole life rather than over this drawer's rolling window. Two
// per-session answers with different scopes on two surfaces is how a reader comes to think
// one of them is wrong.
var spendDrawerAxes = []usage.Group{usage.GroupModel, usage.GroupEndpoint, usage.GroupAgent}

// spendDrawerWindows are the spans `w` cycles through.
//
// RING SPANS ONLY, and the absence of "today" and "7d" is a decision rather than an
// omission. The strip's headline figure is already the day's spend, on its own
// ledger-backed poll, so a "today" option here would render the same number twice and the
// drawer would need a SECOND ledger query to break it down — day files off disk, on a
// keypress, where every other option is a fold of a ring already in memory. The drawer
// answers "what is my spend made of right now"; `abctl cost --window 7d` answers the other
// question, from a shell, where waiting for a disk read is expected.
//
// spendWindow (1h) is the middle entry, so the default index leaves the strip requesting
// exactly what it requested before the drawer existed.
var spendDrawerWindows = []time.Duration{15 * time.Minute, spendWindow, 6 * time.Hour}

// spendDrawerWindowDefault indexes spendWindow in spendDrawerWindows.
const spendDrawerWindowDefault = 1

// window is the span the strip and drawer currently request.
//
// windowStep is an OFFSET FROM THE DEFAULT, not an index, and that is the whole reason this
// function exists. The strip polls long before anyone opens the drawer, so a freshly
// constructed model must request spendWindow — but zero is also the natural zero value of a
// counter, and an index of zero points at the FIRST entry of the slice, which is 15m. The
// first version of this guarded only out-of-range indices and therefore made exactly that
// mistake while carrying a comment claiming it did not; the wire assertion in
// TestFetchSpend_AsksForTheDrawersAxis is what caught it.
//
// Adding the default and taking the modulus makes the zero value mean "the span the strip
// always asked for" while keeping the slice in ascending order, so the hint line reads
// 15m · 1h · 6h and `w` still walks it in that direction.
func (s *spendState) window() time.Duration {
	return spendDrawerWindows[s.windowStepIndex()]
}

// windowStepIndex resolves windowStep to a slice index, wrapping and flooring a negative.
func (s *spendState) windowStepIndex() int {
	i := (spendDrawerWindowDefault + s.windowStep) % len(spendDrawerWindows)
	if i < 0 {
		i += len(spendDrawerWindows)
	}
	return i
}

// axis is the breakdown the strip asks the server to fold for.
//
// Requested unconditionally, whether or not the drawer is open, so opening it renders
// immediately from the snapshot already in hand instead of showing an empty frame for a
// poll interval. It costs nothing to carry: usage.Snapshot sums Totals from the raw buckets
// BEFORE folding, so every figure on the strip is byte-identical under any axis.
func (s *spendState) axis() usage.Group {
	if s.groupIdx < 0 || s.groupIdx >= len(spendDrawerAxes) {
		return usage.GroupModel
	}
	return spendDrawerAxes[s.groupIdx]
}

// spendDrawerHost reports whether the current pane can host the drawer, and says why not when
// it cannot.
//
// THE REASON IS RETURNED, not inferred by the caller, because the refusal is shown to the user
// and a wrong reason is worse than none: `$` on a 60-row pod picker used to flash "no room for
// the strip on a terminal this short", which is a height complaint about a pane condition. A
// reader resizes, nothing changes, and the key looks broken.
func (m *model) spendDrawerHost() (bool, string) {
	switch m.pane {
	case paneNamespaces, panePods:
		// The strip itself does not draw here: these run before a connection exists, so there
		// is no spend to summarise, let alone break down.
		return false, "spend: nothing to break down until a pod is connected"
	case paneUsage:
		// THAT PANE IS ALREADY THE BREAKDOWN, with its own axis, window and metric cycles — and
		// its key handler runs before this one and returns, so the drawer's own keys could never
		// reach it. A drawer whose hint line advertises [w] on the one pane where `w` belongs to
		// something else is a lie printed on screen.
		return false, "spend: the usage pane is the breakdown — use its own [b] and [w]"
	}
	return true, ""
}

// spendDrawerVisible reports whether the drawer draws its rows.
//
// Requires the strip: the drawer is an expansion OF it, and a breakdown floating under a
// title with no summary above it would be a pane after all. So a terminal too short for the
// strip is too short for this, and `$` on a 19-row terminal does nothing rather than
// producing a headless breakdown.
func (m *model) spendDrawerVisible() bool {
	if ok, _ := m.spendDrawerHost(); !ok {
		return false
	}
	return m.spend.expanded && m.spendStripVisible() && m.height >= spendDrawerMinHeight
}

// toggleSpendDrawer handles `$`.
//
// REFUSES ON A SHORT TERMINAL, and says so, rather than setting a flag whose effect the
// user cannot see. A key that silently does nothing reads as a broken key; a key that
// explains the height requirement is a key the user stops pressing for a reason.
//
// The refusal does not set expanded, so growing the terminal later does not surprise the
// user with a drawer they asked for minutes ago and were told they could not have.
func (m *model) toggleSpendDrawer() {
	if m.spend.expanded {
		m.spend.expanded = false
		return
	}
	// The PANE first, because its refusal has nothing to do with height and a height message
	// there sends the reader to resize a terminal that was never the problem.
	if ok, why := m.spendDrawerHost(); !ok {
		m.setFlash(why)
		return
	}
	if !m.spendStripVisible() {
		m.setFlash("spend: no room for the strip on a terminal this short")
		return
	}
	if m.height < spendDrawerMinHeight {
		m.setFlash(fmt.Sprintf("spend: the breakdown needs %d rows, this terminal has %d",
			spendDrawerMinHeight, m.height))
		return
	}
	m.spend.expanded = true
}

// cycleSpendAxis handles `a` while the drawer is open, and refetches.
//
// `a` FOR AXIS, and emphatically not `g` for group. `g` is globally "go to top" (see goTop), and
// the Usage pane already rejected `g` for its own breakdown on exactly that reasoning —
// "shadowing a vim-style motion inside one pane is worse than picking a second-choice mnemonic".
// This drawer is designed to stay OPEN alongside the table, so it would shadow the motion for
// most of a session rather than inside one pane.
//
// Nor `b`, which that pane chose: `b` is global page-up here (see the pgup case), so it would
// shadow a worse motion than `g` did. `a` is unbound.
//
// A refetch rather than a client-side regroup: the server folds, and doing it here would be
// a second implementation of the aggregator's arithmetic that could disagree with the
// figures above it. The old snapshot stays on screen until the new one lands, so the drawer
// does not blink through an empty frame — it is a breakdown of the same traffic either way.
func (m *model) cycleSpendAxis() tea.Cmd {
	m.spend.groupIdx = (m.spend.groupIdx + 1) % len(spendDrawerAxes)
	return m.fetchSpend()
}

// cycleSpendWindow handles `w` while the drawer is open, and refetches.
//
// Changes the STRIP's figure too, which is intended: the strip's window reading and the
// drawer's rows are one snapshot, and a breakdown of six hours sitting under a total for
// one would be two spans presented as one answer. The label moves with it, because the
// label is derived from what the server served — see spendSummary.
func (m *model) cycleSpendWindow() tea.Cmd {
	m.spend.windowStep = (m.spend.windowStep + 1) % len(spendDrawerWindows)
	return m.fetchSpend()
}

// plainFigures lifts a run of fixed strings into the fitter's type. None of them can be partial,
// so none of them degrades.
//
// HERE RATHER THAN IN spend_strip.go, where it used to live: the strip stopped using it when its
// fixed readings became individual figures in one list, and a helper with no caller is dead code
// a linter is right to flag. The drawer's hint line is its only consumer now, so it lives with it.
func plainFigures(ss ...string) []stripFigure {
	out := make([]stripFigure, 0, len(ss))
	for _, s := range ss {
		out = append(out, plainFigure(s))
	}
	return out
}

// drawerRow is one rendered series line.
type drawerRow struct {
	label  string
	counts usage.Counts
}

// spendDrawerRows folds a snapshot's series into the rows the drawer draws.
//
// RANKED ON COST, not on requests or tokens. This is the spend drawer: the row worth
// reading first is the one that cost the most, which for mixed traffic is emphatically not
// the one with the most requests — a handful of opus calls outranks hundreds of haiku ones.
//
// Which is why this does NOT rank through collectSeries, the Usage pane's ranking. That
// function ranks by a usageMetric, and the metrics are tokens, requests, errors and latency
// — there is no cost metric, and adding one would put a fifth entry in the pane's [t] cycle
// and in its persisted settings to serve a surface that is not the pane. Ranking here is
// four lines; the alternative was reshaping a shared enum around this drawer.
//
// The FOLD is still foldTailSeries, which is the part worth sharing: it merges the tail into
// "(other)" AND merges any "(other)" the aggregator already produced by its own label
// capping, so there is never one band meaning "the rest" beside another meaning "the rest".
//
// Summed through usage.Counts.Add, never field by field: Add is the single summation point
// for this struct and its own doc records what happened to the out-of-module copy that
// enumerated the fields by hand — it silently dropped PricedRequests under a comment saying
// every field had to be carried.
func spendDrawerRows(snap *usage.Snapshot, keep int) []drawerRow {
	if snap == nil || len(snap.Buckets) == 0 {
		return nil
	}
	// BOTH RETURNS, and the first one is the whole reason to call this rather than truncate the
	// slice here. foldTailSeries merges an "(other)" the AGGREGATOR already produced — from its
	// own label capping — into the tail total instead of appending a second band. Hand-rolling
	// the truncation appended unconditionally, so a snapshot whose Series already held an
	// "(other)" ranking inside the top three rendered that band TWICE, each carrying the same
	// merged total: the rows then summed past the strip's headline above them by the whole tail.
	ranked, folded := foldTailSeries(snap.Buckets, rankSeriesByCost(snap.Buckets), keep)

	totals := make(map[string]usage.Counts, len(ranked))
	for _, b := range folded {
		for label, c := range b.Series {
			t := totals[label]
			t.Add(c)
			totals[label] = t
		}
	}
	out := make([]drawerRow, 0, len(ranked))
	for _, s := range ranked {
		c, ok := totals[s.label]
		if !ok {
			// Defensive only: foldTailSeries returns exactly the labels its folded buckets
			// carry, so this cannot fire today. It is the guard that stops a future change to
			// either side rendering a row with no counters behind it.
			continue
		}
		out = append(out, drawerRow{label: s.label, counts: c})
	}
	// Stable under equal cost so the rows do not reshuffle under the reader between polls.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].label == tailLabel {
			return false
		}
		if out[j].label == tailLabel {
			return true
		}
		return out[i].counts.CostMicros > out[j].counts.CostMicros
	})
	return out
}

// renderSpendDrawer returns the drawer's rows, ready to join under the strip.
//
// Rows rather than one string, so the caller composes them with the strip and the body in
// one JoinVertical and nothing here needs to know the terminal's height budget.
//
// Empty when there is nothing to break down. An open drawer over a snapshot with no series
// draws its hint line and nothing else — enough to say "this is open and there is nothing
// here", which is a different message from the drawer having failed to open.
// windowLabel is the span in the strip's own vocabulary — "1h", not time.Duration's
// "1h0m0s". Passed in already formatted rather than formatted here, so the drawer's hint and
// the strip's own figure label are produced by one function and cannot drift apart.
func renderSpendDrawer(snap *usage.Snapshot, axis usage.Group, windowLabel string, width int) []string {
	rows := spendDrawerRows(snap, spendDrawerSeries)
	out := make([]string, 0, len(rows)+1)
	for i, r := range rows {
		branch := "├"
		if i == len(rows)-1 {
			branch = "└"
		}
		out = append(out, fitStripFigures(branch, drawerFigures(r), width))
	}
	// The hint line is LAST and always present: it is the only place the two keys and the
	// current axis are written down, and a drawer whose controls are undiscoverable is a
	// drawer nobody changes the axis of.
	out = append(out, fitStripFigures(" ", plainFigures(
		"[a] "+axisHint(axis),
		"[w] "+windowLabel,
		"esc closes",
	), width))
	return out
}

// drawerFigures is one series row's cells, in the drop order fitStripFigures applies.
//
// The LABEL is first, so it is the last thing a narrow terminal gives up — a row whose model
// name has been dropped is unreadable whatever else survives. Cost second, because this is
// the spend drawer and cost is the reason the row is ranked where it is. Requests and tokens
// are context and go last.
//
// Cost through moneyFigure, so a series row carries the same coverage and exactness caveats
// the strip's own figures do, built from THIS row's counters rather than the window's. A
// per-model total that is 3-of-40 priced must say so on its own line; borrowing the window's
// coverage would attach a caveat measured over other traffic.
func drawerFigures(r drawerRow) []stripFigure {
	figs := []stripFigure{{full: r.label, compact: r.label}}
	switch {
	case negativeCost(r.counts.CostMicros):
		// AN IMPOSSIBLE FIGURE IS NOT A FIGURE, and this row is where that guard was missing.
		// The gate below admits anything with PricedRequests > 0, so a series summing negative
		// reached formatUSDCell and printed "$-5.0000" — a credit nobody issued, in a column of
		// costs. Every other money surface in this package refuses it through this same
		// predicate; the drawer is the newest one and the only one that did not inherit it.
		//
		// NO COVERAGE NOTE beside it, unlike the unpriced branch below: the gap may well be zero
		// here — every request priced, and the sum still impossible — so coverage is not what is
		// wrong and naming it would point a reader at the rate table.
		figs = append(figs, plainFigure("cost unavailable"))
	case r.counts.PricedRequests > 0 || r.counts.CostMicros > 0:
		figs = append(figs, moneyFigure(float64(r.counts.CostMicros)/1e6, "",
			gapOf(r.counts), r.counts.PriceableRequests, r.counts.IncompleteRequests, nil,
			r.counts.Saturated))
	case r.counts.PriceableRequests > 0:
		// A SERIES THAT PRICED NOTHING still has to say so, and this branch is the row most in
		// need of a caveat: the gate above skipped moneyFigure entirely, so a 0-of-40-priced
		// model rendered "mystery-model  40 req  900k tokens" with nothing anywhere on the line
		// saying its cost is unknown. "Each row's caveats come from its own counters" held only
		// for rows that managed to price something.
		//
		// The strip's own spelling for a figure nobody can produce, plus the gap, in the slot the
		// money figure would have taken — so the row reads left to right the same way a priced
		// one does.
		figs = append(figs, plainFigure("cost unavailable"),
			plainFigure(coverageNote(gapOf(r.counts), r.counts.PriceableRequests)))
	}
	if r.counts.Requests > 0 {
		figs = append(figs, stripFigure{
			full:    fmt.Sprintf("%d req", r.counts.Requests),
			compact: fmt.Sprintf("%dr", r.counts.Requests),
		})
	}
	if r.counts.Tokens > 0 {
		figs = append(figs, stripFigure{
			full:    humanizeCount(r.counts.Tokens) + " tokens",
			compact: humanizeCount(r.counts.Tokens),
		})
	}
	if r.counts.AvoidedMicros > 0 {
		figs = append(figs, stripFigure{
			full:    "saved " + inexactMarker + formatUSDCell(float64(r.counts.AvoidedMicros)/1e6),
			compact: inexactMarker + formatUSDCell(float64(r.counts.AvoidedMicros)/1e6),
		})
	}
	return figs
}

// gapOf is a row's unpriced count: priceable minus priced, floored at zero.
//
// Floored because the two counters can invert on a row — a response carrying a gateway's
// own cost header but no parsed token counts is priced without being priceable by the
// model-and-tokens test — and a NEGATIVE gap fails every consumer's `> 0` check, so the
// caveat disappears exactly where there is something to caveat. The ledger writer records
// the same inversion and closes it the same way.
func gapOf(c usage.Counts) int64 {
	if gap := c.PriceableRequests - c.PricedRequests; gap > 0 {
		return gap
	}
	return 0
}

// axisHint spells the axis cycle with the current one bracketed, so the line says both what
// `a` will do and where it currently is.
func axisHint(axis usage.Group) string {
	out := ""
	for i, a := range spendDrawerAxes {
		if i > 0 {
			out += " · "
		}
		if a == axis {
			out += "[" + string(a) + "]"
			continue
		}
		out += string(a)
	}
	return out
}

// rankSeriesByCost orders a snapshot's series labels by what they cost, descending.
//
// Mirrors collectSeries' shape — a map of totals, then a sorted slice — and differs only in
// the quantity summed. Ties break on the label so the order is deterministic: two series
// that cost the same must not swap places between polls, which would make the drawer flicker
// under a reader who is trying to compare rows.
func rankSeriesByCost(buckets []usage.Bucket) []seriesKey {
	totals := map[string]int64{}
	for _, b := range buckets {
		for label, c := range b.Series {
			totals[label] += c.CostMicros
		}
	}
	out := make([]seriesKey, 0, len(totals))
	for label, total := range totals {
		out = append(out, seriesKey{label: label, total: total})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].total != out[j].total {
			return out[i].total > out[j].total
		}
		return out[i].label < out[j].label
	})
	return out
}
