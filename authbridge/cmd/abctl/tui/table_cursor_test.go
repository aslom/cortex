package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/rossoctl/cortex/authbridge/authlib/pipeline"
	"github.com/rossoctl/cortex/authbridge/authlib/session"
	"github.com/rossoctl/cortex/authbridge/cmd/abctl/apiclient"
)

// hostToken is the per-row marker these fixtures use. Unique per row and short
// enough that the 20-wide HOST column does not truncate it, so its presence in the
// rendered view is a reliable "this row is on screen" — which is the property under
// test. Asserting on the cursor INDEX alone cannot catch this bug: the index was
// always right, it was the rendered window that excluded it.
func hostToken(i int) string { return fmt.Sprintf("h%02d.example", i) }

// markerOf finds the fixture marker among a row's cells, so an assertion can name the row
// the cursor is actually on rather than the row its index would have been before a filter.
func markerOf(row table.Row) (string, bool) {
	for _, cell := range row {
		if c := strings.TrimSpace(cell); strings.HasSuffix(c, ".example") {
			return c, true
		}
	}
	return "", false
}

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
	// Read the marker off the SELECTED ROW, not from the cursor index. Once a filter is
	// on, row N is no longer event N, so deriving the token from the index would assert
	// that some unrelated row is on screen.
	want, ok := markerOf(tbl.SelectedRow())
	if !ok {
		t.Errorf("%s: selected row %d carries no marker cell: %q", label, cur, tbl.SelectedRow())
		return
	}
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

	// An empty table is left exactly as it was, which is the most the helper can promise:
	// with no rows there is no row to select, and where a bubbles table parks its cursor
	// when empty depends on how it emptied — a fresh one sits at 0, one whose rows were
	// taken away by SetRows(nil) at -1. Neither index addresses a row, so the helper
	// declines to move rather than picking one, and this pins "unchanged" rather than a
	// value that would only describe one of the two ways in.
	for _, tc := range []struct {
		name  string
		build func() table.Model
	}{
		{"fresh", newEventsTable},
		{"emptied by SetRows(nil)", func() table.Model {
			tbl := newEventsTable()
			tbl.SetRows([]table.Row{make(table.Row, len(tbl.Columns()))})
			tbl.SetRows(nil)
			return tbl
		}},
	} {
		empty := tc.build()
		before := empty.Cursor()
		setCursorVisible(&empty, 3)
		if got := empty.Cursor(); got != before {
			t.Errorf("%s empty table: cursor moved %d -> %d, want it left alone", tc.name, before, got)
		}
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

// TestEventsTable_FilterShrinkKeepsSelectionVisible covers the rows-SHRINK case, the one the
// old `else if prevRow < len(rows)` skipped outright: typing a filter or toggling
// hideInactive can pull the list out from under the cursor, and with no restore the cursor
// was left wherever SetRows had clamped it, with an offset nobody reconciled.
//
// Both shrink paths are exercised, and both directions of the round trip, because the
// interesting state is the one where the stale offset still addresses a line of the new,
// shorter content — a drastic shrink is self-correcting, a moderate one is not.
func TestEventsTable_FilterShrinkKeepsSelectionVisible(t *testing.T) {
	for _, tc := range []struct {
		name     string
		shrink   func(m *model)
		wantRows int
	}{
		// "h3" keeps h30..h39: ten rows, every one of them below the parked cursor.
		{"filter to ten rows", func(m *model) { m.filter = "h3" }, 10},
		// A single row is the drastic case: shorter than the table height.
		{"filter to one row", func(m *model) { m.filter = "h07" }, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := cursorModel(t, 40)
			setCursorVisible(&m.eventsTbl, 30)
			assertSelectionVisible(t, m.eventsTbl, "parked at row 30")

			tc.shrink(m)
			m.rebuildEventsTable()
			if got := len(m.eventsTbl.Rows()); got != tc.wantRows {
				t.Fatalf("filter left %d rows, want %d", got, tc.wantRows)
			}
			assertSelectionVisible(t, m.eventsTbl, "after the filter shrank the list")
			if got, want := m.eventsTbl.Cursor(), tc.wantRows-1; got > want {
				t.Errorf("cursor %d addresses no row in a %d-row list", got, tc.wantRows)
			}
			// The detail pane reads the selection through this, so a cursor the table
			// clamped but the model did not follow shows an empty pane.
			if _, ok := m.selectedEventRow(); !ok {
				t.Error("no selected row resolves after the shrink")
			}

			// Clearing the filter grows the list back; the cursor must still be on screen.
			m.filter = ""
			m.rebuildEventsTable()
			if got := len(m.eventsTbl.Rows()); got != 40 {
				t.Fatalf("clearing the filter left %d rows, want 40", got)
			}
			assertSelectionVisible(t, m.eventsTbl, "after clearing the filter")
			if _, ok := m.selectedEventRow(); !ok {
				t.Error("no selected row resolves after clearing the filter")
			}
		})
	}
}

