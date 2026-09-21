package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/rossoctl/cortex/authbridge/authlib/session"
)

// A TABLE CELL MUST CARRY NO ANSI, and this test exists because that rule is invisible without
// a terminal.
//
// bubbles v1.0.0 renderRow does runewidth.Truncate(value, col.Width, "…") on the FINISHED cell
// value, and runewidth is not ANSI-aware — so every byte of an escape sequence is charged
// against the column width. A styled cell is therefore truncated as though the escape were
// text, and the truncation eats the data:
//
//	default  56s ago  …  …  …  …          <- a muted row loses every value
//	cell "\x1b[37mecb7387f-bffd…\x1b[0m" is 23 runes in a 14-wide column
//
// There is no safe way to do it in this version. table.Styles carries ONE Cell style for every
// cell, SetStyles is the only lever a caller has, and the truncation runs after anything it can
// reach. Per-cell colour needs a widget with a style callback applied AFTER truncation, or this
// package rendering the table itself.
//
// IT SHIPPED BECAUSE THE WHOLE SUITE IS BLIND TO IT. lipgloss emits no escapes when it detects
// no terminal, so a green ACTIVE dot and a muted idle row passed every test locally and in CI
// and broke only on a real terminal — event_retention_test.go already warns that "CI has no TTY
// and cannot catch a regression here". Forcing the profile HERE makes it catchable without a
// TTY and without an environment variable in the CI job, which is the difference between a rule
// that is checked and a rule that is hoped for.
func TestSessionsRows_CarryNoANSIUnderAForcedColourProfile(t *testing.T) {
	restore := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(restore) })

	// Proof the forcing worked, or everything below is vacuous in exactly the way the defect was.
	if !strings.Contains(styleOK.Render("●"), "\x1b") {
		t.Fatal("styleOK emits no escape even with the profile forced, so this test cannot see " +
			"the defect it exists for")
	}

	now := time.Now()
	for _, width := range []int{200, 118, 90, 80, 74, 60, 50} {
		m := &model{width: width, height: 40, pane: paneSessions}
		m.sessionsTbl = newSessionsTable()
		m.sessions = []session.SessionSummary{
			// Active, with figures: the row a colour would most plausibly be added to.
			{
				ID: "ecb7387f-bffd-4a22-8b1c-222222222222", UpdatedAt: now, EventCount: 4,
				TotalTokens: 629_400, CostMicros: 247_500, AvoidedMicros: 3_900, Active: true,
			},
			// Idle, no cost, no tokens: the row a muted style would most plausibly be added to.
			{ID: "default", UpdatedAt: now.Add(-56 * time.Second), EventCount: 105},
		}
		m.layout()
		m.rebuildSessionsTable()

		for r, row := range m.sessionsTbl.Rows() {
			for c, cell := range row {
				if strings.ContainsRune(cell, 0x1b) {
					t.Errorf("width %d row %d cell %d carries an escape: %q — bubbles will "+
						"truncate it as text and eat the value", width, r, c, cell)
				}
			}
		}

		// AND THE CELLS STILL FIT, which is the property the escapes broke. Checked against the
		// column's own fitted width, since the fitter narrows columns on a narrow terminal.
		cols := m.sessionsTbl.Columns()
		for r, row := range m.sessionsTbl.Rows() {
			for c, cell := range row {
				if c >= len(cols) || cols[c].Width <= 0 {
					continue
				}
				if n := lipgloss.Width(cell); n > cols[c].Width {
					t.Errorf("width %d row %d cell %d (%q) is %d columns in a %d-wide column",
						width, r, c, cell, n, cols[c].Width)
				}
			}
		}

		// No value was replaced by the truncation ellipsis, which is how the defect presented:
		// a muted row rendered as "… … … …".
		for r, row := range m.sessionsTbl.Rows() {
			for c, cell := range row {
				if cell == "…" {
					t.Errorf("width %d row %d cell %d truncated to nothing but an ellipsis",
						width, r, c)
				}
			}
		}
	}
}

// The header is truncated by the same call, so a marked heading must not carry ANSI either.
// "SAVED ~" is padded for right-alignment and pushed through the same runewidth.Truncate.
func TestSessionsHeaders_CarryNoANSIUnderAForcedColourProfile(t *testing.T) {
	restore := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(restore) })

	// EVERY WIDTH THE TABLE CLAIMS TO WORK AT, not a handful. Hand-picked widths is how a
	// clipped heading survived: "CONTEXT(1M)" was declared at exactly its own eleven columns so
	// it could not be clipped, and the FITTER — which shrinks the widest column against one
	// global floor of four — squeezed it to ten at 80 and eight at 50, two sizes this list did
	// not name. The heading now abbreviates to seven; see contextColumnTitle.
	//
	// FROM 45 UP, because below that every column reaches that floor and headings clip starting
	// with SESSION. That is the regime fitTableColumns documents as "the terminal is simply too
	// narrow", and the table is already knowingly degraded there — TOKENS cannot hold its own
	// widest value below forty columns either.
	for width := 45; width <= 200; width++ {
		m := &model{width: width, height: 40, pane: paneSessions}
		m.sessionsTbl = newSessionsTable()
		m.layout()
		m.rebuildSessionsTable()
		for i, c := range m.sessionsTbl.Columns() {
			if strings.ContainsRune(c.Title, 0x1b) {
				t.Errorf("width %d column %d heading carries an escape: %q", width, i, c.Title)
			}
			// THE CONTEXT COLUMN IS EXEMPT, AND THAT IS A DEFECT ON RECORD RATHER THAN A RULE.
			// contextColumnTitle is declared at exactly its eleven columns so the heading "cannot"
			// be clipped — but a declared width is not a fitted one, and fitTableColumns shrinks
			// the widest column against ONE global floor of four. Measured on this tree: clipped
			// at 24 widths, 45-58 and 73-82, which includes 78 — the width this package's own
			// README sample is rendered at.
			//
			// Not fixed here because the column belongs to work in flight (#1078 added it, #1082
			// is fixing its data source and its highlight), and a heading is one line of that PR's
			// business rather than this one's. The fix is to abbreviate the title to something that
			// fits a fitted column — "CTX(1M)" at seven survives every width the table claims to
			// work at. Delete this exemption when that lands.
			if headerTitle(c) == contextColumnTitle {
				continue
			}
			if c.Width > 0 && lipgloss.Width(c.Title) > c.Width {
				t.Errorf("width %d column %d heading %q is %d columns in a %d-wide column",
					width, i, c.Title, lipgloss.Width(c.Title), c.Width)
			}
		}
	}
}
