package tui

import (
	"context"
	"testing"
	"time"

	"github.com/rossoctl/cortex/authbridge/authlib/pipeline"
)

// projected models what the TIMELINE delivers: sessionapi.summarizeEvent's inference half.
//
// MIRRORED RATHER THAN CALLED — summarizeEvent is unexported and in another module, and authlib's
// own tests pin what it strips (Messages, Tools, ToolCalls; Completion is kept because the events
// filter searches it).
//
// EVERY OTHER FIXTURE IN THIS PACKAGE BUILDS Tools BY HAND, and that is exactly how a whole suite
// stayed green while the column was blank for every row the server sent: the tool-manifest filter
// and the projection strip the same two fields, so a hand-built fixture models the SSE stream and
// nothing else. Anything asserting on the gauge against server-delivered events has to come
// through here.
func projected(events []pipeline.SessionEvent) []pipeline.SessionEvent {
	out := make([]pipeline.SessionEvent, 0, len(events))
	for _, e := range events {
		c := e
		if e.Inference != nil {
			inf := *e.Inference
			inf.Messages = nil
			inf.Tools = nil
			inf.ToolCalls = nil
			c.Inference = &inf
		}
		out = append(out, c)
	}
	return out
}

// THE TIMELINE CANNOT ANSWER THIS QUESTION, stated as a test so nobody has to rediscover it.
//
// Measured against a live proxy, the same 200-event window both ways: 41 of 62 inference responses
// carry a manifest unprojected, 0 of 62 with view=summary — and abctl asks for view=summary on
// every timeline fetch. So a projected event is not a one-shot and not a conversation turn; it is
// unreadable, and the gauge's answer for it has to come from somewhere else (the stream, or a
// detail fetch).
func TestSessionContext_IsBlindToProjectedEvents(t *testing.T) {
	full := conversation("c1", time.Now(), 600, 500_000)
	if got, want := sessionContext(full), 500_000; got != want {
		t.Fatalf("unprojected fixture = %d, want %d", got, want)
	}
	if got := sessionContext(projected(full)); got != 0 {
		t.Errorf("projected = %d, want 0 — if this now answers, the filter reads a field the "+
			"projection keeps and the rebase machinery may be unnecessary", got)
	}
}

// THE REGRESSION BOTH REVIEWS CAUGHT: opening a session must not blank its gauge.
//
// The stream establishes the figure, the operator presses Enter, and the snapshot replaces every
// held event with a projected copy carrying no candidate. Dropping the running answer there — the
// obvious invalidation — turned the column into a dash for exactly the session being looked at.
func TestSessionContextFor_AProjectedSnapshotKeepsTheStreamsFigure(t *testing.T) {
	base := time.Now()
	const id = "s"
	full := conversation("c1", base, 600, 500_000)
	m := &model{events: map[string][]pipeline.SessionEvent{id: full}}
	m.sessionsTbl = newSessionsTable()

	if got, want := m.sessionContextFor(id), 500_000; got != want {
		t.Fatalf("from the stream: %d, want %d", got, want)
	}

	// The real handler, and the real shape: same events, same count, projected.
	m.Update(snapshotLoadedMsg{id: id, events: projected(full), projected: true})

	if got, want := m.sessionContextFor(id), 500_000; got != want {
		t.Errorf("after a view=summary snapshot: %d, want %d — the column blanked on drill-in",
			got, want)
	}
}

