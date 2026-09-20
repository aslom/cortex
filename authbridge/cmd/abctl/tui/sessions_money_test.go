package tui

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/lipgloss"

	"github.com/rossoctl/cortex/authbridge/authlib/pipeline"
	"github.com/rossoctl/cortex/authbridge/authlib/session"
)

// The two money cells, lifetime-scoped, from the server's own per-session sums.
func TestSessionsPicker_ShowsLifetimeCostAndSaving(t *testing.T) {
	m := &model{width: 200}
	m.sessionsTbl = newSessionsTable()
	m.sessions = []session.SessionSummary{{
		ID: "abc", UpdatedAt: time.Now(), EventCount: 315,
		TotalTokens: 77_980_000, CostMicros: 36_577_700, AvoidedMicros: 366_100,
	}}
	m.rebuildSessionsTable()

	rows := m.sessionsTbl.Rows()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := strings.Join(rows[0], " ")
	if !strings.Contains(row, "$36.5777") {
		t.Errorf("row %q is missing the session's lifetime cost", row)
	}
	// The saving wears the estimate marker, and is NOT added to the cost beside it.
	if !strings.Contains(row, inexactMarker+"$0.3661") {
		t.Errorf("row %q is missing the saving, or is missing its %q marker", row, inexactMarker)
	}
	if strings.Contains(row, "$36.9438") {
		t.Errorf("row %q folded the saving into the cost", row)
	}
}

// Unknown is not zero, and this column must never say a session was free.
//
// A zero reaches this cell for two different reasons — nothing in the session could be
// priced, or it genuinely charged nothing — and the wire cannot tell them apart, because the
// server omits the field for both. So the em dash covers both rather than "$0.00", which
// would assert the stronger reading. A NEGATIVE figure is refused on the same footing: the
// session API sums non-negative figures, so it can only come from a broken producer, and a
// minus sign in a column of costs reads as a refund nobody issued.
func TestSessionMoneyCell_NeverAssertsFreeOrARefund(t *testing.T) {
	for _, tc := range []struct {
		name    string
		micros  int64
		avoided bool
		want    string
	}{
		{name: "zero cost", micros: 0, want: emptyCell},
		{name: "zero saving", micros: 0, avoided: true, want: emptyCell},
		{name: "negative cost", micros: -5_000_000, want: emptyCell},
		{name: "negative saving", micros: -1, avoided: true, want: emptyCell},
		{name: "real cost", micros: 36_577_700, want: "$36.5777"},
		{name: "real saving", micros: 366_100, avoided: true, want: inexactMarker + "$0.3661"},
		// Sub-floor but real: formatUSDCell's job, asserted here so a tiny charge cannot
		// arrive in this column as "$0.0000" and read as free.
		{name: "sub-floor cost", micros: 20, want: "<$0.0001"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sessionMoneyCell(tc.micros, tc.avoided, false, sessionsMoneyWidth); got != tc.want {
				t.Errorf("sessionMoneyCell(%d, %v, false) = %q, want %q", tc.micros, tc.avoided, got, tc.want)
			}
		})
	}
}

// The money columns are dropped whole on a terminal that cannot hold them, rather than
// squeezing every other column until a figure truncates — the rule the spend strip's ladder
// follows, applied to a table.
//
// 50 columns is the case that forced it: two extra columns took TOKENS from 8 runes to 5,
// and "100.0k" needs 6. Asserted as a pair with a wide terminal, because a predicate that
// answered false everywhere would satisfy the narrow half on its own and silently cost the
// feature.
func TestSessionsColumns_MoneyColumnsYieldRatherThanTruncateTokens(t *testing.T) {
	wide := sessionsColumnsFor(200)
	if !hasColumn(wide, "COST") || !hasColumn(wide, "SAVED") {
		t.Errorf("a 200-column terminal dropped the money columns: %v", titles(wide))
	}
	narrow := sessionsColumnsFor(50)
	if hasColumn(narrow, "COST") || hasColumn(narrow, "SAVED") {
		t.Errorf("a 50-column terminal kept the money columns: %v — TOKENS truncates there",
			titles(narrow))
	}
	// And the point of dropping them: TOKENS stays legible at every width.
	for _, term := range []int{200, 90, 60, 50, 46, 40} {
		for _, c := range fitTableColumns(sessionsColumnsFor(term), term) {
			if c.Title == "TOKENS" && term >= 40 && c.Width < sessionTokensCellMin {
				t.Errorf("term %d: TOKENS fitted to %d, below the %d runes its widest value "+
					"needs", term, c.Width, sessionTokensCellMin)
			}
		}
	}
}

