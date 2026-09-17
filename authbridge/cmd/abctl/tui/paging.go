package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/rossoctl/cortex/authbridge/authlib/pipeline"
	"github.com/rossoctl/cortex/authbridge/cmd/abctl/apiclient"
)

// maxPagesHeld bounds how many pages of one session abctl keeps stitched together.
//
// There is a bound at all because a page is not small: one page of a real session is
// ~175MB of JSON on the wire, and abctl reached 1.64GB — more than the proxy it was
// watching — before its decode path was fixed. Unbounded paging would put that growth back
// under a key the operator can hold down.
//
// Three, so there is a page of context on either side of the one being read. When a fourth
// arrives the NEWEST is dropped, not the oldest: paging backward means the operator is
// moving away from the tail, so the tail is the page furthest from what they are looking
// at. Dropping the oldest would discard the page just fetched and make the key a no-op.
const maxPagesHeld = 3

// pagingState is what a session's timeline needs once the operator has paged off its tail.
type pagingState struct {
	// pageSizes is the event count of each page currently stitched into m.events[id],
	// OLDEST page first. The events carry no page boundary of their own, so this is how
	// the newest page is found again when the cap evicts one.
	pageSizes []int

	// serverOldest is the Seq of the oldest event the server still holds, from the most
	// recent response. Reaching it means there is nothing left to ask for.
	serverOldest uint64

	// loading is set while a page request is in flight, so holding [o] down cannot stack
	// requests — each one costs a page of decode.
	loading bool

	// droppedNewer records that the cap has evicted at least one page from the newer end,
	// so the footer can say the timeline no longer reaches the present. Without it the
	// operator would see a timeline that simply stops, which is the failure this whole
	// change exists to stop doing.
	droppedNewer bool
}

// pagedBack reports whether the session's timeline has been walked off its tail. Live
// streamed events are not appended while it is true — see handleStreamEvent.
func (m *model) pagedBack(id string) bool {
	return m.paging[id] != nil
}

// loadOlderPage is [o]: fetch the page immediately before the oldest event held.
//
// Suspending the live stream for this session is deliberate, and it is why paging has state
// at all rather than just fetching. Streamed events append to the END of the timeline; once
// the operator is reading a page from the middle of a long session, appending live events
// would leave a gap between what they are reading and what just arrived, presented as one
// continuous list. Better to stop extending it and say so.
func (m *model) loadOlderPage() tea.Cmd {
	id := m.selectedSess
	if id == "" || m.client == nil {
		return nil
	}
	held := m.events[id]
	if len(held) == 0 {
		return nil
	}

	st := m.paging[id]
	if st == nil {
		// First [o] for this session: the events already held are page one, whatever
		// mixture of snapshot and streamed events they are.
		st = &pagingState{pageSizes: []int{len(held)}}
		if m.paging == nil {
			m.paging = map[string]*pagingState{}
		}
		m.paging[id] = st
	}
	if st.loading {
		return nil
	}

	oldest := held[0].Seq
	if oldest == 0 {
		// A proxy that predates Seq. Paging cannot work against it, and saying so beats
		// sending before=0 and silently refetching the tail forever.
		m.setFlash("this proxy is too old to page back")
		return nil
	}
	if st.serverOldest != 0 && oldest <= st.serverOldest {
		m.setFlash("at the beginning of the session")
		return nil
	}

	st.loading = true
	m.setFlash("loading older…")
	return m.olderPageCmd(id, oldest)
}

func (m *model) olderPageCmd(id string, before uint64) tea.Cmd {
	return func() tea.Msg {
		view, err := m.client.GetSessionPage(m.ctx, id, before, apiclient.SnapshotEventLimit)
		if err != nil {
			return errMsg{where: "older page " + id, err: err}
		}
		return olderPageLoadedMsg{id: id, events: view.Events, serverOldest: view.OldestSeq}
	}
}

// applyOlderPage stitches a fetched page onto the front of the timeline.
func (m *model) applyOlderPage(msg olderPageLoadedMsg) {
	st := m.paging[msg.id]
	if st == nil {
		// The operator returned to the tail, or left the session, while this was in
		// flight. Dropping it is correct: the state it would extend is gone.
		return
	}
	st.loading = false
	st.serverOldest = msg.serverOldest

	if len(msg.events) == 0 {
		m.setFlash("at the beginning of the session")
		return
	}

	// A fresh slice, oldest first: appending to the held events would put the older page
	// after the newer ones, and growing the page in place would alias the response.
	held := m.events[msg.id]
	merged := make([]pipeline.SessionEvent, 0, len(msg.events)+len(held))
	merged = append(merged, msg.events...)
	merged = append(merged, held...)
	st.pageSizes = append([]int{len(msg.events)}, st.pageSizes...)

	for len(st.pageSizes) > maxPagesHeld {
		newest := st.pageSizes[len(st.pageSizes)-1]
		st.pageSizes = st.pageSizes[:len(st.pageSizes)-1]
		merged = merged[:len(merged)-newest]
		st.droppedNewer = true
	}
	m.events[msg.id] = merged

	// The count is decremented rather than recomputed from the server's total, because
	// the total counts events at BOTH ends: once the cap has dropped a newer page,
	// total-minus-held would report those as older and send the operator looking for them
	// in the wrong direction. Pages are contiguous going back, so subtracting what just
	// arrived is exact — give or take events appended since, which only ever makes this
	// an undercount of what is now available.
	if n := m.olderNotFetched[msg.id] - len(msg.events); n > 0 {
		m.olderNotFetched[msg.id] = n
	} else {
		m.olderNotFetched[msg.id] = 0
	}
	m.setFlash("")
}

// returnToTail is [t]: discard the paged window and refetch the newest page, which also
// resumes live appends for this session.
func (m *model) returnToTail() tea.Cmd {
	id := m.selectedSess
	if id == "" || !m.pagedBack(id) {
		return nil
	}
	// Cleared before the fetch, not on its return: live appends resume immediately, and
	// the snapshot that lands will replace the window anyway. Leaving it set would drop
	// every event that arrives while the request is in flight.
	delete(m.paging, id)
	m.setFlash("returning to the live tail…")
	return m.snapshotCmd(id)
}
