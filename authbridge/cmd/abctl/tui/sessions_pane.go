package tui

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"

	"github.com/rossoctl/cortex/authbridge/authlib/pipeline"
)

// sessionsColumns is the table's full-width column set, before any terminal-fitting.
//
// A function rather than a package var so layout() can re-fit from the originals on every
// resize: fitting the LIVE columns would be cumulative, and a terminal that got narrower once
// would keep its narrowed columns after being widened again.
//
// These widths sum to 90 rendered columns (80 declared plus bubbles' two per cell), which is
// why they are fitted rather than used as-is — see fitTableColumns.
func sessionsColumns() []table.Column {
	return []table.Column{
		// SESSION, and 14 wide rather than 40. A session id is a 36-character UUID whose
		// first characters identify it to a human as well as all of them do, and the 26
		// columns it was spending are the ones the money columns need. `/` filters on the
		// FULL id, so nothing is lost for finding a session — only for reading one.
		{Title: "SESSION", Width: 14},
		{Title: "UPDATED", Width: 14},
		{Title: "EVENTS", Width: 8},
		{Title: "TOKENS", Width: 10},
		// COST and SAVED are LIFETIME figures, scoped exactly like TOKENS beside them, so
		// the row is internally consistent. A per-hour cost next to a lifetime token count
		// understated the row by roughly 6x on a long session and invited a reader to
		// derive a rate from two figures measured over different spans.
		//
		// Both come from the server's own sum over the session's events
		// (session.SessionSummary.CostMicros / .AvoidedMicros), not from the strip's ring
		// window: the durable ledger's row key carries no session dimension by design, and
		// the ring covers only its rolling span. So these RESET when the proxy restarts
		// while the strip's "today" figure does not — both are correct, and neither is a
		// check on the other.
		{Title: "COST", Width: 10},
		{Title: "SAVED", Width: 10},
		{Title: "ACTIVE", Width: 8},
	}
}

// sessionsRightAligned names the columns whose cells rebuildSessionsTable right-aligns with
// padLeft, and whose HEADINGS therefore have to be right-aligned too.
//
// One list, read by alignSessionsHeaders below and by the header/cell agreement test, so a
// column cannot be padded in its cells while its heading stays on the far side of the column —
// which is what all four of these did until now. There is no eventColumn-style struct to hang
// the flag on here: the sessions table is a []table.Column whose cells are built inline against
// each column's fitted width, so the set is named instead.
//
// ACTIVE is deliberately absent. Its ● and "cached" are marks rather than figures, nothing is
// compared down the column, and the left edge is where a reader looks for them.
var sessionsRightAligned = map[string]bool{
	"EVENTS": true,
	"TOKENS": true,
	"COST":   true,
	"SAVED":  true,
}

// alignSessionsHeaders right-aligns the headings of the numeric columns, against the widths
// they were FITTED to rather than the widths they declare — see rightAlignHeader.
//
// A copy, never the caller's slice, for the same reason fitTableColumns takes one: the input
// comes from a package-level constructor, and writing through it would make the result
// permanent for every later caller instead of per-rebuild.
//
// Idempotent, because each heading is re-derived from headerTitle rather than padded again:
// re-aligning an already-aligned set is a no-op rather than a heading pushed off its column.
func alignSessionsHeaders(cols []table.Column) []table.Column {
	out := make([]table.Column, len(cols))
	copy(out, cols)
	for i, c := range out {
		if sessionsRightAligned[headerTitle(c)] {
			out[i].Title = rightAlignHeader(headerTitle(c), c.Width)
		}
	}
	return out
}

// newSessionsTable builds an empty sessions table. Columns are fitted to the terminal by
// layout(), which is called on every WindowSizeMsg.
//
// The headings are aligned here too, even though these widths are the unfitted ones: an
// unaligned header would otherwise be what a pane that has not seen a WindowSizeMsg yet
// renders.
func newSessionsTable() table.Model {
	t := table.New(
		table.WithColumns(alignSessionsHeaders(sessionsColumns())),
		table.WithFocused(true),
	)
	t.SetStyles(tableStyles())
	return t
}