// A row must always carry exactly as many cells as the header has columns. They are decided
// by two separate calls — layout() sets the columns, rebuildSessionsTable builds the rows —
// so a predicate consulted in one place and not the other would misalign every cell after
// TOKENS, which renders as data under the wrong headings rather than as an error.
func TestSessionsPicker_RowArityMatchesTheHeaderAtEveryWidth(t *testing.T) {
	for _, term := range []int{200, 90, 60, 50, 40} {
		// Height as well as width: layout() returns early until both are known, and a
		// fixture that set only the width was testing a state bubbletea never produces.
		m := &model{width: term, height: 40}
		m.sessionsTbl = newSessionsTable()
		m.sessions = []session.SessionSummary{{ID: "abc", UpdatedAt: time.Now(), CostMicros: 1}}
		m.events = map[string][]pipeline.SessionEvent{"cached-one": {{}}}
		m.layout()
		m.rebuildSessionsTable()

		want := len(m.sessionsTbl.Columns())
		for i, r := range m.sessionsTbl.Rows() {
			if len(r) != want {
				t.Errorf("term %d row %d has %d cells, header has %d: %v", term, i, len(r), want, r)
			}
		}
	}
}

// titles and hasColumn below name columns the way the code does, through headerTitle.
//
// Both take any column set, including one off a live table whose numeric headings carry
// alignment padding — and an unpadded name is what a caller writes. Comparing the raw Title
// made them silently wrong on exactly those sets: see TestSessionsRows_RightAlignNumericCells,
// which that mistake reduced to a no-op.
func titles(cols []table.Column) []string {
	out := make([]string, 0, len(cols))
	for _, c := range cols {
		out = append(out, headerTitle(c))
	}
	return out
}

// A CLAMPED total must not render as a measured one.
//
// session.SessionSummary.Saturated exists because MaxInt64 micros is about $9.2 trillion — a
// well-formed dollar amount no reader can tell apart from a real figure. The server publishing
// the flag and this cell ignoring it would be the same defect one layer up: the honest number
// is there and the screen still lies.
func TestSessionMoneyCell_AClampedFigureIsMarkedAsAFloor(t *testing.T) {
	plain := sessionMoneyCell(36_577_700, false, false, sessionsMoneyWidth)
	clamped := sessionMoneyCell(36_577_700, false, true, sessionsMoneyWidth)
	if plain == clamped {
		t.Fatalf("a clamped figure renders identically to a measured one (%q): the flag reached "+
			"the client and the cell dropped it", plain)
	}
	if !strings.HasSuffix(clamped, partialMarker) {
		t.Errorf("clamped cell = %q, want the %q suffix that already means \"and more\" on the "+
			"strip", clamped, partialMarker)
	}
	// The saving keeps its own marker as well: the two say different things and one must not
	// displace the other.
	saved := sessionMoneyCell(366_100, true, true, sessionsMoneyWidth)
	if !strings.HasPrefix(saved, inexactMarker) || !strings.HasSuffix(saved, partialMarker) {
		t.Errorf("clamped saving = %q, want both %q (estimated) and %q (a floor)",
			saved, inexactMarker, partialMarker)
	}
}

// hasColumn reports whether a header set carries the named column. Local to the tests: the
// production side no longer asks that question — rebuildSessionsTable derives the money flag
// from the width and sets the header itself — so a helper for it would be dead code.
func hasColumn(cols []table.Column, title string) bool {
	for _, c := range cols {
		if headerTitle(c) == title {
			return true
		}
	}
	return false
}

