package tui

import (
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
			if got := sessionMoneyCell(tc.micros, tc.avoided); got != tc.want {
				t.Errorf("sessionMoneyCell(%d, %v) = %q, want %q", tc.micros, tc.avoided, got, tc.want)
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
	if !hasSessionsColumn(wide, "COST") || !hasSessionsColumn(wide, "SAVED") {
		t.Errorf("a 200-column terminal dropped the money columns: %v", titles(wide))
	}
	narrow := sessionsColumnsFor(50)
	if hasSessionsColumn(narrow, "COST") || hasSessionsColumn(narrow, "SAVED") {
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

func titles(cols []table.Column) []string {
	out := make([]string, 0, len(cols))
	for _, c := range cols {
		out = append(out, c.Title)
	}
	return out
}