// An older page is projected too, and it arrives in FRONT of the folded events — so the run cannot
// be extended and must not be discarded either.
//
// THE HELD EVENTS HAVE TO BE PROJECTED for this to test anything, which the first version of it got
// wrong. With an unprojected conversation still in the slice, dropping the run and rescanning finds
// that conversation again and reports the same figure, so the assertion passed with the fix
// reverted. The production sequence is the one built here: stream, [t] (snapshot, projected), [o].
func TestSessionContextFor_AnOlderPageKeepsTheFigure(t *testing.T) {
	base := time.Now()
	full := conversation("c1", base, 600, 500_000)
	m := pagedModel(t, full)

	if got, want := m.sessionContextFor("sess-1"), 500_000; got != want {
		t.Fatalf("from the stream: %d, want %d", got, want)
	}
	// The snapshot leaves the slice projected — and clears the paging state, so [o] rebuilds it.
	m.Update(snapshotLoadedMsg{id: "sess-1", events: projected(full), projected: true})
	m.paging = map[string]*pagingState{"sess-1": {pageSizes: []int{len(full)}}}

	// Older in wall-clock terms, or applyOlderPage refuses it as a restarted session.
	older := projected(conversation("c0", base.Add(-time.Hour), 40, 62_000))
	m.applyOlderPage(olderPageLoadedMsg{id: "sess-1", events: older, serverOldest: 1})

	if got, want := m.sessionContextFor("sess-1"), 500_000; got != want {
		t.Errorf("after an older page: %d, want %d — nothing in the slice can answer this "+
			"question any more, so the remembered figure is the only source left", got, want)
	}
}

// THE DETAIL FETCH IS THE ONLY PATH THAT PUTS A MANIFEST BACK, so it is the only way a session
// abctl never streamed can ever show a gauge — and it changes an event's CONTENT at the same
// length, which the fold's length check cannot see.
func TestSessionContextFor_ADetailFetchFillsTheGauge(t *testing.T) {
	base := time.Now()
	const id = "s"
	full := conversation("c1", base, 600, 500_000)
	for i := range full {
		full[i].Seq = uint64(i + 1)
	}
	// A session abctl attached to after its traffic: the timeline is all it has.
	m := &model{events: map[string][]pipeline.SessionEvent{id: projected(full)}}
	if got := m.sessionContextFor(id); got != 0 {
		t.Fatalf("a projected timeline: %d, want 0 (the dash)", got)
	}

	// The operator opens the response row; the fetched event is unprojected.
	resp := full[1]
	m.replaceHeldEvent(id, &resp)

	if got, want := m.sessionContextFor(id), 500_000; got != want {
		t.Errorf("after the detail fetch: %d, want %d — the write-back was invisible to the "+
			"length check", got, want)
	}
}

// A DIFFERENT POD IS A DIFFERENT WORKLOAD, and it is the one case where a remembered figure is
// void rather than merely unsupported: the same session id now names someone else's conversation.
// Matching event counts make it a cache HIT, so the wrong figure would render rather than leak.
func TestSessionContextFor_APodSwitchVoidsTheFigure(t *testing.T) {
	base := time.Now()
	m := fitModel(t, paneEvents, 200, 40, conversation("c1", base, 600, 500_000))
	m.parentCtx, m.ctx = context.Background(), context.Background()
	m.cancel = func() {}

	if got, want := m.sessionContextFor("sess-1"), 500_000; got != want {
		t.Fatalf("on the first pod: %d, want %d", got, want)
	}

	m.backToPodsPane()
	// The next pod happens to have a session with the same id and the same event count.
	m.events["sess-1"] = conversation("other", base.Add(time.Hour), 40, 62_000)

	if got, want := m.sessionContextFor("sess-1"), 62_000; got != want {
		t.Errorf("on the second pod: %d, want %d — the previous pod's context carried over",
			got, want)
	}
}

// RELEASING EVENTS FOR MEMORY IS NOT NEWS ABOUT THE SESSION. The picker drops cached events for
// sessions the server still lists; the figure is one int and stays, or a live session's gauge would
// fall back to a dash for having been economical.
func TestSessionContextFor_AReleaseOfItsEventsKeepsTheFigure(t *testing.T) {
	const id = "s"
	m := &model{events: map[string][]pipeline.SessionEvent{
		id: conversation("c1", time.Now(), 600, 500_000),
	}}
	if got, want := m.sessionContextFor(id), 500_000; got != want {
		t.Fatalf("before the release: %d, want %d", got, want)
	}

	delete(m.events, id) // what the picker's prune does
	if got, want := m.sessionContextFor(id), 500_000; got != want {
		t.Errorf("after the release: %d, want %d", got, want)
	}
	// And a later streamed turn still wins on message count.
	m.events[id] = conversation("c2", time.Now().Add(time.Minute), 900, 700_000)
	if got, want := m.sessionContextFor(id), 700_000; got != want {
		t.Errorf("after a new turn: %d, want %d", got, want)
	}
}
