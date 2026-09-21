package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/rossoctl/cortex/authbridge/authlib/pipeline"
	"github.com/rossoctl/cortex/authbridge/authlib/session"
)

// toolsOf builds a manifest of n tools. Only its LENGTH matters to sessionContext: a request that
// carries any tools is an agentic conversation, one that carries none is a one-shot completion.
func toolsOf(n int) []pipeline.InferenceTool {
	out := make([]pipeline.InferenceTool, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, pipeline.InferenceTool{Name: fmt.Sprintf("Tool%02d", i)})
	}
	return out
}

// exchange is a request/response pair as the store records one: the manifest and the message count
// on both sides, the token counts on the response, since the provider is the only party that
// tokenizes.
func exchange(id string, at time.Time, msgs, ntools, input, cacheRead int) []pipeline.SessionEvent {
	inf := func() *pipeline.InferenceExtension {
		return &pipeline.InferenceExtension{
			Model:    "claude-opus-5",
			Messages: make([]pipeline.InferenceMessage, msgs),
			Tools:    toolsOf(ntools),
		}
	}
	respInf := inf()
	respInf.InputTokens, respInf.CacheReadTokens = input, cacheRead
	return []pipeline.SessionEvent{
		{At: at, RequestID: id, Phase: pipeline.SessionRequest,
			Direction: pipeline.Outbound, Inference: inf()},
		{At: at.Add(time.Second), RequestID: id, Phase: pipeline.SessionResponse,
			Direction: pipeline.Outbound, Inference: respInf},
	}
}

// conversation is an agentic turn: 27 tools, as every main-thread request measured carried.
func conversation(id string, at time.Time, msgs, context int) []pipeline.SessionEvent {
	return exchange(id, at, msgs, 27, 300, context-300)
}

// oneShot is a title / quota / summary call: no tools, three messages, and — the part that
// matters — a context that can be large.
func oneShot(id string, at time.Time, context int) []pipeline.SessionEvent {
	return exchange(id, at, 3, 0, 300, context-300)
}

// THE CASE THIS COLUMN WAS REPORTED FOR. Real interleaving from one live session: a conversation
// at ~1500 messages and 830-851k, with one-shots at 3 messages carrying 282k and 6k landing
// between its turns. Before this rule the gauge followed whichever spoke last, swinging 83% to
// 0.7% between adjacent turns.
func TestSessionContext_IgnoresOneShotsBetweenTurns(t *testing.T) {
	base := time.Now()
	at := func(n int) time.Time { return base.Add(time.Duration(n) * time.Minute) }
	var evs []pipeline.SessionEvent
	for _, e := range [][]pipeline.SessionEvent{
		conversation("c1", at(1), 1491, 830_000),
		oneShot("o1", at(2), 282_145),
		oneShot("o2", at(3), 282_493),
		conversation("c2", at(4), 1494, 835_000),
		oneShot("o3", at(5), 6_538),
		conversation("c3", at(6), 1509, 851_000),
		oneShot("o4", at(7), 7_000), // the most recent event of all
	} {
		evs = append(evs, e...)
	}

	if got, want := sessionContext(evs), 851_000; got != want {
		t.Errorf("sessionContext = %d, want %d — the conversation's latest turn, not the "+
			"one-shot that spoke after it", got, want)
	}
}

// A ONE-SHOT RUN OF ANY LENGTH MUST NOT WIN, which is why there is no window: the conversation
// goes silent while a subagent works, and that silence is structural rather than evidence it has
// gone. Thirty one-shots after the last conversation turn is past any last-N window.
func TestSessionContext_SurvivesALongSilence(t *testing.T) {
	base := time.Now()
	evs := conversation("c1", base, 700, 445_000)
	for i := 0; i < 30; i++ {
		evs = append(evs, oneShot(fmt.Sprintf("o%d", i),
			base.Add(time.Duration(i+1)*time.Minute), 186_870)...)
	}

	if got, want := sessionContext(evs), 445_000; got != want {
		t.Errorf("sessionContext = %d, want %d — a silent conversation must not age out",
			got, want)
	}
}

// Among conversation turns the most messages wins, and equal counts keep the most recent. The
// message count is what identifies the main thread: a subagent carrying its own tools IS a
// candidate, and cannot out-message a long conversation.
func TestSessionContext_MostMessagesWinsTiesGoToTheLatest(t *testing.T) {
	base := time.Now()
	var evs []pipeline.SessionEvent
	for _, e := range [][]pipeline.SessionEvent{
		conversation("main", base, 700, 445_000),
		conversation("sub", base.Add(time.Minute), 12, 40_000),       // a tool-carrying subagent
		conversation("main2", base.Add(2*time.Minute), 700, 448_000), // ties on messages
	} {
		evs = append(evs, e...)
	}

	if got, want := sessionContext(evs), 448_000; got != want {
		t.Errorf("sessionContext = %d, want %d", got, want)
	}
}