// RESIZING one model across the money-column boundary, in both directions.
//
// This is the case TestSessionsPicker_RowArityMatchesTheHeaderAtEveryWidth could not see: it
// builds a FRESH model per width, so the header and the rows are always created together and no
// transition is ever crossed. A real terminal transitions — a tmux split is enough — and on the
// way down it CRASHED:
//
//	index out of range [5] with length 5
//
// inside layout(), because bubbles' SetColumns calls UpdateViewport synchronously and renderRow
// indexes cols[i] once per cell, so a 5-column header met 7-cell rows. On the way up it did not
// crash, it put ACTIVE's dot under COST.
//
// 53 is the narrowest width that keeps the money columns and 52 the widest that drops them, so
// the sequence walks across that boundary twice and ends where it started.
func TestSessionsPicker_ResizingAcrossTheMoneyBoundaryNeitherPanicsNorMisaligns(t *testing.T) {
	m := &model{width: 200, height: 40}
	m.sessionsTbl = newSessionsTable()
	m.sessions = []session.SessionSummary{
		{ID: "abc", UpdatedAt: time.Now(), EventCount: 3, TotalTokens: 1_000, CostMicros: 36_577_700},
		{ID: "def", UpdatedAt: time.Now(), EventCount: 1, TotalTokens: 10},
	}
	m.events = map[string][]pipeline.SessionEvent{"cached-one": {{}}}
	m.layout()

	for _, w := range []int{200, 53, 52, 40, 52, 53, 200} {
		// THE PRODUCTION SEQUENCE, and getting it wrong made the first version of this test
		// unable to fail: a poll builds the rows at the CURRENT width, and the resize arrives
		// afterwards. Leaving it to layout() to populate them meant the table was empty when the
		// old code ran, so the arity loop below iterated nothing and the panic never fired.
		m.rebuildSessionsTable()
		if len(m.sessionsTbl.Rows()) == 0 {
			t.Fatalf("width %d: no rows to check — every assertion below would pass vacuously", w)
		}

		m.width = w
		// layout() is what a WindowSizeMsg runs, and it is where the panic was.
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("width %d: layout() panicked: %v — a resize across the money-column "+
						"boundary crashes the TUI", w, r)
				}
			}()
			m.layout()
		}()

		cols := m.sessionsTbl.Columns()
		if len(m.sessionsTbl.Rows()) == 0 {
			t.Fatalf("width %d: the resize emptied the table", w)
		}
		for i, r := range m.sessionsTbl.Rows() {
			if len(r) != len(cols) {
				t.Fatalf("width %d: row %d has %d cells against a %d-column header %v: the cells "+
					"render under the wrong headings", w, i, len(r), len(cols), titles(cols))
			}
		}
		// And the last column really is the one the last cell belongs to, which is what
		// misalignment actually looks like on screen.
		//
		// headerTitle, though ACTIVE is deliberately not in sessionsRightAligned and so arrives
		// unpadded: this reads a LIVE header set, and the assertion should survive ACTIVE
		// joining that set rather than start reporting a wrong last column.
		if last := headerTitle(cols[len(cols)-1]); last != "ACTIVE" {
			t.Errorf("width %d: last column is %q, want ACTIVE — the header itself is wrong", w, last)
		}
	}
}

// sessionsMoneyWidth is the declared width of the COST and SAVED columns, which is the budget a
// terminal wide enough to show them gives. Read from the column set rather than written as 10, so
// these tests follow a change to it.
var sessionsMoneyWidth = sessionsColumnWidth(sessionsColumns(), "COST")

// NO MONEY CELL MAY EXCEED ITS COLUMN. A bubbles table truncates rather than re-flowing, and a
// truncated money figure is a smaller figure that reads as real — "$936.5777" clipped to
// "$936.57" is a plausible number that is simply wrong.
//
// The cell was unbounded: "~$936.5777+" is eleven columns against ten, so a session past about
// $937 overflowed, and the saturated case was twenty-one because the marker meaning "this is a
// floor" is appended to the longest value there is. Markers are the reason the boundary is that
// low — they cost two of the ten columns.
//
// Every magnitude, both markers, and the narrow fitted widths too, since the fitter shrinks these
// columns before it drops them.
func TestSessionMoneyCell_NeverExceedsItsColumn(t *testing.T) {
	for _, budget := range []int{sessionsMoneyWidth, 8, 6} {
		for _, micros := range []int64{
			1,              // sub-floor, renders "<$0.0001"
			20,             // likewise
			36_577_700,     // $36.5777 — the ordinary case
			936_577_700,    // $936.5777 — where the overflow started
			99_999_990_000, // $99,999.99
			math.MaxInt64,  // the saturated clamp, the longest value there is
		} {
			for _, avoided := range []bool{false, true} {
				for _, saturated := range []bool{false, true} {
					got := sessionMoneyCell(micros, avoided, saturated, budget)
					if n := len([]rune(got)); n > budget {
						t.Errorf("micros=%d avoided=%v saturated=%v budget=%d: cell %q is %d "+
							"columns — the table truncates it into a smaller figure that reads "+
							"as real", micros, avoided, saturated, budget, got, n)
					}
					// And it always says SOMETHING — a coarse figure where one fits, the em dash
					// where none does. A blank cell would read as a rendering fault.
					if got == "" {
						t.Errorf("micros=%d budget=%d: blank cell", micros, budget)
					}
				}
			}
		}
	}
	// Precision is what yields, and only when it has to: at the declared width an ordinary
	// figure keeps all four decimals.
	if got := sessionMoneyCell(36_577_700, false, false, sessionsMoneyWidth); got != "$36.5777" {
		t.Errorf("cell = %q at the declared width, want the full $36.5777 — the ladder is giving "+
			"up precision it does not need to", got)
	}
}