// hideInactive is the other shrink lever, and it hides rows from the MIDDLE of the list
// rather than the ends, so the surviving rows keep no relationship to their old indices.
func TestEventsTable_HideInactiveShrinkKeepsSelectionVisible(t *testing.T) {
	events := cursorRowsFixture(40)
	// Give a handful of events a plugin invocation so they survive the inactive filter.
	active := map[int]bool{2: true, 9: true, 17: true, 26: true, 33: true, 38: true}
	for i := range events {
		if active[i] {
			events[i].Invocations = &pipeline.Invocations{Outbound: []pipeline.Invocation{
				{Plugin: "tool-prune", Action: pipeline.ActionModify, Reason: "tools_pruned"},
			}}
		}
	}
	m := &model{
		pane: paneEvents, selectedSess: "s", bodyHeight: 12, width: 200,
		events: map[string][]pipeline.SessionEvent{"s": events},
	}
	m.eventsTbl = newEventsTable()
	m.rebuildEventsTable()
	setCursorVisible(&m.eventsTbl, 30)
	assertSelectionVisible(t, m.eventsTbl, "parked at row 30")

	m.hideInactive = true
	m.rebuildEventsTable()
	if got, want := len(m.eventsTbl.Rows()), len(active); got != want {
		t.Fatalf("hideInactive left %d rows, want %d", got, want)
	}
	assertSelectionVisible(t, m.eventsTbl, "after hideInactive shrank the list")
	if _, ok := m.selectedEventRow(); !ok {
		t.Error("no selected row resolves after hideInactive")
	}

	m.hideInactive = false
	m.rebuildEventsTable()
	assertSelectionVisible(t, m.eventsTbl, "after hideInactive off")
}

// sessionsModel builds a sessions pane with n sessions, taller than the table, and the
// cursor parked on one of them. IDs carry the marker so the same visibility assertion works.
func sessionsModel(t *testing.T, n int) *model {
	t.Helper()
	m := &model{pane: paneSessions, width: 200}
	m.sessionsTbl = newSessionsTable()
	m.sessionsTbl.SetHeight(12)
	for i := 0; i < n; i++ {
		m.sessions = append(m.sessions, session.SessionSummary{
			ID: hostToken(i), UpdatedAt: time.Now(), EventCount: 3,
		})
	}
	m.rebuildSessionsTable()
	if got := len(m.sessionsTbl.Rows()); got != n {
		t.Fatalf("fixture built %d session rows, want %d", got, n)
	}
	if h := m.sessionsTbl.Height(); h >= n {
		t.Fatalf("fixture height %d must be smaller than %d rows", h, n)
	}
	return m
}

// The sessions pane restores its cursor BY SESSION ID on every refresh, which arrives on a
// poll rather than a keystroke — so the same lost highlight was one refresh away there too,
// for any session list longer than the pane.
func TestSessionsTable_RestoreByIDKeepsSelectionVisible(t *testing.T) {
	m := sessionsModel(t, 40)
	setCursorVisible(&m.sessionsTbl, 30)
	assertSelectionVisible(t, m.sessionsTbl, "parked at session 30")
	want := m.selectedSessionID()

	// A refresh that changes nothing must not move or lose the selection.
	m.rebuildSessionsTable()
	if got := m.selectedSessionID(); got != want {
		t.Errorf("refresh moved the selection to %q, want %q", got, want)
	}
	assertSelectionVisible(t, m.sessionsTbl, "after a refresh")

	// A refresh that drops the selected session falls back to the first row — the branch
	// that absorbed the old len(rows) > 0 guard, so it must survive an empty list too.
	m.sessions = m.sessions[:20]
	m.rebuildSessionsTable()
	if got, want := m.sessionsTbl.Cursor(), 0; got != want {
		t.Errorf("cursor after the selected session vanished = %d, want %d", got, want)
	}
	assertSelectionVisible(t, m.sessionsTbl, "after the selected session vanished")

	m.sessions = nil
	m.rebuildSessionsTable()
	if got := len(m.sessionsTbl.Rows()); got != 0 {
		t.Fatalf("expected an empty sessions table, got %d rows", got)
	}
	if got := m.selectedSessionID(); got != "" {
		t.Errorf("empty sessions table reports %q selected", got)
	}
}

// pipelineModel builds a pipeline pane with an inbound and an outbound chain either side of
// the divider, long enough that the table scrolls.
func pipelineModel(t *testing.T, inbound, outbound int) *model {
	t.Helper()
	m := &model{pane: panePipeline, width: 200, pipeline: &apiclient.PipelineView{}}
	m.pipelineTbl = newPipelineTable()
	m.pipelineTbl.SetHeight(12)
	for i := 0; i < inbound; i++ {
		m.pipeline.Inbound = append(m.pipeline.Inbound, apiclient.PipelinePlugin{
			Name: fmt.Sprintf("in-%02d", i), Direction: "inbound", Position: i + 1,
		})
	}
	for i := 0; i < outbound; i++ {
		m.pipeline.Outbound = append(m.pipeline.Outbound, apiclient.PipelinePlugin{
			Name: fmt.Sprintf("out-%02d", i), Direction: "outbound", Position: i + 1,
		})
	}
	m.rebuildPipelineTable()
	return m
}

