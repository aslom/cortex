package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/rossoctl/cortex/authbridge/authlib/pipeline"
)

// detailEventLoadedMsg carries the full event the detail pane asked for.
type detailEventLoadedMsg struct {
	sessionID string
	seq       uint64
	event     *pipeline.SessionEvent
	err       error
}

// needsFullEvent reports whether the detail pane has to fetch before it can show
// everything.
//
// Two conditions, both required. The server must actually be projecting — against
// a proxy that predates view=summary the timeline already carries whole events, and
// re-fetching one would be a round trip for bytes abctl is holding. And the event
// must have a protocol extension at all: a plain CONNECT tunnel or a bare denial
// has no message body to be missing, so fetching would confirm what is already on
// screen.
func needsFullEvent(serverProjects bool, e *pipeline.SessionEvent) bool {
	if !serverProjects || e == nil {
		return false
	}
	return e.Inference != nil || e.A2A != nil || e.MCP != nil
}

// fetchDetailEventCmd fetches one event in full for the detail pane.
//
// The row is rendered from the summary FIRST and this fills the bodies in when it
// arrives — deliberately, rather than blocking on a spinner. Everything the
// summary carries is worth reading immediately (status, duration, tokens, every
// plugin invocation), the fetch is milliseconds against a local proxy, and a
// timeline that opens instantly only to show "loading…" would have traded one
// wait for another.
//
// Carries the session id and seq so the handler can drop a reply for a row the
// operator has already navigated away from.
func fetchDetailEventCmd(m *model, sessionID string, seq uint64) tea.Cmd {
	ctx, client := m.ctx, m.client
	return func() tea.Msg {
		ev, err := client.GetEvent(ctx, sessionID, seq)
		return detailEventLoadedMsg{sessionID: sessionID, seq: seq, event: ev, err: err}
	}
}

// applyDetailEvent installs a fetched full event and re-renders, or reports why it
// could not.
//
// Ignores a reply that no longer matches what is on screen: the operator can move
// the cursor or leave the pane while the fetch is in flight, and a late reply must
// not redraw the detail of a row they are no longer looking at.
func (m *model) applyDetailEvent(msg detailEventLoadedMsg) {
	if m.pane != paneDetail || m.detailEvent == nil {
		return
	}
	if m.selectedSess != msg.sessionID || m.detailEvent.Seq != msg.seq {
		return
	}
	if msg.err != nil {
		// A footer flash rather than replacing the pane: what is already rendered is
		// correct and useful, it is only missing the bodies, so destroying it to
		// report the failure would be a net loss of information.
		m.setFlash("could not load the full event: " + msg.err.Error())
		return
	}
	// Write the full event back into the slice abctl holds, so re-opening the same
	// row is free. Without this the summary stays in m.events and every ↵ → Esc → ↵
	// pays the round trip again for bytes already fetched.
	//
	// Matched on Seq rather than position: the slice is rebuilt from the stream and
	// snapshots, so an index taken when the fetch was issued may not be the same row
	// by the time it lands.
	m.replaceHeldEvent(msg.sessionID, msg.event)

	// Re-render through showDetail so the tunnel/TLS headers, wrapping and scroll
	// position are all rebuilt exactly as the first render built them.
	m.detailRow.event = msg.event
	m.showDetail(m.detailRow, false)
}

// replaceHeldEvent swaps the stored event with the same Seq for the full one.
//
// A no-op when the session is gone or the Seq is not held — both happen normally
// (a pod switch, FIFO eviction) and neither is worth reporting: the detail pane
// already has what it fetched.
func (m *model) replaceHeldEvent(sessionID string, full *pipeline.SessionEvent) {
	if full == nil || full.Seq == 0 {
		return
	}
	held := m.events[sessionID]
	for i := range held {
		if held[i].Seq == full.Seq {
			held[i] = *full
			return
		}
	}
}

// detailIsProjected reports that the event on screen is still a summary: the
// server projects, it carries a protocol extension, and the bodies have not
// arrived — either because the fetch is in flight or because it failed.
//
// Used to warn on yank. `y` writes the event's JSON out for debugging, and handing
// somebody a body-less event that looks complete is the kind of surprise that
// wastes an afternoon.
func (m *model) detailIsProjected() bool {
	e := m.detailEvent
	if !needsFullEvent(m.serverProjects, e) {
		return false
	}
	// The fields the projection drops. Any one of them present means the full event
	// has landed; a genuinely empty conversation is indistinguishable, and calling
	// that "projected" is the safe direction — the note says the bodies may be
	// missing, not that they are.
	if e.Inference != nil && (len(e.Inference.Messages) > 0 || len(e.Inference.Tools) > 0) {
		return false
	}
	if e.MCP != nil && (e.MCP.Params != nil || e.MCP.Result != nil) {
		return false
	}
	if e.A2A != nil && e.A2A.Artifact != "" {
		return false
	}
	return true
}