// A KNOWN NON-ZERO CHARGE MUST NEVER RENDER AS ZERO.
//
// The precision ladder gives up decimals to fit a narrow column, and two of its rungs round a
// sub-cent figure away entirely: %.2f makes $0.0012 into "$0.00" and %.0f makes anything under
// fifty cents into "$0". This cell's own rule is never $0.00 for a figure that might be unknown —
// and a figure that is KNOWN and shown as nothing breaks it harder, because "free" is a claim
// about the traffic.
//
// Every width where these columns survive, both markers, and the sub-cent magnitudes the ladder
// reaches for.
func TestSessionMoneyCell_NeverRendersAKnownChargeAsZero(t *testing.T) {
	// The budgets the fitter actually produces for these columns, narrowest first.
	budgets := map[int]bool{}
	for term := 40; term <= 200; term++ {
		if !sessionsShowMoney(term) {
			continue
		}
		cols := fitTableColumns(sessionsColumnsFor(term), term)
		budgets[sessionsColumnWidth(cols, "COST")] = true
		budgets[sessionsColumnWidth(cols, "SAVED")] = true
	}
	if len(budgets) == 0 {
		t.Fatal("no width keeps the money columns, so nothing below is exercised")
	}

	for budget := range budgets {
		for _, micros := range []int64{1, 12, 1200, 5_000, 499_000} { // $0.000001 … $0.499
			for _, avoided := range []bool{false, true} {
				for _, saturated := range []bool{false, true} {
					got := sessionMoneyCell(micros, avoided, saturated, budget)
					// A zero-valued amount is the defect; the em dash is the honest fallback.
					for _, zero := range []string{"$0.00", "$0.0000", "$0 ", "$0"} {
						if got == zero || got == inexactMarker+zero ||
							got == zero+partialMarker || got == inexactMarker+zero+partialMarker {
							t.Errorf("micros=%d budget=%d avoided=%v saturated=%v: cell %q shows a "+
								"real charge as nothing — free is a claim about the traffic",
								micros, budget, avoided, saturated, got)
						}
					}
					if n := len([]rune(got)); n > budget {
						t.Errorf("micros=%d budget=%d: cell %q is %d columns", micros, budget, got, n)
					}
				}
			}
		}
	}

	// And where there IS room, the sub-cent figure is stated rather than dropped — the guard must
	// skip dishonest rungs, not every rung.
	if got := sessionMoneyCell(1200, false, false, sessionsMoneyWidth); got != "<$0.0001" && got != "$0.0012" {
		t.Errorf("cell = %q at the declared width, want the sub-cent figure stated", got)
	}
}

