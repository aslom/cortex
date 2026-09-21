package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/rossoctl/cortex/authbridge/authlib/usage"
)

// esc is the ASCII escape character, built rather than written as a literal so this file carries
// no control byte of its own.
const esc = "\x1b"

// A SERVER-SUPPLIED WINDOW LABEL REACHES THE TERMINAL, so it must not be able to carry escape
// sequences or newlines. CWE-150 — improper neutralization of escape sequences — in a TUI: a
// proxy answering {"window":"<ESC>[2J1h"} could clear the screen, and one answering a newline
// could add a line to a two-row reservation and push the footer off the bottom of the terminal.
//
// THESE TESTS EXISTED AND WERE DELETED AS COLLATERAL.
// TestSpendSummary_SanitisesTheServersWindowLabel and its escape-sequence sibling went out in a
// sweep aimed at TestRenderSpendStrip_*, because they were named TestSpendSummary_*. The
// PROPERTY outlived renderSpendStrip: sanitizeLabel is live on the drawer's caption, on the
// band's per-span served-window comparison, and on the drawer's error text. Nothing else in the
// package feeds a control character into a spend label, so deleting any of those calls breaks no
// other test — which is what made the loss invisible.
//
// Asserted THROUGH THE RENDERED SURFACES rather than on sanitizeLabel directly. The helper is
// covered by construction; what regressed is the CALL, and a test on the helper alone would keep
// passing if a call site dropped it.
func TestSpendLabels_NeutraliseAServerSuppliedControlCharacter(t *testing.T) {
	for _, tc := range []struct{ name, window string }{
		{"escape sequence", esc + "[2J1h"},
		{"newline", "1h\nEXTRA"},
		{"carriage return", "1h\rEXTRA"},
		{"DEL", "1h" + string(rune(0x7f))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The drawer's caption prints the window the SERVER said it served.
			m := &model{width: 120}
			m.spend.drawer.snap = drawerSnap()
			m.spend.drawer.snap.Window = tc.window
			axis, window := m.drawerLabels()
			assertNoControlChars(t, "drawer caption", window)

			// And the whole rendered drawer, so a second path cannot reintroduce it.
			for _, line := range renderSpendDrawer(m.spend.drawer.snap, nil, axis, window, 120) {
				assertNoControlChars(t, "drawer line", line)
			}

			// THE BAND NEVER PRINTS THE SERVER'S LABEL, so its side of this is a different
			// assertion — and the previous version of this claimed otherwise ("a control
			// character travels into that comparison and onto the cell") and checked the band
			// lines for control characters, which no band cell can contain whatever the server
			// sends: bandSpanCell builds its label from spendSpanDefs and its value from
			// moneyAmount, and snap.Window reaches neither.
			//
			// What the band DOES with a tampered window is refuse the span: the served label
			// cannot match the requested one, so the reading is Unanswerable and the cell is an
			// em dash. That is the assertion worth making, because the alternative — drawing the
			// figure under the requested label — publishes a number for a window nobody served.
			b := &model{}
			b.spend.chains[spanHour].snap = &usage.Snapshot{
				Window: tc.window,
				Totals: usage.Counts{Requests: 1, CostMicros: 1_000_000, PricedRequests: 1, PriceableRequests: 1},
				Priced: true,
			}
			lines := renderSpendBand(spendSummary{Spans: b.spanReadings()}, 200)
			for _, line := range lines {
				assertNoControlChars(t, "band line", line)
			}
			if !strings.Contains(lines[1], emptyCell) {
				t.Errorf("a tampered served window drew a cell instead of %q:\n%s",
					emptyCell, strings.Join(lines, "\n"))
			}
			if strings.Contains(lines[1], "$1.00") {
				t.Errorf("the band drew the figure under the requested label for a window the "+
					"server did not serve:\n%s", strings.Join(lines, "\n"))
			}
		})
	}
}

// A failed drawer fetch prints the error, and a transport error can carry the server's own bytes
// — the same exposure one layer over.
func TestRenderSpendDrawer_NeutralisesControlCharsInItsError(t *testing.T) {
	err := errors.New("unexpected status 500: " + esc + "[2Jgone\nsecond line")
	for _, line := range renderSpendDrawer(nil, err, usage.GroupModel, "MONTH", 120) {
		assertNoControlChars(t, "drawer error line", line)
	}
}

// assertNoControlChars fails on any C0 control character or DEL — exactly the set sanitizeLabel
// replaces with U+FFFD. One display column per replacement, so the width arithmetic every one of
// these surfaces depends on still holds.
func assertNoControlChars(t *testing.T, where, s string) {
	t.Helper()
	for i, r := range s {
		if r == 0x7f || r < 0x20 {
			t.Errorf("%s: control character %#U at byte %d in %q — a server-supplied label "+
				"reached the terminal unsanitised", where, r, i, s)
			return
		}
	}
}
