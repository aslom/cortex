package tui

import (
	"strings"
	"testing"

	"github.com/rossoctl/cortex/authbridge/authlib/session"
)

// tokensCellWidth is what sessionsColumns declares for TOKENS.
func tokensCellWidth(t *testing.T) int {
	t.Helper()
	for _, c := range sessionsColumns() {
		if c.Title == "TOKENS" {
			return c.Width
		}
	}
	t.Fatal("no TOKENS column")
	return 0
}

// Every plausible session total has to fit the cell it is rendered into. Grouped digits
// did not: 137,156,234 is eleven characters in a ten-wide column, so the pane showed
// "137,156,2…" — longer than the rounded form, less readable, and truncated at the end
// where the digits that distinguish 137M from 137M live.
func TestSessionTokens_FitsTheColumn(t *testing.T) {
	width := tokensCellWidth(t)
	for _, total := range []int{
		0, 1, 999, 1000, 9_999, 100_000, 999_999,
		1_000_000, 9_999_999,
		30_661_090,  // from a real picker row
		137_156_234, // the one that truncated
		547_853_512, // the largest real one seen
		9_999_999_999,
	} {
		got := sessionTokens(total, nil)
		if n := len([]rune(got)); n > width {
			t.Errorf("total %d renders as %q (%d runes), wider than the %d-column cell",
				total, got, n, width)
		}
		if strings.Contains(got, "…") {
			t.Errorf("total %d renders pre-truncated: %q", total, got)
		}
	}
}

// The compact form still distinguishes the magnitudes an operator is reading the column
// for. A formatter that rendered everything as "1.0M" would fit and say nothing.
func TestSessionTokens_DistinguishesMagnitudes(t *testing.T) {
	for _, tc := range []struct {
		total int
		want  string
	}{
		{0, "—"},
		{842, "842"},
		{4_385_706, "4.4M"},
		{30_661_090, "30.7M"},
		{137_156_234, "137.2M"},
		{547_853_512, "547.9M"},
	} {
		if got := sessionTokens(tc.total, nil); got != tc.want {
			t.Errorf("sessionTokens(%d) = %q, want %q", tc.total, got, tc.want)
		}
	}
}

// And the rendered row agrees with the cell, so a future width change to the column or a
// change in how rows are built cannot reintroduce the truncation.
func TestSessionsPane_TokensCellIsNotTruncatedInTheRow(t *testing.T) {
	m := &model{pane: paneSessions, width: 200}
	m.sessionsTbl = newSessionsTable()
	m.sessionsTbl.SetHeight(6)
	m.sessions = []session.SessionSummary{{ID: "s1", EventCount: 5289, TotalTokens: 547_853_512}}
	m.rebuildSessionsTable()

	rows := m.sessionsTbl.Rows()
	if len(rows) != 1 {
		t.Fatalf("built %d rows, want 1", len(rows))
	}
	cell := strings.TrimSpace(rows[0][3])
	if cell != "547.9M" {
		t.Errorf("TOKENS cell = %q, want %q", cell, "547.9M")
	}
	if strings.Contains(m.sessionsTbl.View(), "…") {
		t.Errorf("the rendered pane truncates a cell:\n%s", m.sessionsTbl.View())
	}
}
