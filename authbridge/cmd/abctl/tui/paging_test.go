package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rossoctl/cortex/authbridge/authlib/pipeline"
	"github.com/rossoctl/cortex/authbridge/cmd/abctl/apiclient"
)

// pagingEpoch is the base timestamp for fixture events, so a test can place one batch
// before or after another in wall-clock time independently of its Seq.
var pagingEpoch = time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

// pagedEvents builds n events carrying Seq from start, which paging needs as cursors, and
// timestamps that ascend with Seq — the normal case, where the two agree.
// cursorRowsFixture leaves Seq zero, which is what a proxy predating paging sends.
func pagedEvents(start, n int) []pipeline.SessionEvent {
	return pagedEventsAt(start, n, pagingEpoch.Add(time.Duration(start)*time.Second))
}

// pagedEventsAt is pagedEvents with the timestamps placed explicitly, for the case Seq and
// wall-clock time DISAGREE: a re-created session numbers its new events from 1 again.
func pagedEventsAt(start, n int, at time.Time) []pipeline.SessionEvent {
	out := make([]pipeline.SessionEvent, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, pipeline.SessionEvent{
			Seq:       uint64(start + i),
			At:        at.Add(time.Duration(i) * time.Second),
			SessionID: "sess-1",
			Direction: pipeline.Outbound,
			Phase:     pipeline.SessionRequest,
			Host:      fmt.Sprintf("h%04d", start+i),
			Inference: &pipeline.InferenceExtension{Model: "m"},
		})
	}
	return out
}

// pagedModel is a model already one page deep, as it stands after the first [o].
//
// It sets a client, which fitModel does not. Without one, loadOlderPage returns at its
// m.client == nil guard before reaching anything else — which made the request-stacking
// test below pass whether or not the behaviour it names existed.
func pagedModel(t *testing.T, held []pipeline.SessionEvent) *model {
	t.Helper()
	m := fitModel(t, paneEvents, 200, 40, held)
	m.client = apiclient.New("http://127.0.0.1:1")
	m.ctx = context.Background()
	m.paging = map[string]*pagingState{
		"sess-1": {pageSizes: []int{len(held)}},
	}
	m.olderNotFetched = map[string]int{"sess-1": 500}
	return m
}

func heldSeqs(m *model) []uint64 {
	out := make([]uint64, 0, len(m.events["sess-1"]))
	for _, e := range m.events["sess-1"] {
		out = append(out, e.Seq)
	}
	return out
}

// An older page goes in FRONT of what is held. Appending it would put older events after
// newer ones and present them as a timeline.
func TestApplyOlderPage_PrependsInOrder(t *testing.T) {
	m := pagedModel(t, pagedEvents(11, 10))

	m.applyOlderPage(olderPageLoadedMsg{id: "sess-1", events: pagedEvents(1, 10), serverOldest: 1})

	got := heldSeqs(m)
	if len(got) != 20 {
		t.Fatalf("held %d events, want 20: %v", len(got), got)
	}
	for i, seq := range got {
		if want := uint64(i + 1); seq != want {
			t.Fatalf("event %d has Seq %d, want %d — the page was not prepended in order", i, seq, want)
		}
	}
	// The count is decremented by what arrived, not recomputed from a total.
	if got, want := m.olderNotFetched["sess-1"], 490; got != want {
		t.Errorf("olderNotFetched = %d, want %d", got, want)
	}
}

// At the cap the NEWEST page is dropped, because paging backward moves the operator away
// from the tail — dropping the oldest would discard the page just fetched.
func TestApplyOlderPage_DropsTheNewestPageAtTheCap(t *testing.T) {
	// Start at the tail: Seq 301..400 is page one.
	m := pagedModel(t, pagedEvents(301, 100))

	// Three more pages, newest-to-oldest as [o] fetches them.
	for i, start := range []int{201, 101, 1} {
		m.applyOlderPage(olderPageLoadedMsg{
			id: "sess-1", events: pagedEvents(start, 100), serverOldest: 1,
		})
		if held := len(m.paging["sess-1"].pageSizes); held > maxPagesHeld {
			t.Fatalf("after %d pages, %d are held; cap is %d", i+2, held, maxPagesHeld)
		}
	}

	got := heldSeqs(m)
	if len(got) != maxPagesHeld*100 {
		t.Fatalf("held %d events, want %d", len(got), maxPagesHeld*100)
	}
	// The three OLDEST pages survive: 1..300. The tail page (301..400) is the one dropped.
	if got[0] != 1 {
		t.Errorf("oldest held Seq = %d, want 1 — the fetched pages should survive", got[0])
	}
	if last := got[len(got)-1]; last != 300 {
		t.Errorf("newest held Seq = %d, want 300 — the tail page should have been dropped", last)
	}
	if !m.paging["sess-1"].droppedNewer {
		t.Error("droppedNewer not set; the footer would not say the timeline stops short of the present")
	}
}

// While paged back, a streamed event must not be appended: it belongs at the end of the
// session, and the end is not what is on screen.
func TestHandleStreamEvent_DoesNotAppendWhilePagedBack(t *testing.T) {
	m := pagedModel(t, pagedEvents(11, 10))
	before := len(m.events["sess-1"])

	live := pagedEvents(21, 1)[0]
	m.handleStreamEvent(apiclient.StreamEvent{Event: &live})

	if got := len(m.events["sess-1"]); got != before {
		t.Errorf("held %d events after a streamed one arrived, want %d unchanged", got, before)
	}
	// The traffic still counts — the rate meter is about the proxy, not about the window.
	if m.eventCt != 1 {
		t.Errorf("eventCt = %d, want 1: a suppressed append is still an observed event", m.eventCt)
	}
}