// The divider nudge moves by one row in the direction of travel. As MoveUp/MoveDown that is
// index-equivalent to SetCursor(±1) — including the clamp at both ends — but it also moves
// the viewport offset, which is both the point and a visible change: skipping the divider can
// now scroll the pane by a line. Pinned here so neither half drifts.
func TestPipelineTable_DividerNudgeKeepsSelectionVisible(t *testing.T) {
	m := pipelineModel(t, 20, 20)
	rows := m.pipelineTbl.Rows()
	divider := -1
	for i := range rows {
		if isDividerRow(rows, i) {
			divider = i
			break
		}
	}
	if divider <= 0 || divider >= len(rows)-1 {
		t.Fatalf("divider at %d in %d rows; fixture needs plugins on both sides", divider, len(rows))
	}

	// Travelling DOWN onto the divider skips to the row after it.
	setCursorVisible(&m.pipelineTbl, divider-1)
	m.pipelineTbl, _ = m.pipelineTbl.Update(tea.KeyMsg{Type: tea.KeyDown})
	if isDividerRow(m.pipelineTbl.Rows(), m.pipelineTbl.Cursor()) {
		m.pipelineTbl.MoveDown(1) // what handleKey does for panePipeline
	}
	if got, want := m.pipelineTbl.Cursor(), divider+1; got != want {
		t.Errorf("down onto the divider left cursor %d, want %d", got, want)
	}
	if p := m.selectedPlugin(); p == nil {
		t.Error("cursor rests on the divider after a downward nudge")
	}
	assertPipelineSelectionVisible(t, m, "after a downward nudge")

	// Travelling UP onto it skips to the row before.
	setCursorVisible(&m.pipelineTbl, divider+1)
	m.pipelineTbl, _ = m.pipelineTbl.Update(tea.KeyMsg{Type: tea.KeyUp})
	if isDividerRow(m.pipelineTbl.Rows(), m.pipelineTbl.Cursor()) {
		m.pipelineTbl.MoveUp(1)
	}
	if got, want := m.pipelineTbl.Cursor(), divider-1; got != want {
		t.Errorf("up onto the divider left cursor %d, want %d", got, want)
	}
	if p := m.selectedPlugin(); p == nil {
		t.Error("cursor rests on the divider after an upward nudge")
	}
	assertPipelineSelectionVisible(t, m, "after an upward nudge")

	// A rebuild whose cursor lands on the divider nudges past it and keeps it on screen.
	setCursorVisible(&m.pipelineTbl, divider)
	m.rebuildPipelineTable()
	if isDividerRow(m.pipelineTbl.Rows(), m.pipelineTbl.Cursor()) {
		t.Errorf("rebuild left the cursor on the divider at %d", m.pipelineTbl.Cursor())
	}
	assertPipelineSelectionVisible(t, m, "after a rebuild off the divider")
}

// The clamp at both ends: an inbound-only chain puts the divider last, where nudging DOWN
// has nowhere to go. SetCursor(+1) clamped there and so does MoveDown(1) — the cursor stays
// on the divider, and selectedPlugin returning nil is what keeps the detail pane honest.
func TestPipelineTable_DividerNudgeClampsAtTheEnds(t *testing.T) {
	m := pipelineModel(t, 20, 0)
	rows := m.pipelineTbl.Rows()
	if last := len(rows) - 1; !isDividerRow(rows, last) {
		t.Fatalf("expected the divider last in an inbound-only chain, rows=%d", len(rows))
	}
	last := len(rows) - 1
	m.pipelineTbl.SetCursor(last)
	m.pipelineTbl.MoveDown(1)
	if got := m.pipelineTbl.Cursor(); got != last {
		t.Errorf("nudge past the last row moved cursor to %d, want %d", got, last)
	}
	if p := m.selectedPlugin(); p != nil {
		t.Errorf("divider row reported plugin %q", p.Name)
	}

	// And an outbound-only chain puts it first, where nudging UP has nowhere to go.
	m = pipelineModel(t, 0, 20)
	if !isDividerRow(m.pipelineTbl.Rows(), 0) {
		t.Fatal("expected the divider first in an outbound-only chain")
	}
	m.pipelineTbl.SetCursor(0)
	m.pipelineTbl.MoveUp(1)
	if got := m.pipelineTbl.Cursor(); got != 0 {
		t.Errorf("nudge above the first row moved cursor to %d, want 0", got)
	}
}

// assertPipelineSelectionVisible is assertSelectionVisible for the pipeline table, whose rows
// carry plugin names rather than the host marker.
func assertPipelineSelectionVisible(t *testing.T, m *model, label string) {
	t.Helper()
	row := m.pipelineTbl.SelectedRow()
	if len(row) < 3 {
		t.Errorf("%s: no row selected", label)
		return
	}
	if name := strings.TrimSpace(row[2]); !strings.Contains(m.pipelineTbl.View(), name) {
		t.Errorf("%s: selected row %d (%q) is off screen", label, m.pipelineTbl.Cursor(), name)
	}
}
