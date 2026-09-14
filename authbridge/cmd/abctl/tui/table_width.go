package tui

import "github.com/charmbracelet/bubbles/table"

// minColumnWidth is the floor a column will not shrink below: enough for a character and the
// truncation ellipsis bubbles appends, so a squeezed column still says something.
const minColumnWidth = 4

// tableWidth is the width a table with these columns actually renders at: columnsWidth for
// []table.Column instead of []eventColumn, charging the same cellPadding per column.
//
// Zero-width columns are skipped because renderRow skips them, so counting their padding would
// overstate the total and shrink the others for nothing.
func tableWidth(cols []table.Column) int {
	w := 0
	for _, c := range cols {
		if c.Width <= 0 {
			continue
		}
		w += c.Width + cellPadding
	}
	return w
}

// fitTableColumns returns cols narrowed to fit a terminal termWidth columns wide.
//
// This is issue #866's failure mode in the tables that fitColumns does not cover. That fix
// taught the EVENTS table to fit itself; the sessions, pipeline and catalog tables kept the
// fixed widths their constructors declare — the sessions table's comment even promised "widths
// are refined later by layout() based on terminal width", which layout() never did. So the
// sessions pane, the first screen an operator sees, rendered 90 columns wide on every terminal
// including the 80-column default: every row wrapped onto a second screen line, which
// scrambled the columns and broke the height budget at the same time, because the budget
// counts rows while the terminal counts lines.
//
// Shrinks the widest column first, one column at a time, so the space comes out of whichever
// field has the most to give (the session ID, the plugin name) instead of being spread evenly
// across fields that are already tight. Stops when nothing can give without crossing
// minColumnWidth: below that the terminal is simply too narrow, and a table that lies about
// its content is worse than one that overflows visibly.
//
// A copy, never the caller's slice: these come from package-level constructors and mutating
// them in place would make the fit permanent and cumulative across resizes.
func fitTableColumns(cols []table.Column, termWidth int) []table.Column {
	out := make([]table.Column, len(cols))
	copy(out, cols)
	if termWidth <= 0 {
		return out
	}
	for tableWidth(out) > termWidth {
		widest, at := minColumnWidth, -1
		for i, c := range out {
			if c.Width > widest {
				widest, at = c.Width, i
			}
		}
		if at < 0 {
			break
		}
		out[at].Width--
	}
	return out
}