// Every width that keeps the money columns can render an honest figure in them.
//
// THE COLLISION THIS REFUSES: emptyCell means "not known here", and sessionMoneyCell falls
// back to it when no rung fits without rounding a real charge to zero. So a width that keeps
// COST but cannot fit "<$0.0001" renders a KNOWN charge as unknown — the same lie as "$0.00"
// for a sub-cent figure, read from the other end. Measured before sessionsShowMoney gained its
// money half: a $0.0012 charge printed "—" in COST at widths 53-58 and in SAVED at 53-64.
//
// Sub-cent deliberately, because that is the figure the narrow widths could not express; an
// ordinary $36.58 fits in six runes and would pass at widths this test rejects.
func TestSessionsShowMoney_EveryKeptWidthFitsASubCentCharge(t *testing.T) {
	const subCent = 1_200 // $0.0012
	kept := 0
	for termWidth := 1; termWidth <= 200; termWidth++ {
		if !sessionsShowMoney(termWidth) {
			continue
		}
		kept++
		cols := fitTableColumns(sessionsColumnsFor(termWidth), termWidth)
		for _, c := range []struct {
			title   string
			avoided bool
		}{{"COST", false}, {"SAVED", true}} {
			budget := sessionsColumnWidth(cols, c.title)
			cell := sessionMoneyCell(subCent, c.avoided, false, budget)
			if cell == emptyCell {
				t.Errorf("width %d: %s has a %d-rune budget, which renders a known $0.0012 as %q "+
					"— the cell that means \"not known here\"", termWidth, c.title, budget, cell)
			}
		}
	}
	if kept == 0 {
		t.Fatal("no width kept the money columns, so the loop asserted nothing")
	}
}

// The cells are rendered against the column's FITTED width, not its declared one.
//
// rebuildSessionsTable reads each money column's fitted width because the fitter shrinks
// columns on a narrow terminal — and until this test the wiring was exercised only at width
// 200, where fitted and declared are both 10. A regression passing the declared 10 instead
// passed every other test in this file.
//
// The gap is real and narrow: measured across every width, COST fits to 9 rather than 10 at
// terminal widths 72-76 (SAVED at 72-75), the band just above the floor where sessionsShowMoney
// drops them. So the fixture is a charge whose honest form needs all ten runes — $1234.5678 —
// because a $36.58 cell fits either budget and cannot tell the two apart.
func TestSessionsMoneyCells_UseTheFittedWidthNotTheDeclaredOne(t *testing.T) {
	const bigCost = 1_234_567_800 // $1234.5678, ten runes
	shrunken := 0
	for termWidth := 1; termWidth <= 200; termWidth++ {
		if !sessionsShowMoney(termWidth) {
			continue
		}
		cols := fitTableColumns(sessionsColumnsFor(termWidth), termWidth)
		budget := sessionsColumnWidth(cols, "COST")
		declared := 0
		for _, c := range sessionsColumns() {
			if c.Title == "COST" {
				declared = c.Width
			}
		}
		if budget >= declared {
			continue
		}
		shrunken++

		m := &model{width: termWidth}
		m.sessionsTbl = newSessionsTable()
		m.sessions = []session.SessionSummary{{
			ID: "abc", UpdatedAt: time.Now(), EventCount: 3, CostMicros: bigCost,
		}}
		m.rebuildSessionsTable()
		rows := m.sessionsTbl.Rows()
		if len(rows) != 1 {
			t.Fatalf("width %d: rows = %d, want 1", termWidth, len(rows))
		}
		row := strings.Join(rows[0], " ")
		// What the fitted budget can honestly hold.
		if want := sessionMoneyCell(bigCost, false, false, budget); !strings.Contains(row, want) {
			t.Errorf("width %d: row %q does not carry %q, the cell a %d-rune budget allows",
				termWidth, row, want, budget)
		}
		// And emphatically not the wider form the declared width would have allowed, which
		// bubbles/table would then truncate.
		if wide := sessionMoneyCell(bigCost, false, false, declared); wide != "" &&
			len([]rune(wide)) > budget && strings.Contains(row, wide) {
			t.Errorf("width %d: row %q carries %q, %d runes in a %d-rune column — the cell was "+
				"built against the declared width", termWidth, row, wide, len([]rune(wide)), budget)
		}
	}
	if shrunken == 0 {
		t.Fatal("no width shrinks COST below its declared size, so this test asserted nothing " +
			"about the fitted budget")
	}
}

