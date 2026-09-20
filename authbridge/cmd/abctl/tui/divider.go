package tui

import "strings"

// dividerLines is the rule's height, and the number layout() must reserve for it.
//
// A named constant for a literal 1, for the reason spendBandLines and spendDrawerLines are:
// paneView draws it and layout() reserves it from the terminal height, in two files, and the
// two going out of step is not a cosmetic bug. Under-reserve and the view is a line taller than
// the terminal, which scrolls it and takes the footer off the bottom; over-reserve and the body
// leaves a row nobody fills. The comments on both of those constants record the same failure
// happening one row and five rows at a time.
const dividerLines = 1

// renderDivider is the rule that closes the top block, immediately above the body.
//
// ALWAYS DRAWN, on every pane, whether or not the spend band or its drawer had anything to say.
// That is what makes it reservable unconditionally in layout() — the band's reservation has to
// ask spendStripReservesRow() and carries a comment about the staleness that a pane-dependent
// reservation would cause. This one cannot go stale because it never varies.
//
// FULL TERMINAL WIDTH, not the width of the content above it. A rule as wide as the band's
// figures would move whenever a figure changed width — $9.99 to $10.01 shortens it — and a
// divider whose length is data-dependent reads as a rendering bug rather than as structure.
//
// Box-drawing rather than '-' or a blank line: the pipeline pane already separates its inbound
// and outbound halves with "── (app) ──", so the glyph is this TUI's existing vocabulary for
// "a boundary between two things".
func renderDivider(width int) string {
	if width <= 0 {
		return ""
	}
	return strings.Repeat("─", width)
}
