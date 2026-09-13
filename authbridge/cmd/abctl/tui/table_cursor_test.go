package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/rossoctl/cortex/authbridge/authlib/pipeline"
)

// hostToken is the per-row marker these fixtures use. Unique per row and short
// enough that the 20-wide HOST column does not truncate it, so its presence in the
// rendered view is a reliable "this row is on screen" — which is the property under
// test. Asserting on the cursor INDEX alone cannot catch this bug: the index was
// always right, it was the rendered window that excluded it.
func hostToken(i int) string { return fmt.Sprintf("h%02d.example", i) }

// cursorRowsFixture builds n request events, each identifiable by its host.
func cursorRowsFixture(n int) []pipeline.SessionEvent {
	events := make([]pipeline.SessionEvent, n)
	for i := range events {
		events[i] = pipeline.SessionEvent{
			Direction: pipeline.Outbound, Phase: pipeline.SessionRequest,
			Host:      hostToken(i),
			Inference: &pipeline.InferenceExtension{Model: "m"},
		}
	}
	return events
}

func cursorModel(t *testing.T, n int) *model {
	t.Helper()
	m := &model{
		pane: paneEvents, selectedSess: "s", bodyHeight: 12, width: 200,
		events: map[string][]pipeline.SessionEvent{"s": cursorRowsFixture(n)},
	}
	m.eventsTbl = newEventsTable()
	m.rebuildEventsTable()
	if got := len(m.eventsTbl.Rows()); got != n {
		t.Fatalf("fixture built %d rows, want %d", got, n)
	}
	if h := m.eventsTbl.Height(); h >= n {
		t.Fatalf("fixture height %d must be smaller than %d rows to exercise scrolling", h, n)
	}
	return m
}

// assertSelectionVisible fails when the highlighted row is outside the window the
// table actually renders. The highlight is drawn on the cursor row wherever it is,
// so a cursor outside the rendered window means a table with no visible highlight
// at all — which is what an operator sees.
func assertSelectionVisible(t *testing.T, tbl table.Model, label string) {
	t.Helper()
	cur := tbl.Cursor()
	if cur < 0 {
		t.Errorf("%s: no row selected (cursor=%d)", label, cur)
		return
	}
	view := tbl.View()
	want := hostToken(cur)
	if strings.Contains(view, want) {
		return
	}
	var onScreen []int
	for i := 0; i < len(tbl.Rows()); i++ {
		if strings.Contains(view, hostToken(i)) {
			onScreen = append(onScreen, i)
		}
	}
	win := "nothing"
	if len(onScreen) > 0 {
		win = fmt.Sprintf("rows %d..%d", onScreen[0], onScreen[len(onScreen)-1])
	}
	t.Errorf("%s: selected row %d is off screen; the view shows %s", label, cur, win)
}

// TestSetCursorVisible_LandsOnScreen is the unit-level guard.
//
// table.SetCursor re-windows the rendered rows around the new cursor
// (start = cursor − height) but never reconciles the viewport's YOffset, so for any
// target at or past one screenful the cursor lands exactly one line below the last
// visible row. setCursorVisible must put the cursor where it was asked AND leave it
// on screen, for targets on both sides of that boundary.
func TestSetCursorVisible_LandsOnScreen(t *testing.T) {
	const n = 40
	for _, target := range []int{0, 1, 5, 10, 11, 12, 20, 33, 38, 39} {
		m := cursorModel(t, n)
		setCursorVisible(&m.eventsTbl, target)
		if got := m.eventsTbl.Cursor(); got != target {
			t.Errorf("target %d: cursor=%d", target, got)
		}
		assertSelectionVisible(t, m.eventsTbl, fmt.Sprintf("target %d", target))
	}
}

// Out-of-range targets clamp to the row range rather than leaving the table with a
// cursor pointing at nothing — SetCursor's own clamping behaviour, preserved.
func TestSetCursorVisible_ClampsAndSurvivesEmpty(t *testing.T) {
	m := cursorModel(t, 40)
	setCursorVisible(&m.eventsTbl, 999)
	if got := m.eventsTbl.Cursor(); got != 39 {
		t.Errorf("cursor past the end = %d, want 39", got)
	}
	assertSelectionVisible(t, m.eventsTbl, "target past the end")

	setCursorVisible(&m.eventsTbl, -5)
	if got := m.eventsTbl.Cursor(); got != 0 {
		t.Errorf("negative target = %d, want 0", got)
	}
	assertSelectionVisible(t, m.eventsTbl, "negative target")

	// An empty table must not panic or park the cursor on a row that is not there.
	empty := newEventsTable()
	setCursorVisible(&empty, 3)
	if got := empty.Cursor(); got > 0 {
		t.Errorf("empty table cursor = %d, want <= 0", got)
	}
}

