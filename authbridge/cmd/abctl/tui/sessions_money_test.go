package tui

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/table"

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
	if !strings.Contains(row, "$36.58") {
		t.Errorf("row %q is missing the session's lifetime cost", row)
	}
	// The saving is present as a bare figure, and is NOT added to the cost beside it. Its
	// estimate marker lives on the COLUMN now — asserted just below, against the header.
	if !strings.Contains(row, "$0.37") {
		t.Errorf("row %q is missing the saving", row)
	}
	if strings.Contains(row, inexactMarker+"$0.37") {
		t.Errorf("row %q still marks the saving per value; headerMarker carries that on the "+
			"heading, and both would be the caveat stated twice", row)
	}
	// AND THE CAVEAT IS STILL ON SCREEN, which is the half that makes moving it legitimate
	// rather than a quiet deletion. Asserted here, next to the value losing the marker, so a
	// change that dropped both cannot pass.
	var savedHeading string
	for _, c := range m.sessionsTbl.Columns() {
		if headerTitle(c) == "SAVED" {
			savedHeading = c.Title
		}
	}
	if !strings.Contains(savedHeading, inexactMarker) {
		t.Errorf("SAVED heading = %q, want it to carry %q — the estimate caveat came off the "+
			"values and has to be somewhere", savedHeading, inexactMarker)
	}
	if strings.Contains(row, "$36.94") {
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
		{name: "real cost", micros: 36_577_700, want: "$36.58"},
		// A saving renders exactly like a cost now: identical figure, no per-value marker. The
		// "estimated" caveat is the SAVED column's, carried by its heading.
		{name: "real saving", micros: 366_100, avoided: true, want: "$0.37"},
		// Sub-floor but real: formatUSDCell's job, asserted here so a tiny charge cannot
		// arrive in this column as "$0.00" and read as free.
		{name: "sub-floor cost", micros: 20, want: "<$0.01"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sessionMoneyCell(tc.micros, false, sessionsMoneyWidth); got != tc.want {
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
	plain := sessionMoneyCell(36_577_700, false, sessionsMoneyWidth)
	clamped := sessionMoneyCell(36_577_700, true, sessionsMoneyWidth)
	if plain == clamped {
		t.Fatalf("a clamped figure renders identically to a measured one (%q): the flag reached "+
			"the client and the cell dropped it", plain)
	}
	if !strings.HasSuffix(clamped, partialMarker) {
		t.Errorf("clamped cell = %q, want the %q suffix that already means \"and more\" on the "+
			"strip", clamped, partialMarker)
	}
	// A clamped SAVING carries the floor marker too, and ONLY that one: "estimated" is the
	// column's property and sits in its heading, while "this is a floor" is conditional on this
	// row's own total and therefore still rides on the figure. That split is the whole rule —
	// see sessionMoneyCell — so asserting the absence matters as much as the presence.
	saved := sessionMoneyCell(366_100, true, sessionsMoneyWidth)
	if !strings.HasSuffix(saved, partialMarker) {
		t.Errorf("clamped saving = %q, want the %q suffix that says the figure is a floor",
			saved, partialMarker)
	}
	if strings.HasPrefix(saved, inexactMarker) {
		t.Errorf("clamped saving = %q still carries %q on the value; that caveat is "+
			"unconditional for this column and belongs on the heading", saved, inexactMarker)
	}
}

// hasColumn reports whether a header set carries the named column. Local to the tests: the
// production side no longer asks that question — rebuildSessionsTable derives the money flag
// from the width and sets the header itself — so a helper for it would be dead code.
//
// By headerTitle rather than the raw Title, so it finds a column whose heading carries
// headerMarker: against the raw string, "SAVED" matches nothing once the column is named
// "SAVED ~", and every caller here would report the money columns as absent.
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
			1,              // sub-floor, renders "<$0.01"
			20,             // likewise
			36_577_700,     // $36.58 — the ordinary case
			936_577_700,    // $936.5777 — where the overflow started
			99_999_990_000, // $99,999.99
			math.MaxInt64,  // the saturated clamp, the longest value there is
		} {
			for _, avoided := range []bool{false, true} {
				for _, saturated := range []bool{false, true} {
					got := sessionMoneyCell(micros, saturated, budget)
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
	// figure keeps its cents, which is now the finest rung the ladder has.
	//
	// ASKED OF THE FORMATTER rather than written out, so this cannot drift from it again. The
	// literal here used to be "$36.5777" and said "the full figure"; when the formatter moved to
	// two decimals that became an assertion about a format nothing produces.
	if want := formatUSDCell(36.5777); want != "$36.58" {
		t.Fatalf("formatUSDCell(36.5777) = %q; this test's premise is that the finest rung is "+
			"cents, so it needs rewriting alongside the formatter", want)
	}
	if got := sessionMoneyCell(36_577_700, false, sessionsMoneyWidth); got != formatUSDCell(36.5777) {
		t.Errorf("cell = %q at the declared width, want %q — the ladder is giving up precision "+
			"it does not need to", got, formatUSDCell(36.5777))
	}
}

// A KNOWN NON-ZERO CHARGE MUST NEVER RENDER AS ZERO.
//
// The precision ladder gives up decimals to fit a narrow column, and its two COARSE rungs round a
// sub-cent figure away entirely: %.0f makes anything under fifty cents into "$0", and the compact
// form truncates. The finest rung cannot, because formatUSDCell floors at "<$0.01" rather than
// printing "$0.00" — so the ladder's honesty now rests on that floor plus the skip-a-zero-rung
// guard, where it used to rest on four decimals being enough for any real charge.
//
// This cell's own rule is never $0.00 for a figure that might be unknown — and a figure that is
// KNOWN and shown as nothing breaks it harder, because "free" is a claim about the traffic.
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
					got := sessionMoneyCell(micros, saturated, budget)
					// A zero-valued amount is the defect; the em dash is the honest fallback.
					// "$0.00" covers the cents rung, "$0"/"$0 " the whole-dollar and compact ones.
					for _, zero := range []string{"$0.00", "$0 ", "$0"} {
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

	// And where there IS room, a sub-cent charge is DISCLOSED rather than dropped to the em dash —
	// the guard must skip dishonest rungs, not every rung.
	//
	// "<$0.01" is the only honest form left for $0.0012. At four decimals the cell could state it
	// exactly ("$0.0012") and this assertion accepted either; at two decimals the floor notation is
	// the whole answer, so the alternative is gone rather than left in as dead reassurance. What is
	// still asserted is the part that matters: the cell says SOMETHING about a real charge, and
	// emptyCell — which means "not known here" — is not an acceptable answer for a charge we know.
	if got := sessionMoneyCell(1200, false, sessionsMoneyWidth); got != "<$0.01" {
		t.Errorf("cell = %q at the declared width, want %q — a known sub-cent charge must be "+
			"disclosed as below the floor, not rendered as unknown", got, "<$0.01")
	}
}

// Every width that keeps the money columns can render an honest figure in them.
//
// THE COLLISION THIS REFUSES: emptyCell means "not known here", and sessionMoneyCell falls
// back to it when no rung fits without rounding a real charge to zero. So a width that keeps
// COST but cannot fit "<$0.01" renders a KNOWN charge as unknown — the same lie as "$0.00"
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
			cell := sessionMoneyCell(subCent, false, budget)
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
// The gap is real and narrow: measured across every width, the only fitted COST budgets below the
// declared 10 are 9 (five widths) and 8 (three) — the band just above the floor where
// sessionsShowMoney drops the columns. So the fixture has to be a charge whose honest cents form
// needs all ten runes, because a cell that fits either budget cannot tell the two apart.
//
// AND THE CENTS CHANGE BROKE THAT, which is why the amount moved. $1234.5678 needed ten runes at
// four decimals and needs eight at two ("$1234.57"), so it fitted every budget: `wide` came back
// identical to `want`, len(wide) > budget was 8 > 8 and 8 > 9, and the regression detector below
// could not fire at either width. A fixture chosen for a format the code no longer produces is a
// test that has quietly stopped testing.
//
// $123456.78 restores it — ten runes at two decimals, and the ladder's next rung down is the
// whole-dollar "$123457" at seven, so the two widths produce visibly different cells.
func TestSessionsMoneyCells_UseTheFittedWidthNotTheDeclaredOne(t *testing.T) {
	const bigCost = 123_456_780_000 // $123456.78 — ten runes at cents, "$123457" when shrunk
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
		if want := sessionMoneyCell(bigCost, false, budget); !strings.Contains(row, want) {
			t.Errorf("width %d: row %q does not carry %q, the cell a %d-rune budget allows",
				termWidth, row, want, budget)
		}
		// And emphatically not the wider form the declared width would have allowed, which
		// bubbles/table would then truncate.
		if wide := sessionMoneyCell(bigCost, false, declared); wide != "" &&
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

// TestSessionMoneyCellMin_CoversCellAndHeading pins sessionMoneyCellMin to BOTH halves of the
// column it bounds, rather than to a digit someone has to remember to change.
//
// IT EXISTS BECAUSE THE DRIFT IS SILENT IN THE EXPENSIVE DIRECTION. The constant was 9, derived
// from "~<$0.0001" when money rendered at four decimals. Moving the formatter to cents left 9
// passing every other test in this file, because a column WIDER than necessary still renders an
// honest figure — nothing looks wrong on screen, the money columns are simply declined on
// terminals that could have afforded them, and no assertion about cell CONTENTS can see that.
//
// BOTH HALVES, because which one binds has now changed twice. The cell needs six runes
// ("<$0.01", formatUSDCell's floor). The heading needs seven ("SAVED ~", since headerMarker
// moved the estimate caveat there and bubbles truncates a heading that will not fit). So the
// answer is seven and the HEADING is what binds — where before the marker moved it was the
// cell. Deriving from the max of the two means neither change can go unnoticed.
func TestSessionMoneyCellMin_CoversCellAndHeading(t *testing.T) {
	// A charge below half the floor, which is what makes formatUSDCell emit its "<$…" form.
	floorForm := formatUSDCell(usdFloor / 4)
	if !strings.HasPrefix(floorForm, "<$") {
		t.Fatalf("formatUSDCell(%v) = %q, want the floor form: this test's premise is that a "+
			"sub-floor charge is disclosed rather than stated", usdFloor/4, floorForm)
	}
	// The widest heading among the money columns, off the real column set rather than spelled out.
	widestHeading := 0
	for _, c := range sessionsColumns() {
		if t := headerTitle(c); t != "COST" && t != "SAVED" {
			continue
		}
		if n := len([]rune(c.Title)); n > widestHeading {
			widestHeading = n
		}
	}
	if widestHeading == 0 {
		t.Fatal("no money column found in sessionsColumns(); this test asserted nothing")
	}
	want := max(len([]rune(floorForm)), widestHeading)
	if sessionMoneyCellMin != want {
		t.Errorf("sessionMoneyCellMin = %d, want %d — the narrowest honest cell is %q (%d runes) "+
			"and the widest money heading is %d runes. A value too LARGE costs money columns on "+
			"narrow terminals without failing any other assertion; too small either renders real "+
			"charges as %q or lets bubbles truncate a heading.",
			sessionMoneyCellMin, want, floorForm, len([]rune(floorForm)), widestHeading, emptyCell)
	}
	// And the constant is actually sufficient for the CELL: at exactly that budget it renders.
	if got := sessionMoneyCell(1200, false, sessionMoneyCellMin); got != floorForm {
		t.Errorf("sessionMoneyCell at the declared minimum = %q, want %q", got, floorForm)
	}
	// The cell's own floor is the narrower of the two, so it keeps working below the constant.
	// Asserted so the comment above cannot quietly become wrong about which half binds.
	if got := sessionMoneyCell(1200, false, len([]rune(floorForm))); got != floorForm {
		t.Errorf("sessionMoneyCell at the cell floor %d = %q, want %q",
			len([]rune(floorForm)), got, floorForm)
	}
	// One column below the CELL floor it cannot render, which is what makes that a floor.
	if got := sessionMoneyCell(1200, false, len([]rune(floorForm))-1); got != emptyCell {
		t.Errorf("sessionMoneyCell one column below the cell floor = %q, want %q", got, emptyCell)
	}
}
