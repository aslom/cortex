package tui

import (
	"fmt"
	"testing"
	"time"

	"github.com/rossoctl/cortex/authbridge/authlib/pipeline"
)

// THE FOLD MUST AGREE WITH A FULL RESCAN AT EVERY LENGTH, because the fold is the only thing that
// runs in production and the rescan is the definition.
//
// Why a fold at all: the sessions row loop asks for every session, rebuildSessionsTable runs on
// every streamed event, and retention is unbounded. Measured before this — 6.1ms and 3.49MB per
// call at 100k events, against ~14ns for the tail scan it replaced — ten sessions of that size
// cost 60ms and 35MB for one arriving event, on the pane abctl opens on.
func TestSessionContextFor_FoldMatchesAFullRescan(t *testing.T) {
	base := time.Now()
	const id = "s"
	m := &model{events: map[string][]pipeline.SessionEvent{}}

	var all []pipeline.SessionEvent
	for i := 0; i < 40; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		var next []pipeline.SessionEvent
		switch {
		case i%3 == 0:
			next = oneShot(fmt.Sprintf("o%d", i), at, 200_000+i) // a big one-shot, must never win
		case i%7 == 0:
			next = conversation(fmt.Sprintf("sub%d", i), at, 5, 30_000) // a short thread
		default:
			next = conversation(fmt.Sprintf("c%d", i), at, 100+i, 400_000+i)
		}
		all = append(all, next...)
		m.events[id] = all

		got := m.sessionContextFor(id)
		want := sessionContext(all)
		if got != want {
			t.Fatalf("after %d events: folded %d, rescan %d", len(all), got, want)
		}
	}
	// And the run really was folded rather than rescanned: every event has been accounted for.
	if run := m.contextRun[id]; run.n != len(all) {
		t.Errorf("folded %d events, slice holds %d", run.n, len(all))
	}
}

// A repeat call with nothing appended must not rescan. Asserted through the run's own counter,
// since that is what the length check reads.
func TestSessionContextFor_RepeatCallIsAHit(t *testing.T) {
	const id = "s"
	m := &model{events: map[string][]pipeline.SessionEvent{
		id: conversation("c1", time.Now(), 600, 500_000),
	}}

	first := m.sessionContextFor(id)
	run := m.contextRun[id]
	if got := m.sessionContextFor(id); got != first {
		t.Errorf("second call = %d, first = %d", got, first)
	}
	if m.contextRun[id] != run {
		t.Errorf("the run changed on a call that folded nothing: %+v -> %+v", run, m.contextRun[id])
	}
}

// A WHOLESALE REPLACEMENT OF THE SAME LENGTH must not return the old figure. The length check
// alone cannot see it, which is why both replacement paths drop the entry — and why this test
// goes through the message handler rather than calling the helper.
func TestSessionContextFor_SnapshotReplacementDropsTheRun(t *testing.T) {
	base := time.Now()
	const id = "s"
	m := &model{events: map[string][]pipeline.SessionEvent{}}
	m.sessionsTbl = newSessionsTable()
	m.events[id] = conversation("c1", base, 600, 500_000)

	if got, want := m.sessionContextFor(id), 500_000; got != want {
		t.Fatalf("before the snapshot: %d, want %d", got, want)
	}

	// Same event count, different content — a refetch of the same window after a compaction.
	m.Update(snapshotLoadedMsg{id: id, events: conversation("c2", base.Add(time.Hour), 40, 62_000)})

	if got, want := m.sessionContextFor(id), 62_000; got != want {
		t.Errorf("after the snapshot: %d, want %d — the run survived a replacement", got, want)
	}
}

// Ties keep the LATEST turn, and the fold walks forward where the rescan it replaced walked back,
// so the comparison had to flip with it. A fold using `>` would report the earlier turn.
func TestFoldSessionContext_TiesKeepTheLatest(t *testing.T) {
	base := time.Now()
	evs := conversation("first", base, 700, 445_000)
	evs = append(evs, conversation("second", base.Add(time.Minute), 700, 448_000)...)

	if got, want := sessionContext(evs), 448_000; got != want {
		t.Errorf("sessionContext = %d, want %d", got, want)
	}
	// And the same answer when the two arrive in separate folds, which is the production path.
	m := &model{events: map[string][]pipeline.SessionEvent{"s": evs[:2]}}
	_ = m.sessionContextFor("s")
	m.events["s"] = evs
	if got, want := m.sessionContextFor("s"), 448_000; got != want {
		t.Errorf("folded in two steps = %d, want %d", got, want)
	}
}