// TestEventsTable_AutoFollowKeepsSelectionVisible is the reported bug.
//
// The pane rebuilds on every incoming event, and the rebuild restores the cursor —
// to the last row while following the tail. That restore used to lose the highlight,
// so on a live session the highlight vanished on the next event even when the
// operator had scrolled to the bottom with the arrow keys. Pressing up then scrolled
// the rows one at a time with no highlight anywhere, because the cursor stayed
// exactly one row below the rendered window.
func TestEventsTable_AutoFollowKeepsSelectionVisible(t *testing.T) {
	m := cursorModel(t, 40)

	// The first rebuild already follows the tail.
	assertSelectionVisible(t, m.eventsTbl, "after initial rebuild")
	if got, want := m.eventsTbl.Cursor(), 39; got != want {
		t.Fatalf("auto-follow cursor = %d, want %d", got, want)
	}

	// A rebuild while following the tail keeps the highlight on screen.
	m.rebuildEventsTable()
	assertSelectionVisible(t, m.eventsTbl, "rebuild while following")

	// Arrow up, then more rebuilds: the highlight stays visible and the cursor
	// keeps walking up one row at a time.
	for i := 1; i <= 6; i++ {
		m.eventsTbl, _ = m.eventsTbl.Update(tea.KeyMsg{Type: tea.KeyUp})
		assertSelectionVisible(t, m.eventsTbl, fmt.Sprintf("up #%d", i))
		if got, want := m.eventsTbl.Cursor(), 39-i; got != want {
			t.Fatalf("up #%d: cursor = %d, want %d", i, got, want)
		}
		m.rebuildEventsTable()
		assertSelectionVisible(t, m.eventsTbl, fmt.Sprintf("up #%d + rebuild", i))
		if got, want := m.eventsTbl.Cursor(), 39-i; got != want {
			t.Fatalf("up #%d + rebuild: cursor = %d, want %d", i, got, want)
		}
	}
}

// A new event arriving while the operator reads mid-list must not move the
// highlight, and must not lose it either — the other branch of the rebuild.
func TestEventsTable_RebuildMidListKeepsSelectionVisible(t *testing.T) {
	m := cursorModel(t, 40)
	setCursorVisible(&m.eventsTbl, 20)
	assertSelectionVisible(t, m.eventsTbl, "parked mid-list")

	m.events["s"] = append(m.events["s"], cursorRowsFixture(41)[40])
	m.rebuildEventsTable()
	if got := m.eventsTbl.Cursor(); got != 20 {
		t.Errorf("cursor moved on a new event: %d, want 20", got)
	}
	assertSelectionVisible(t, m.eventsTbl, "new event while parked mid-list")
}

// TestGoBottom_KeepsSelectionVisible covers the G/end key, which jumps straight to
// the last row — the fastest way to reproduce this by hand.
func TestGoBottom_KeepsSelectionVisible(t *testing.T) {
	m := cursorModel(t, 40)
	setCursorVisible(&m.eventsTbl, 0)

	m.goBottom()
	if got, want := m.eventsTbl.Cursor(), 39; got != want {
		t.Fatalf("goBottom cursor = %d, want %d", got, want)
	}
	assertSelectionVisible(t, m.eventsTbl, "goBottom")

	for i := 1; i <= 3; i++ {
		m.eventsTbl, _ = m.eventsTbl.Update(tea.KeyMsg{Type: tea.KeyUp})
		assertSelectionVisible(t, m.eventsTbl, fmt.Sprintf("goBottom then up #%d", i))
	}

	m.goTop()
	if got := m.eventsTbl.Cursor(); got != 0 {
		t.Fatalf("goTop cursor = %d, want 0", got)
	}
	assertSelectionVisible(t, m.eventsTbl, "goTop")
}

// A terminal resize re-heights the table mid-session. SetHeight re-windows the rows
// the same way SetCursor does, so the highlight must survive it.
func TestEventsTable_ResizeKeepsSelectionVisible(t *testing.T) {
	m := cursorModel(t, 40)
	setCursorVisible(&m.eventsTbl, 30)
	for _, h := range []int{40, 20, 12, 8, 30} {
		m.bodyHeight = h
		m.rebuildEventsTable()
		assertSelectionVisible(t, m.eventsTbl, fmt.Sprintf("bodyHeight %d", h))
	}
}
