package tui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rossoctl/cortex/authbridge/authlib/pipeline"
	"github.com/rossoctl/cortex/authbridge/cmd/abctl/apiclient"
)

// A truncated timeline has to say so. Without this the events pane looks identical
// whether a session has 3 events or 5000 of which 3 arrived — which is exactly how the
// unbounded-snapshot bug presented: a full session rendering as three rows, with the
// failure only visible as a transport error in the footer.
func TestFooter_NamesTheEventsTheSnapshotDidNotFetch(t *testing.T) {
	m := fitModel(t, paneEvents, 200, 40, cursorRowsFixture(3))
	m.rebuildEventsTable()

	if got := m.helpView(); strings.Contains(got, "not fetched") {
		t.Fatalf("an untruncated snapshot must say nothing about older events: %q", got)
	}

	m.Update(snapshotLoadedMsg{id: m.selectedSess, events: cursorRowsFixture(3), olderNotFetched: 4571})
	got := m.helpView()
	if !strings.Contains(got, "4571 older not fetched") {
		t.Errorf("footer does not name the omitted events: %q", got)
	}
}

// The count comes from the server's own total, and this drives the real snapshotCmd
// against a real HTTP server rather than re-implementing its arithmetic in the test —
// a test that recomputes the thing it is checking passes whatever the code does.
func TestSnapshotCmd_DerivesTheOlderCountFromTotalEvents(t *testing.T) {
	for _, tc := range []struct {
		name        string
		total, sent int
		want        int
	}{
		{"truncated", 5000, 4, 4996},
		{"whole session reports nothing", 0, 4, 0},
		{"total equal to sent reports nothing", 4, 4, 0},
		{"a total below sent cannot underflow", 2, 4, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(pipeline.SessionView{
					ID:          "s1",
					Events:      cursorRowsFixture(tc.sent),
					TotalEvents: tc.total,
				})
			}))
			defer ts.Close()

			m := &model{ctx: context.Background(), client: apiclient.New(ts.URL)}
			msg, ok := m.snapshotCmd("s1")().(snapshotLoadedMsg)
			if !ok {
				t.Fatalf("snapshotCmd returned %T, want snapshotLoadedMsg", m.snapshotCmd("s1")())
			}
			if msg.olderNotFetched != tc.want {
				t.Errorf("olderNotFetched = %d, want %d", msg.olderNotFetched, tc.want)
			}
			if len(msg.events) != tc.sent {
				t.Errorf("events = %d, want %d", len(msg.events), tc.sent)
			}
		})
	}
}

// Snapshots land asynchronously, for whichever session was selected when the fetch
// started. A late one for a session the operator has left must not describe the session
// they are looking at — which is what a single shared counter did.
func TestFooter_OlderCountIsPerSession(t *testing.T) {
	m := fitModel(t, paneEvents, 200, 40, cursorRowsFixture(3))
	m.selectedSess = "current"
	m.events["current"] = cursorRowsFixture(3)
	m.rebuildEventsTable()

	// The snapshot for the session being viewed: nothing omitted.
	m.Update(snapshotLoadedMsg{id: "current", events: cursorRowsFixture(3)})
	// Then a straggler for a session that was abandoned, reporting thousands omitted.
	m.Update(snapshotLoadedMsg{id: "abandoned", events: cursorRowsFixture(1), olderNotFetched: 9999})

	if got := m.helpView(); strings.Contains(got, "9999") {
		t.Errorf("a late snapshot for another session leaked into the footer: %q", got)
	}

	// And selecting that session shows its own count, not the current one's.
	m.selectedSess = "abandoned"
	if got := m.helpView(); !strings.Contains(got, "9999 older not fetched") {
		t.Errorf("the session's own count is not shown after selecting it: %q", got)
	}
}