// And the rendered cells never exceed the column they sit in, at every width that keeps them.
//
// Row arity was already pinned; cell WIDTH was not, and a cell wider than its column is what
// bubbles/table truncates — the clipped figure this file's own rule forbids.
func TestSessionsMoneyCells_NeverOutgrowTheirColumn(t *testing.T) {
	widths := 0
	for termWidth := 40; termWidth <= 200; termWidth++ {
		if !sessionsShowMoney(termWidth) {
			continue
		}
		widths++
		m := &model{width: termWidth}
		m.sessionsTbl = newSessionsTable()
		m.sessions = []session.SessionSummary{{
			ID: "abc", UpdatedAt: time.Now(), EventCount: 315,
			TotalTokens: 77_980_000, CostMicros: 36_577_700, AvoidedMicros: 366_100,
		}}
		m.rebuildSessionsTable()
		rows := m.sessionsTbl.Rows()
		if len(rows) != 1 {
			t.Fatalf("width %d: rows = %d, want 1", termWidth, len(rows))
		}
		cols := m.sessionsTbl.Columns()
		for i, cell := range rows[0] {
			if i >= len(cols) {
				t.Fatalf("width %d: row has %d cells for %d columns", termWidth, len(rows[0]), len(cols))
			}
			if n := len([]rune(cell)); n > cols[i].Width {
				t.Errorf("width %d: %s cell %q is %d runes in a %d-rune column",
					termWidth, cols[i].Title, cell, n, cols[i].Width)
			}
		}
	}
	if widths == 0 {
		t.Fatal("no width kept the money columns, so the loop asserted nothing")
	}
}

// Numerics are right-aligned, so the digits line up and a column reads as a column.
//
// Left-aligned numbers were the main reason the table read as ragged: "105" and "3" started
// at the same column and ended three apart, so no two rows could be compared by eye.
//
// EVERY LOOKUP HERE IS BY headerTitle, and the count of columns reached is asserted at the
// bottom. Selecting on the raw Title left this test DEAD the moment the headings grew their
// alignment padding: " EVENTS" matches no case, every column took the default and continued,
// and the "asserted nothing" guard was itself inside the skipped block. Measured — with the
// four cells below switched from padLeft to a right-padding helper, the precise defect this
// test is named for, it still reported PASS. Hence the outer guard: a per-column guard cannot
// notice that no column was examined.
func TestSessionsRows_RightAlignNumericCells(t *testing.T) {
	m := &model{width: 200}
	m.sessionsTbl = newSessionsTable()
	m.sessions = []session.SessionSummary{
		{ID: "aaa", UpdatedAt: time.Now(), EventCount: 5, TotalTokens: 1_000, CostMicros: 1_000_000},
		{ID: "bbb", UpdatedAt: time.Now(), EventCount: 105, TotalTokens: 7_000_000, CostMicros: 5_834_400},
	}
	m.rebuildSessionsTable()

	cols := m.sessionsTbl.Columns()
	rows := m.sessionsTbl.Rows()
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	checked := 0
	for ci, col := range cols {
		title := headerTitle(col)
		if !sessionsRightAligned[title] {
			continue
		}
		checked++
		// Every non-empty cell in a numeric column ends at the same column, which is what
		// right-alignment means and what left-alignment cannot give.
		var seen int
		for ri, row := range rows {
			cell := row[ci]
			if strings.TrimSpace(cell) == "" {
				continue
			}
			seen++
			if got := len([]rune(cell)); got != col.Width {
				t.Errorf("row %d %s cell %q is %d runes in a %d-wide column — not right-aligned",
					ri, title, cell, got, col.Width)
			}
			if strings.HasSuffix(cell, " ") {
				t.Errorf("row %d %s cell %q is padded on the right, so it is left-aligned",
					ri, title, cell)
			}
		}
		if seen == 0 {
			t.Errorf("%s column had no non-empty cell, so this asserted nothing", title)
		}
	}
	if checked != len(sessionsRightAligned) {
		t.Fatalf("examined %d of the %d right-aligned columns %v in %v — the rest were not "+
			"matched at all, so this test asserted nothing about them",
			checked, len(sessionsRightAligned), sessionsRightAligned, titles(cols))
	}
}