// Returning to the tail resumes appends immediately, without waiting for the refetch: any
// event arriving in that window would otherwise be dropped for good.
func TestReturnToTail_ResumesAppendsBeforeTheRefetchLands(t *testing.T) {
	m := pagedModel(t, pagedEvents(11, 10))
	m.returnToTail()

	if m.pagedBack("sess-1") {
		t.Fatal("still paged back after returnToTail")
	}
	live := pagedEvents(21, 1)[0]
	m.handleStreamEvent(apiclient.StreamEvent{Event: &live})
	if got := len(m.events["sess-1"]); got != 11 {
		t.Errorf("held %d events, want 11 — the streamed event was not appended", got)
	}
}

// A page can land after the operator has left the session or returned to the tail. It has
// to be dropped: the window it would extend no longer exists.
func TestApplyOlderPage_IgnoresAPageWhosePagingEnded(t *testing.T) {
	m := pagedModel(t, pagedEvents(11, 10))
	delete(m.paging, "sess-1")

	m.applyOlderPage(olderPageLoadedMsg{id: "sess-1", events: pagedEvents(1, 10), serverOldest: 1})

	if got := len(m.events["sess-1"]); got != 10 {
		t.Errorf("held %d events, want the original 10 — a stale page was stitched in", got)
	}
}

// A second [o] while one request is in flight is a no-op, so holding the key down cannot
// stack fetches that each cost a page of decode.
//
// The positive case is asserted first, and that is the point: without it this test passed
// against a model with no client at all, where loadOlderPage returns nil for a reason that
// has nothing to do with in-flight requests.
func TestLoadOlderPage_DoesNotStackRequests(t *testing.T) {
	m := pagedModel(t, pagedEvents(11, 10))

	if cmd := m.loadOlderPage(); cmd == nil {
		t.Fatal("the first request was not issued, so this test cannot show anything about the second")
	}
	if !m.paging["sess-1"].loading {
		t.Fatal("loading not set by the first request")
	}

	if cmd := m.loadOlderPage(); cmd != nil {
		t.Error("a second request was issued while one was in flight")
	}
}

// A page whose events are NEWER than what is held must be refused rather than prepended.
//
// Reachable because Seq restarts at 1 for a re-created session: TTL cleanup or max_sessions
// eviction drops the entry, traffic under the same id makes a new one, and a cursor from
// the previous incarnation is then above everything held. ViewPage answers "everything
// before that cursor" — the whole new session — and prepending it would put its newest
// events in front of the older ones already on screen.
func TestApplyOlderPage_RefusesAPageThatIsNotOlder(t *testing.T) {
	m := pagedModel(t, pagedEvents(3000, 10)) // held: Seq 3000..3009
	before := heldSeqs(m)

	// What a re-created session returns: Seq 1..5, numbered far BELOW the cursor that asked
	// for them — so a Seq comparison sees a well-ordered page — but recorded an hour after
	// everything held, because the store dropped the session and started over.
	newer := pagedEventsAt(1, 5, m.events["sess-1"][0].At.Add(time.Hour))
	m.applyOlderPage(olderPageLoadedMsg{id: "sess-1", events: newer, serverOldest: 1})

	if got := heldSeqs(m); len(got) != len(before) {
		t.Errorf("held %d events, want %d unchanged — an out-of-order page was stitched in",
			len(got), len(before))
	}
	if !strings.Contains(m.flash, "restarted") {
		t.Errorf("flash = %q, want it to name the reason", m.flash)
	}
}

// The dropped page must be cleared, not just sliced off. Reslicing leaves those events
// reachable through the backing array, so the cap would hold one page more than it claims.
func TestApplyOlderPage_ClearsTheDroppedPage(t *testing.T) {
	m := pagedModel(t, pagedEvents(301, 100))
	for _, start := range []int{201, 101, 1} {
		m.applyOlderPage(olderPageLoadedMsg{
			id: "sess-1", events: pagedEvents(start, 100), serverOldest: 1,
		})
	}

	held := m.events["sess-1"]
	// The dropped page sits just past the length, inside the same backing array.
	spare := held[:len(held)+100]
	for i := len(held); i < len(spare); i++ {
		if spare[i].Inference != nil || spare[i].Seq != 0 {
			t.Fatalf("dropped event at %d is still live (Seq %d): the cap holds more than it says",
				i, spare[i].Seq)
		}
	}
}

// Paging cannot work against a proxy that does not stamp Seq, and it should say so rather
// than send before=0 — which the server reads as "from the newest" and would refetch the
// same tail forever.
func TestLoadOlderPage_SaysSoWhenTheProxyHasNoSeq(t *testing.T) {
	m := fitModel(t, paneEvents, 200, 40, cursorRowsFixture(3)) // Seq zero throughout
	m.client = &apiclient.Client{}

	if cmd := m.loadOlderPage(); cmd != nil {
		t.Error("issued a page request against events carrying no Seq")
	}
	if !strings.Contains(m.flash, "too old") {
		t.Errorf("flash = %q, want it to name the reason", m.flash)
	}
}

// The footer has to say that live updates are suspended. A timeline that silently stopped
// updating is the same class of lie as one that silently omitted its beginning.
func TestFooter_SaysWhenPagedBack(t *testing.T) {
	m := pagedModel(t, pagedEvents(11, 10))
	m.rebuildEventsTable()

	got := m.helpView()
	if !strings.Contains(got, "live paused") || !strings.Contains(got, "[t]") {
		t.Errorf("footer does not report the paged-back state: %q", got)
	}

	m.paging["sess-1"].droppedNewer = true
	if got := m.helpView(); !strings.Contains(got, "newer dropped") {
		t.Errorf("footer does not report that newer events were dropped: %q", got)
	}
}
