package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/rossoctl/cortex/authbridge/authlib/pipeline"
	"github.com/rossoctl/cortex/authbridge/authlib/session"
)

// inferenceAt builds a response event whose prompt side sums to want.
func inferenceAt(at time.Time, input, cacheRead, cacheWrite int) pipeline.SessionEvent {
	return pipeline.SessionEvent{
		At:        at,
		Direction: pipeline.Outbound,
		Phase:     pipeline.SessionResponse,
		Inference: &pipeline.InferenceExtension{
			Model:            "claude-opus-5",
			InputTokens:      input,
			CacheReadTokens:  cacheRead,
			CacheWriteTokens: cacheWrite,
		},
	}
}

// THE LATEST REQUEST, not the largest and not the sum.
//
// A conversation's context is whatever it currently carries. TOKENS beside this column already
// answers "how much has this session spent in total", so a gauge fed the sum would be the same
// question twice — and one fed the maximum would keep showing a context the session has since
// compacted away.
func TestSessionContext_ReadsTheLatestRequest(t *testing.T) {
	base := time.Now()
	events := []pipeline.SessionEvent{
		inferenceAt(base, 500, 900_000, 0),                          // the largest, and not the answer
		{At: base.Add(time.Second), Phase: pipeline.SessionRequest}, // no Inference at all
		inferenceAt(base.Add(2*time.Second), 300, 120_000, 4_000),
	}

	if got, want := sessionContext(events), 124_300; got != want {
		t.Errorf("sessionContext = %d, want %d (input+cacheRead+cacheWrite of the LAST one)",
			got, want)
	}
}

// Nothing to say is zero, which the gauge renders as a dash rather than an empty track.
func TestSessionContext_ZeroWhenNothingCanBeSaid(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events []pipeline.SessionEvent
	}{
		{"no events at all", nil},
		{"no inference on any event", []pipeline.SessionEvent{
			{Phase: pipeline.SessionRequest}, {Phase: pipeline.SessionResponse},
		}},
		{"an inference with no prompt counts", []pipeline.SessionEvent{
			inferenceAt(time.Now(), 0, 0, 0),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sessionContext(tc.events); got != 0 {
				t.Errorf("sessionContext = %d, want 0", got)
			}
		})
	}
}

func TestContextGauge(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tokens int
		width  int
		want   string
	}{
		// The track is always drawn, so a scale is visible on every row.
		{"half full", 500_000, 10, "▕████    ▏"},
		{"nearly full", 991_200, 10, "▕███████▉▏"},
		{"full", 1_000_000, 10, "▕████████▏"},
		// A NON-ZERO CONTEXT NEVER RENDERS AS AN EMPTY TRACK. tierBar's rule, inherited: 8,200
		// of a million is 0.8%, which rounds to no block at all, so it gets the sliver instead.
		// An empty track beside a live session reads as a rendering fault.
		{"a sliver rather than nothing", 8_200, 10, "▕▏       ▏"},
		// Unknown is the em dash COST and SAVED use, and it must not be confusable with the
		// sliver above — which is the whole reason the brackets are drawn.
		{"unknown", 0, 10, emptyCell},
		// Over the window: capped rather than overflowing its column.
		{"past the window", 1_400_000, 10, "▕████████▏"},
		// Narrow terminals shrink the track with the column.
		{"the narrowest useful gauge", 500_000, 3, "▕▌▏"},
		// Below that there is nothing to draw; a bracket pair alone would claim a scale it
		// cannot show.
		{"too narrow to say anything", 500_000, 2, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := contextGauge(tc.tokens, tc.width)
			if got != tc.want {
				t.Errorf("contextGauge(%d, %d) = %q, want %q", tc.tokens, tc.width, got, tc.want)
			}
			// Exactly the column's width, so every row's gauge starts and ends in the same
			// place and the fills can be compared down the column by eye.
			if tc.want != "" && tc.want != emptyCell {
				if w := lipgloss.Width(got); w != tc.width {
					t.Errorf("gauge is %d columns, want %d: %q", w, tc.width, got)
				}
			}
		})
	}
}

// THE COLUMN REPLACED ACTIVE, and the row arity follows the header — which is the invariant
// rebuildSessionsTable's own comment calls a crash rather than a cosmetic bug.
func TestSessionsTable_ContextColumnReplacesActive(t *testing.T) {
	m := &model{width: 200}
	m.sessionsTbl = newSessionsTable()
	m.sessions = []session.SessionSummary{{
		ID: "ctx", UpdatedAt: time.Now(), EventCount: 3, TotalTokens: 500_000,
	}}
	m.events = map[string][]pipeline.SessionEvent{
		"ctx": {inferenceAt(time.Now(), 1_000, 499_000, 0)},
	}
	m.rebuildSessionsTable()

	cols := m.sessionsTbl.Columns()
	for _, c := range cols {
		if headerTitle(c) == "ACTIVE" {
			t.Error("the ACTIVE column is still here")
		}
	}
	last := headerTitle(cols[len(cols)-1])
	if last != contextColumnTitle {
		t.Fatalf("last column is %q, want %s", last, contextColumnTitle)
	}
	// The heading states the denominator, because a gauge with no scale is a decoration.
	if !strings.Contains(contextColumnTitle, "1M") {
		t.Errorf("the heading does not name its denominator: %q", contextColumnTitle)
	}

	row := m.sessionsTbl.Rows()[0]
	if len(row) != len(cols) {
		t.Fatalf("row has %d cells against %d columns", len(row), len(cols))
	}
	// 500k of 1M: a gauge half full, drawn to the fitted width.
	cell := row[len(row)-1]
	if !strings.Contains(cell, "▕") || !strings.Contains(cell, "█") {
		t.Errorf("last cell is not a gauge: %q", cell)
	}
	if got, want := lipgloss.Width(cell), cols[len(cols)-1].Width; got != want {
		t.Errorf("gauge cell is %d columns in a %d-wide column: %q", got, want, cell)
	}
}

// A session abctl has no events for shows the dash, not an empty track. On a fresh attach that is
// every session idle since before the connection, so it is the common case rather than an edge.
func TestSessionsTable_UnknownContextIsADash(t *testing.T) {
	m := &model{width: 200}
	m.sessionsTbl = newSessionsTable()
	m.sessions = []session.SessionSummary{{ID: "idle", UpdatedAt: time.Now(), EventCount: 9}}
	m.rebuildSessionsTable()

	cell := m.sessionsTbl.Rows()[0]
	if got := strings.TrimSpace(cell[len(cell)-1]); got != emptyCell {
		t.Errorf("unknown context rendered %q, want %q", got, emptyCell)
	}
}