// A 36-character UUID is truncated: it is the least readable thing on the row and was taking
// 40 columns to say less than its first twelve do.
func TestSessionsRows_TruncateTheSessionID(t *testing.T) {
	m := &model{width: 100}
	m.sessionsTbl = newSessionsTable()
	const full = "ecb7387f-bffd-4172-adc4-8da23e992e9a"
	m.sessions = []session.SessionSummary{{ID: full, UpdatedAt: time.Now(), EventCount: 1}}
	m.rebuildSessionsTable()

	cell := m.sessionsTbl.Rows()[0][0]
	if cell == full {
		t.Errorf("the full UUID is in the cell: %q", cell)
	}
	if !strings.HasPrefix(cell, "ecb7387f") {
		t.Errorf("cell %q lost the identifying prefix", cell)
	}
	idW := sessionsColumnWidth(m.sessionsTbl.Columns(), "SESSION")
	if idW == 0 {
		t.Fatal("no SESSION column")
	}
	if n := len([]rune(cell)); n > idW {
		t.Errorf("cell %q is %d runes in a %d-wide column", cell, n, idW)
	}
	// A short id is untouched: truncation is for the ones that need it.
	m.sessions = []session.SessionSummary{{ID: "default", UpdatedAt: time.Now()}}
	m.rebuildSessionsTable()
	if got := m.sessionsTbl.Rows()[0][0]; got != "default" {
		t.Errorf("short id rendered as %q, want %q untouched", got, "default")
	}
}

// THE SCOPE IS ON SCREEN, not only in this file's comments.
//
// The reader's report that started this: a spend strip reading 84.6M tokens for its rolling hour,
// directly above a TOKENS column summing to 123.8M for the same traffic. Both correct, three spans
// on one screen (hour, day, lifetime) and one of them stated. The strip now names its own span as
// a group prefix; this is the other half.
func TestPaneView_SessionsTitleStatesTheLifetimeScope(t *testing.T) {
	m := &model{width: 160, height: 40, endpoint: "http://x"}
	m.pane = paneSessions
	m.sessionsTbl = newSessionsTable()
	m.layout()

	out := m.paneView()
	title := strings.SplitN(out, "\n", 2)[0]
	if !strings.Contains(title, "lifetime") {
		t.Errorf("sessions title %q does not say the figures below it are lifetime totals; a reader "+
			"comparing the TOKENS column against the strip's window has nothing telling them the "+
			"two are different questions", title)
	}
}

// AND IT YIELDS RATHER THAN WRAPS. The title is not width-fitted, so an unconditional suffix
// pushes it past a narrow terminal — and a wrapped title costs a row of the table it describes,
// which is the same failure renderSpendStrip's whole ladder is built to avoid.
//
// THE ASSERTION IS "THE NOTE NEVER OVERFLOWS", NOT "THE TITLE NEVER DOES", and the difference is a
// finding rather than a weakening: the base title already overflows without any note at all —
// "abctl · http://x · [Sessions] Pipeline" is 38 columns and nothing fits it, so every pane's
// title wraps on a terminal narrower than itself. That predates this change and is left alone
// here; what this pins is that the note is not a new way to reach it.
//
// Asserted across every width rather than at one chosen number, because the note's length and the
// endpoint's are both free to change while the invariant is neither.
func TestPaneView_SessionsScopeNoteNeverOverflowsTheTerminal(t *testing.T) {
	for _, endpoint := range []string{"http://x", "http://weather-agent-7f9c.team1.svc:9094"} {
		for w := 20; w <= 200; w++ {
			m := &model{width: w, height: 40, endpoint: endpoint}
			m.pane = paneSessions
			m.sessionsTbl = newSessionsTable()
			m.layout()

			title := strings.SplitN(m.paneView(), "\n", 2)[0]
			if !strings.Contains(title, strings.TrimSpace(sessionsScopeNote)) {
				continue // dropped, which is the honest degradation
			}
			if got := lipgloss.Width(title); got > w {
				t.Fatalf("width %d, endpoint %q: title carries the scope note at %d columns (%q) "+
					"and will wrap", w, endpoint, got, title)
			}
		}
	}
}

// And it IS added wherever there is room, so the guard above cannot be satisfied by never adding
// it at all. The widest fixture leaves 90 columns of slack, so a version that dropped the note
// unconditionally would pass every assertion in the test above.
func TestPaneView_SessionsScopeNoteAppearsWhenItFits(t *testing.T) {
	m := &model{width: 200, height: 40, endpoint: "http://x"}
	m.pane = paneSessions
	m.sessionsTbl = newSessionsTable()
	m.layout()

	if title := strings.SplitN(m.paneView(), "\n", 2)[0]; !strings.Contains(title, sessionsScopeNote) {
		t.Errorf("sessions title %q omits the scope note on a 200-column terminal", title)
	}
}
