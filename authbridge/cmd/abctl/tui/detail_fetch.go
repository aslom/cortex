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
	// Re-render through showDetail so the tunnel/TLS headers, wrapping and scroll
	// position are all rebuilt exactly as the first render built them.
	m.detailRow.event = msg.event
	m.showDetail(m.detailRow, false)
}