// rebuildSessionsTable updates the rows from m.sessions, applies the current
// filter, and keeps the cursor on the previously-selected session if still
// present.
func (m *model) rebuildSessionsTable() {
	// The FULL id of the previously-selected row, read out of band. Taken from the rendered
	// cell it was a truncated string compared against other truncated strings, which happened
	// to work and would have stopped working the moment two ids shared a prefix — at 72
	// columns the SESSION cell fits eight runes.
	prev := ""
	if c := m.sessionsTbl.Cursor(); c >= 0 && c < len(m.sessionRowIDs) {
		prev = m.sessionRowIDs[c]
	}
	now := time.Now()
	// ONE FUNCTION SETS THE HEADER AND THE ROWS, and it is this one. They have to change
	// together — the money columns come and go with the terminal width — and anything that
	// moves one without the other is a crash rather than a cosmetic bug:
	//
	//   - bubbles' SetColumns calls UpdateViewport SYNCHRONOUSLY, and renderRow indexes
	//     cols[i] once per CELL. So installing a 5-column header while 7-cell rows are still
	//     loaded panics inside SetColumns itself — "index out of range [5] with length 5" —
	//     and layout() did exactly that on any resize across 53 columns. A tmux split was
	//     enough. Appending a rebuild after SetColumns cannot help; the panic is inside it.
	//   - The other direction does not crash, it MISALIGNS: a 5-cell row under a 7-column
	//     header puts ACTIVE's dot under COST.
	//
	// Reading the flag off the table's own columns fixed the disagreement WITHIN a rebuild and
	// did nothing for this, because the columns were still set somewhere else. Derived once
	// here now, and used for both, so the two cannot be produced by separate decisions at all.
	showMoney := sessionsShowMoney(m.width)
	// The header this rebuild will install, computed first because the money cells are rendered
	// against their column's FITTED width — the fitter shrinks columns on a narrow terminal, so
	// the declared 10 is a ceiling rather than the budget.
	//
	// Fitted, THEN aligned, and in that order: the alignment pads each heading to the width the
	// fitter settled on, so padding first would pad to a width the column no longer has. Every
	// lookup below still finds its column, because they go through headerTitle.
	want := alignSessionsHeaders(fitTableColumns(sessionsColumnsFor(m.width), m.width))
	costW := sessionsColumnWidth(want, "COST")
	savedW := sessionsColumnWidth(want, "SAVED")
	// The other cells are fitted too: padLeft right-aligns into the FITTED width, so digits
	// line up at whatever width the fitter settled on. Left-aligned numbers were the main
	// reason this table read as ragged — "5" and "105" began at the same column and ended
	// two apart, so no two rows could be compared by eye.
	idW := sessionsColumnWidth(want, "SESSION")
	eventsW := sessionsColumnWidth(want, "EVENTS")
	tokensW := sessionsColumnWidth(want, "TOKENS")
	rows := make([]table.Row, 0, len(m.sessions))
	ids := make([]string, 0, len(m.sessions))
	for _, s := range m.sessions {
		if m.filter != "" && !strings.Contains(s.ID, m.filter) {
			continue
		}
		active := ""
		if s.Active {
			active = "●"
		}
		row := table.Row{
			trunc(s.ID, idW),
			relTime(now, s.UpdatedAt),
			// The server's count, and only ever the server's: it is the complete one.
			// abctl's own cache holds what it snapshotted plus what it has streamed
			// since attaching, which for a session older than the connection is a
			// smaller number — and when handleStreamEvent also wrote this field, the
			// cell flipped between the two on live traffic. The cached-only rows below
			// use len(cached) because the server does not list those at all.
			padLeft(fmt.Sprintf("%d", s.EventCount), eventsW),
			padLeft(sessionTokens(s.TotalTokens, m.events[s.ID]), tokensW),
		}
		if showMoney {
			row = append(row,
				padLeft(sessionMoneyCell(s.CostMicros, false, s.Saturated, costW), costW),
				padLeft(sessionMoneyCell(s.AvoidedMicros, true, s.Saturated, savedW), savedW))
		}
		row = append(row, active)
		rows = append(rows, row)
		// APPENDED IN LOCKSTEP, one line apart, so the two cannot drift: the row carries what
		// a reader sees and this carries what the code acts on.
		ids = append(ids, s.ID)
	}
	// Sessions whose events abctl still holds but the server no longer lists.
	// Retaining the events (#870) is only half a fix if there is no row to
	// select them from: after a proxy restart the server lists nothing, so
	// without this the picker is empty and the retained history is unreachable.
	for _, id := range m.cachedOnlySessionIDs() {
		if m.filter != "" && !strings.Contains(id, m.filter) {
			continue
		}
		cached := m.events[id]
		row := table.Row{
			trunc(id, idW),
			emptyCell,
			padLeft(fmt.Sprintf("%d", len(cached)), eventsW),
			padLeft(sessionTokens(0, cached), tokensW),
		}
		if showMoney {
			// No figures for a session the server no longer lists. abctl holds these
			// events and never held their costs: the money is summed server-side from the
			// session store, and this row exists precisely because that store has
			// forgotten the session. An em dash says "not known here", where $0.00 would
			// say the session was free.
			row = append(row, emptyCell, emptyCell)
		}
		row = append(row, "cached")
		rows = append(rows, row)
		ids = append(ids, id)
	}
	// ONLY WHEN THE HEADER ACTUALLY CHANGES, which is a resize and nothing else. SetRows(nil)
	// resets the viewport's offset, and this function runs on every poll — clearing
	// unconditionally scrolled the picker back under the operator twice a second, which
	// TestSessionsTable_PollRebuildKeepsScrollPosition exists to catch.
	//
	// When it does change, the rows go first: SetColumns renders whatever rows are loaded, so
	// there must be none it could misread. A scroll reset is unavoidable there — the rows are
	// being rebuilt against a different header — and a resize is already a re-layout.
	if !sameColumns(m.sessionsTbl.Columns(), want) {
		m.sessionsTbl.SetRows(nil)
		m.sessionsTbl.SetColumns(want)
	}
	m.sessionsTbl.SetRows(rows)
	// Published with the rows it describes, never separately.
	m.sessionRowIDs = ids

	// Restore cursor position if possible. Through setCursorVisible: a restored row
	// past the first screenful would otherwise land one line below the rendered
	// window, leaving the pane with no highlight — see setCursorVisible.
	if prev != "" {
		for i, id := range ids {
			if id == prev {
				setCursorVisible(&m.sessionsTbl, i)
				return
			}
		}
	}
	setCursorVisible(&m.sessionsTbl, 0)
}