// THE RESPONSE'S OWN MANIFEST IS THE FILTER, and the request's is deliberately not consulted.
//
// An earlier version required the paired request to carry tools too, justified as insurance.
// It was dead code: SnapshotInference is `c := *ext`, a shallow copy, and Tools is only appended
// while parsing the REQUEST — so both snapshots carry the same slice header off the same
// extension and cannot disagree. The pairing needed a map keyed by request id, which is what made
// this function allocate on every call, for a branch that could not be reached.
func TestSessionContext_JudgesTheResponsesOwnManifest(t *testing.T) {
	evs := []pipeline.SessionEvent{{
		At: time.Now(), Phase: pipeline.SessionResponse, Direction: pipeline.Outbound,
		Inference: &pipeline.InferenceExtension{
			Model: "claude-opus-5", Messages: make([]pipeline.InferenceMessage, 50),
			Tools: toolsOf(27), InputTokens: 1_000, CacheReadTokens: 99_000,
		},
	}}

	if got, want := sessionContext(evs), 100_000; got != want {
		t.Errorf("sessionContext = %d, want %d — a response with tools and tokens is a "+
			"candidate on its own account", got, want)
	}
}

// ONLY RESPONSES. A request snapshot carries no token counts, so it would be dropped anyway — but
// by accident of when SnapshotInference copies, not because the loop said so. This pins the
// phase check that makes the rule explicit: a request bearing tokens must still not count.
func TestSessionContext_IgnoresRequestEventsEvenWithCounts(t *testing.T) {
	inf := &pipeline.InferenceExtension{
		Model: "claude-opus-5", Messages: make([]pipeline.InferenceMessage, 900),
		Tools: toolsOf(27), InputTokens: 1_000, CacheReadTokens: 499_000,
	}
	evs := []pipeline.SessionEvent{
		{At: time.Now(), Phase: pipeline.SessionRequest, Direction: pipeline.Outbound,
			Inference: inf},
	}

	if got := sessionContext(evs); got != 0 {
		t.Errorf("sessionContext = %d, want 0 — the prompt side is read off the response", got)
	}
}

// THE COMPACTION TRADEOFF, pinned so it cannot be "fixed" by reintroducing the window that was
// ruled out.
//
// A compaction restarts the conversation at a low message count while the pre-compaction turn
// stays retained with 1500 of them, so the older, longer turn keeps winning and the gauge holds
// the old figure. That is deliberate: a stale figure beats one that flips to a one-shot's. If this
// test starts failing because a recency rule was added, the silence problem is back with it — the
// main thread goes quiet while a subagent runs, and a last-N window fills with its traffic.
func TestSessionContext_HoldsThePreCompactionFigure(t *testing.T) {
	base := time.Now()
	evs := conversation("before", base, 1500, 851_000)
	evs = append(evs, conversation("after", base.Add(time.Hour), 40, 62_000)...)

	if got, want := sessionContext(evs), 851_000; got != want {
		t.Errorf("sessionContext = %d, want %d — the stale-after-compaction tradeoff changed; "+
			"see the doc comment before accepting a new expectation here", got, want)
	}
}

// Nothing to say is zero, which the gauge renders as a dash rather than an empty track.
func TestSessionContext_ZeroWhenNothingCanBeSaid(t *testing.T) {
	base := time.Now()
	for _, tc := range []struct {
		name   string
		events []pipeline.SessionEvent
	}{
		{"no events at all", nil},
		{"no inference on any event", []pipeline.SessionEvent{
			{Phase: pipeline.SessionRequest}, {Phase: pipeline.SessionResponse},
		}},
		// Every request a one-shot: there is no conversation to report on, and reporting a
		// one-shot's own context is the defect this rule exists to fix.
		{"one-shots only", append(oneShot("o1", base, 60_000), oneShot("o2", base, 61_000)...)},
		// A conversation whose response reported no token counts at all — through exchange
		// directly, because conversation() takes a context and cannot express zero.
		{"a conversation with no prompt counts", exchange("c1", base, 40, 27, 0, 0)},
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
		"ctx": conversation("c1", time.Now(), 600, 500_000),
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
