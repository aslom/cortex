package tui

import "github.com/charmbracelet/bubbles/table"

// setCursorVisible moves a table's cursor to row n and leaves it ON SCREEN.
//
// Use this instead of table.SetCursor everywhere a cursor is placed
// programmatically — following a tail, restoring a saved position, jumping to an
// end. SetCursor alone loses the highlight for any row at or past one screenful.
//
// WHY: bubbles' UpdateViewport renders a WINDOW of rows around the cursor —
// start = clamp(cursor−height, 0, cursor) — and the viewport then shows `height`
// lines of that window beginning at its own YOffset. SetCursor recomputes the
// window but never reconciles YOffset, and only MoveUp/MoveDown do. So with
// YOffset 0 and cursor ≥ height, start lands on cursor−height and the cursor sits
// at window line `height`: exactly one line past the last visible one. The row is
// rendered, the highlight is drawn on it, and it is off the bottom edge.
//
// MoveUp cannot recover from that state either — its three cases are start == 0,
// start < height, and YOffset ≥ 1, and in it none of them match — so YOffset stays
// 0 while start walks up with the cursor. The rows scroll one at a time under an
// arrow key and no row is ever highlighted, which is how this reached us: "the
// lines are getting scrolled up, but the highlight disappears".
//
// The fix is to express the jump as relative movement, the only cursor API in
// bubbles that maintains the offset: GotoTop normalizes it (a MoveUp to row 0),
// then one MoveDown of n walks to the target. n is a distance, not a loop, so this
// is two O(height) re-renders however far the cursor travels.
//
// n is clamped to the row range, as SetCursor's own is. An empty table is left
// alone rather than parked on a row that does not exist.
func setCursorVisible(t *table.Model, n int) {
	rows := len(t.Rows())
	if rows == 0 {
		return
	}
	if n < 0 {
		n = 0
	}
	if n > rows-1 {
		n = rows - 1
	}
	t.GotoTop()
	if n > 0 {
		t.MoveDown(n)
	}
}