// cachedOnlySessionIDs lists sessions abctl has events for that the server's
// current list omits, sorted so the picker does not reshuffle under the cursor
// on each refresh. Empty caches are skipped: a row advertising zero events
// helps nobody, and snapshotLoadedMsg can create the key with an empty slice.
func (m *model) cachedOnlySessionIDs() []string {
	live := make(map[string]bool, len(m.sessions))
	for _, s := range m.sessions {
		live[s.ID] = true
	}
	out := make([]string, 0, len(m.events))
	for id, evs := range m.events {
		if live[id] || len(evs) == 0 {
			continue
		}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// relTime renders "Ns", "Nm", "Nh" for small deltas; absolute time otherwise.
func relTime(now, t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := now.Sub(t)
	switch {
	case d < time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Format("Jan 2 15:04")
	}
}

// sessionTokens reports the total tokens for a session, compactly: 137,156,234 renders
// as "137.2M".
//
// Grouped digits overflowed the 10-column cell at 100M and were truncated to
// "137,156,2…", which is worse than a rounded figure in every way — it is longer, less
// readable, and its last digits are the ones that got cut. A session total is a
// magnitude, not something anyone reconciles: the exact per-event counts are in the
// events pane, and the exact total is one curl away on /v1/sessions.
//
// Prefers the server-computed count from SessionSummary (authoritative, covers the
// full event backlog even before we've streamed anything for this
// session). Falls back to a client-side sum over the cached events when
// the server returned zero (older authbridge server without token
// aggregation). Returns "—" when neither source has data.
func sessionTokens(serverTotal int, cached []pipeline.SessionEvent) string {
	if serverTotal > 0 {
		return formatCompact(float64(serverTotal))
	}
	var total int
	for i := range cached {
		if cached[i].Phase != pipeline.SessionResponse {
			continue
		}
		if cached[i].Inference == nil {
			continue
		}
		total += cached[i].Inference.TotalTokens
	}
	if total == 0 {
		return "—"
	}
	return formatCompact(float64(total))
}

// selectedSessionID returns the cursor row's session ID, or "".
func (m *model) selectedSessionID() string {
	// OUT OF BAND, never the rendered cell: see model.sessionRowIDs for what reading the cell
	// cost when the SESSION column began truncating.
	c := m.sessionsTbl.Cursor()
	if c < 0 || c >= len(m.sessionRowIDs) {
		return ""
	}
	return m.sessionRowIDs[c]
}

// emptyCell is what a table cell shows for a figure that is NOT KNOWN, as opposed to one
// that is zero. One spelling, because the difference between the two is the whole point and
// a surface that used "0" in one column and "—" in another would erase it.
const emptyCell = "—"

// sessionMoneyCell renders one session's lifetime cost, or its lifetime saving when
// avoided is set.
//
// UNKNOWN AND ZERO ARE THE SAME CELL HERE, and deliberately: micros == 0 means either the
// session's traffic could not be priced or it genuinely charged nothing, and this function
// cannot tell those apart — the server omits the field for both (omitempty on a zero). So it
// prints the em dash for both rather than "$0.00", which would assert the stronger of the
// two readings. That is the standing rule on every money surface in this package: never
// $0.00 for a figure that might be unknown.
//
// A NEGATIVE figure is refused through the shared negativeCost, not clamped: the session API
// sums non-negative per-request figures, so a negative can only come from a broken producer,
// and "-$5.00" in a column of costs reads as a refund nobody issued.
//
// A saving wears inexactMarker unconditionally. It is estimated from a bytes-to-tokens ratio
// and gross of the prompt-cache re-warm — see usage.Counts.AvoidedMicros — and the per-request
// flags that record which caveats applied do not survive summation, so the marker cannot be
// conditional on them without claiming an exactness nothing here can verify.
// saturated marks the figure as a FLOOR: the session's total reached the int64 ceiling and
// was clamped, so the real number is larger by an amount nothing can state. It arrives from
// session.SessionSummary.Saturated, which exists because MaxInt64 micros is about
// $9.2 trillion — a well-formed dollar amount indistinguishable from a measured one.
//
// partialMarker, the glyph that already means "and more" on the spend strip. Two causes share
// it in this cell — a clamped total and, if this column ever renders one, a partially covered
// one — because both mean exactly "the real figure is larger than the number shown" and a
// ten-column cell has no room to say which. The strip has room for a note and distinguishes
// them there. A marker that rides ON the figure is the point: a cell can be truncated to
// nothing but while the number is on screen its caveat is too.
// budget is the column's FITTED width, and the figure is rendered less precisely rather than
// wider when four decimals will not fit.
//
// A bubbles table does not re-flow an overflowing cell, it truncates — and truncating a money
// figure produces a smaller figure that reads as real, which is the one thing every surface here
// refuses. The cell used to be unbounded: "~$936.5777+" is eleven columns against a ten-column
// header, so a session past about $937 overflowed, and the saturated case was the worst of all
// ("~$9223372036854.7754+", twenty-one) because the marker that says "this is a floor" is
// appended to the longest value there is.
//
// PRECISION IS WHAT YIELDS, in order: four decimals, two, none, then humanizeCount's compact
// form, which is itself width-bounded and clamps at ">999T". Four decimals are worth having on a
// cent-scale figure and are noise on a four-figure one, so the ladder costs nothing where it
// matters. The last candidate always fits a sane column, and is returned unconditionally so this
// cannot fall through to an unbounded string.
func sessionMoneyCell(micros int64, avoided, saturated bool, budget int) string {
	if micros == 0 || negativeCost(micros) {
		return emptyCell
	}
	decorate := func(amount string) string {
		if avoided {
			amount = inexactMarker + amount
		}
		if saturated {
			amount += partialMarker
		}
		return amount
	}
	usd := float64(micros) / 1e6
	// A RUNG THAT ROUNDS THE FIGURE TO ZERO IS SKIPPED, not rendered. %.2f turns a sub-cent
	// charge into "$0.00" and %.0f turns anything under fifty cents into "$0" — and a known
	// non-zero cost displayed as free is worse than the unknown one emptyCell stands for. This
	// cell's own rule, fifty lines up, is never $0.00 for a figure that might be unknown; a figure
	// that is KNOWN and shown as nothing breaks it harder.
	//
	// Measured before the guard: at the narrowest widths these columns survive (a six-column
	// budget), $0.0012 rendered "$0.00" in COST and "~$0.00" in SAVED.
	//
	// Each rung carries the rounded value it would print, so "is this rung honest" is one
	// comparison rather than a guess about the format string.
	type rung struct {
		text    string
		rounded float64
	}
	compact := rung{"$" + humanizeCount(int64(usd)), math.Trunc(usd)}
	for _, r := range []rung{
		// formatUSDCell has its own floor: anything positive under half a CENT renders
		// "<$0.01" rather than "$0.00", so this rung never claims zero for a real charge.
		//
		// THE %.2f RUNG THAT USED TO SIT BELOW THIS ONE IS GONE, because formatUSDCell now IS
		// two decimals — the two rendered the identical string, so the ladder had a step that
		// could never change the outcome and only obscured which rung a cell came from. The
		// ladder is now three rungs: cents, whole dollars, compact.
		{formatUSDCell(usd), usd},
		{"$" + fmt.Sprintf("%.0f", usd), math.Round(usd)},
		compact,
	} {
		if r.rounded == 0 {
			continue
		}
		if cell := decorate(r.text); len([]rune(cell)) <= budget {
			return cell
		}
	}
	// NOTHING FITS — or nothing that can be shown WITHOUT rounding a real charge to zero — so
	// nothing is shown. Both are reachable at the narrowest width these columns survive at: the
	// compact form plus both markers is seven columns against a budget the fitter can squeeze to
	// six, and a sub-cent charge has no honest form shorter than "<$0.0001".
	//
	// The em dash rather than a truncation, and rather than dropping the markers to buy two
	// columns: a clipped figure is a smaller figure that reads as real, and the markers are the
	// figure's meaning — "~" says estimated and "+" says floor, so a bare number in their place
	// is a different claim. If it cannot be said correctly it is not said, which is the rule the
	// spend strip's ladder follows for the same reason.
	return emptyCell
}

// sessionsColumnWidth is the fitted width of one named column, or 0 when it is not present.
//
// By headerTitle rather than by the raw Title, so it finds a column whose heading carries
// alignment padding. Against the raw string, a right-aligned COST reads as absent and its cells
// get rendered against a zero budget — which sessionMoneyCell renders as "not known here" for
// every charge.
func sessionsColumnWidth(cols []table.Column, title string) int {
	for _, c := range cols {
		if headerTitle(c) == title {
			return c.Width
		}
	}
	return 0
}

// sessionTokensCellMin is the narrowest TOKENS cell that can hold every value it renders:
// sessionTokens' widest output is six runes ("999.9M"), so six always fits and five never
// does. Named rather than inlined because two things depend on it — the fit decision below
// and TestSessionTokens_FitsEveryFittedWidth, which walks the same boundary.
const sessionTokensCellMin = 6

// sessionUpdatedCellMin is the narrowest UPDATED cell that holds relTime's widest RELATIVE
// form: "just now", eight runes. Every other relative form is shorter ("59s ago", "23h ago"
// are seven).
//
// RELATIVE ONLY, and that limit is a known gap rather than an oversight. Past 24 hours relTime
// switches to an absolute stamp, and "Jan 12 15:04" is TWELVE runes — so a column fitted below
// twelve clips a day-old session's timestamp. The fitter only reaches twelve at about 83
// terminal columns, which means an 80-column terminal — the documented default this table's
// whole fitting ladder was built for — renders "Jan 12 15…" today.
//
// NOT FIXED HERE, and not papered over by raising this constant to twelve either: twelve would
// decline the money columns on every terminal under ~83, which is a much larger regression than
// the clip it prevents, and it would be choosing a display for the common case (a session
// touched seconds ago) to protect the rare one. The real fix is for relTime to offer a narrow
// absolute form and be told its budget, the way sessionTokens already yields precision to its
// column. That is a separate change to a separate function; this constant is deliberately
// scoped to what it can honestly promise.
const sessionUpdatedCellMin = 8

// sessionMoneyCellMin is the narrowest money cell that can hold an honest figure for ANY
// non-zero charge. sessionMoneyCell's last honest rung is formatUSDCell's own floor,
// "<$0.01" at six runes, and SAVED wears the estimate marker on top: "~<$0.01", seven.
// Below that every rung either rounds a real charge to zero — which the ladder skips — or does
// not fit, so the cell can only ever come out as emptyCell.
//
// Deliberately NOT ten, the declared width, even though the fitter's shrink order means the
// columns are at their declared width whenever they survive today. Ten is what the widest
// value happens to need; seven is what honesty needs, and only the second one stays true if a
// column's declared width changes.
//
// SEVEN, DOWN FROM NINE, and the drop is a CONSEQUENCE rather than a tuning: the floor form was
// "<$0.0001" while money rendered at four decimals. Leaving it at nine after the formatter moved
// to cents would not have failed any test — nine still holds an honest figure, it is merely two
// columns wider than one needs — so the cost would have been silent, paid as money columns
// dropped on terminals that could now afford them. TestSessionMoneyCellMin_MatchesTheFormatter
// pins it to the formatter so the next precision change cannot go unnoticed the same way.
const sessionMoneyCellMin = 7

// sessionsShowMoney reports whether this terminal can afford the COST and SAVED columns.
//
// MEASURED, not thresholded. fitTableColumns squeezes the widest column repeatedly until the
// table fits, so two more columns cost every other column width — at 50 columns they took
// TOKENS from 8 runes to 5 and "100.0k" began truncating, which is the exact failure
// TestSessionTokens_FitsEveryFittedWidth exists to catch. A hardcoded "60 columns" would be
// the same fact written as a number that goes stale the moment any column's declared width
// changes; asking the fitter keeps it true by construction.
//
// DROP WHOLE COLUMNS, NEVER CLIP A CELL — the rule the spend strip's ladder follows for the
// same reason. A truncated figure is a wrong figure, and two money columns are not worth
// making the token count unreadable on a narrow terminal.
//
// True before the first layout, when the width is still zero: newSessionsTable is built from
// the declared columns, so the rows must match them, and the first WindowSizeMsg refits both.
// AND ON THE MONEY COLUMNS' OWN FLOOR, which measuring TOKENS alone did not cover. The two
// tests are independent: a budget can be generous enough to leave TOKENS readable and still be
// too small for any honest money figure. Measured before this half existed, with a known
// $0.0012 charge: COST printed the em-dash at widths 53-58 and SAVED at 53-64, because the
// narrowest form that does not round a real charge to zero is "~<$0.0001" at nine runes.
//
// That em-dash is the collision worth refusing. emptyCell means "not known here", and
// sessionMoneyCell falls back to it when nothing fits — so a column kept at a width where the
// fallback is the only outcome renders a KNOWN charge as unknown. That is the same lie as
// "$0.00" for a sub-cent figure, read from the other end, and the rule this file states fifty
// lines above sessionMoneyCell forbids it in both directions.
//
// The cost is real and measured: the columns now disappear below 72 columns, where an ordinary
// "$36.58" would still have fitted. Dropping a whole column is this file's stated answer to
// not being able to render a cell honestly, and a reader who cannot see COST at all goes
// looking for the width; one who sees "—" against a session that definitely spent money
// concludes the figure is missing from the server.
func sessionsShowMoney(termWidth int) bool {
	if termWidth <= 0 {
		return true
	}
	fitted := fitTableColumns(sessionsColumns(), termWidth)
	for _, c := range fitted {
		if headerTitle(c) == "TOKENS" && c.Width < sessionTokensCellMin {
			return false
		}
		// UPDATED is measured on the same footing as TOKENS, and it has to be: the money
		// columns are paid for out of EVERY other column's width, so any column with a
		// minimum of its own is a reason to decline them.
		//
		// LEAVING IT OUT HID WHICH CONSTRAINT WAS BINDING. Between 59 and 68 columns the
		// fitter left UPDATED 6 or 7 runes where "just now" needs 8, and the money columns
		// were only declined there because sessionMoneyCellMin was two columns wider than
		// the money cells actually needed. So the right answer came out of the wrong
		// reason, and narrowing that constant to what it claims to measure turned a
		// correct decision into a clipped cell.
		if c.Title == "UPDATED" && c.Width < sessionUpdatedCellMin {
			return false
		}
	}
	for _, title := range []string{"COST", "SAVED"} {
		if sessionsColumnWidth(fitted, title) < sessionMoneyCellMin {
			return false
		}
	}
	return true
}

// sessionsColumnsFor is the column set this terminal actually gets. Paired with
// sessionsShowMoney in rebuildSessionsTable so the row arity always matches the header.
func sessionsColumnsFor(termWidth int) []table.Column {
	cols := sessionsColumns()
	if sessionsShowMoney(termWidth) {
		return cols
	}
	out := make([]table.Column, 0, len(cols))
	for _, c := range cols {
		if t := headerTitle(c); t == "COST" || t == "SAVED" {
			continue
		}
		out = append(out, c)
	}
	return out
}

// sameColumns reports whether two header sets are identical in titles and widths.
//
// Both matter. A title change is the money columns coming or going, and a WIDTH change is the
// fitter squeezing the same columns for a narrower terminal — either one means the loaded rows
// were measured against a different header, and only a change justifies the scroll reset that
// reinstalling them costs.
//
// The RAW titles, alignment padding included, because both sides are post-alignment header sets
// and that padding is derived from the width this compares anyway — so it can neither miss a
// change nor invent one.
func sameColumns(a, b []table.Column) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Title != b[i].Title || a[i].Width != b[i].Width {
			return false
		}
	}
	return true
}
