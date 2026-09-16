package session

import (
	"fmt"
	"strings"
	"testing"
	"unsafe"

	"github.com/rossoctl/cortex/authbridge/authlib/pipeline"
)

// backing returns the address of a string's bytes.
//
// Pointer identity is the only honest assertion here. A RSS or allocation measurement is
// what the change is FOR, but it is noisy enough to pass while the code does nothing;
// two strings sharing a backing array is exactly the claim, and it is deterministic.
func backing(s string) uintptr {
	return uintptr(unsafe.Pointer(unsafe.StringData(s)))
}

// convo builds a turn's worth of messages: the whole conversation so far, as an LLM
// request actually carries it, with one new message on the end.
func convo(turns int) []pipeline.InferenceMessage {
	out := make([]pipeline.InferenceMessage, 0, turns)
	for i := 0; i < turns; i++ {
		out = append(out, pipeline.InferenceMessage{
			Role: "user",
			// Distinct per index, long enough to be worth interning, and rebuilt from
			// scratch on every call — so a test that sees sharing is seeing the store
			// share it, not the fixture handing out one string twice.
			Content: fmt.Sprintf("message %04d: %s", i, strings.Repeat("prompt text ", 8)),
		})
	}
	return out
}

// The claim: appending N turns of a growing conversation keeps ONE copy of each message,
// not one per turn.
func TestAppend_SharesRepeatedMessageContent(t *testing.T) {
	s := New(0, 0, 100)
	defer s.Close()

	const turns = 25
	for i := 1; i <= turns; i++ {
		s.Append("s1", pipeline.SessionEvent{
			Phase:     pipeline.SessionRequest,
			Inference: &pipeline.InferenceExtension{Model: "m", Messages: convo(i)},
		})
	}

	v := s.View("s1")
	if len(v.Events) != turns {
		t.Fatalf("stored %d events, want %d", len(v.Events), turns)
	}

	// Message 0 appears in all 25 events. Every occurrence must be the same bytes.
	first := backing(v.Events[0].Inference.Messages[0].Content)
	shared := 0
	for i := range v.Events {
		got := backing(v.Events[i].Inference.Messages[0].Content)
		if got == first {
			shared++
			continue
		}
		t.Errorf("event %d holds its own copy of message 0", i)
	}
	if shared != turns {
		t.Errorf("message 0 shared by %d of %d events", shared, turns)
	}

	// And the content still reads correctly — sharing must not have crossed any wires.
	want := convo(1)[0].Content
	if got := v.Events[turns-1].Inference.Messages[0].Content; got != want {
		t.Errorf("message 0 content changed: %q", trunc(got))
	}
}

// Distinct content is left distinct: interning must not collapse two different messages.
func TestAppend_DoesNotMergeDifferentContent(t *testing.T) {
	s := New(0, 0, 100)
	defer s.Close()

	msgs := convo(3)
	s.Append("s1", pipeline.SessionEvent{Inference: &pipeline.InferenceExtension{Messages: msgs}})

	v := s.View("s1")
	got := v.Events[0].Inference.Messages
	for i := range got {
		if got[i].Content != msgs[i].Content {
			t.Errorf("message %d = %q, want %q", i, trunc(got[i].Content), trunc(msgs[i].Content))
		}
		for j := range got {
			if i != j && backing(got[i].Content) == backing(got[j].Content) {
				t.Errorf("messages %d and %d were collapsed into one string", i, j)
			}
		}
	}
}

// Two sessions must not share a table. The content is identical here, and it still has
// to be two copies: one session's eviction cannot be allowed to free another's messages,
// and a table shared across conversations would outlive whichever ended first.
func TestAppend_DoesNotShareAcrossSessions(t *testing.T) {
	s := New(0, 0, 100)
	defer s.Close()

	msgs := convo(2)
	s.Append("s1", pipeline.SessionEvent{Inference: &pipeline.InferenceExtension{Messages: convo(2)}})
	s.Append("s2", pipeline.SessionEvent{Inference: &pipeline.InferenceExtension{Messages: convo(2)}})

	a := backing(s.View("s1").Events[0].Inference.Messages[0].Content)
	b := backing(s.View("s2").Events[0].Inference.Messages[0].Content)
	if a == b {
		t.Error("two sessions share one copy of the same message")
	}
	_ = msgs
}

// Short strings skip the table: a role or a finish reason costs less to duplicate than
// to hash, and the savings are in bodies.
func TestIntern_SkipsShortStrings(t *testing.T) {
	var in interner
	next := map[string]string{}
	short := strings.Repeat("x", internMinLen-1)
	if got := in.intern(short, next); backing(got) != backing(short) {
		t.Error("a short string was interned")
	}
	if len(next) != 0 {
		t.Errorf("table grew by %d for a short string", len(next))
	}

	long := strings.Repeat("y", internMinLen)
	in.intern(long, next)
	if len(next) != 1 {
		t.Errorf("table holds %d entries after one long string, want 1", len(next))
	}
}

// A2A part content interns too — the other place a conversation repeats itself.
func TestAppend_SharesA2APartContent(t *testing.T) {
	s := New(0, 0, 100)
	defer s.Close()

	// Built fresh inside the loop, not hoisted into a variable. A single string handed
	// to four events is shared by every event whether or not the store interns
	// anything, which is a test that cannot fail — the first version of this one did
	// exactly that and passed with interning disabled.
	body := func() string { return strings.Repeat("intent text ", 16) }
	for i := 0; i < 4; i++ {
		s.Append("s1", pipeline.SessionEvent{
			Direction: pipeline.Inbound, Phase: pipeline.SessionRequest,
			A2A: &pipeline.A2AExtension{Parts: []pipeline.A2APart{{Content: body()}}},
		})
	}

	v := s.View("s1")
	first := backing(v.Events[0].A2A.Parts[0].Content)
	for i := range v.Events {
		if backing(v.Events[i].A2A.Parts[0].Content) != first {
			t.Errorf("event %d holds its own copy of the A2A part", i)
		}
	}
}

// The table holds one event's strings, not the session's. Otherwise it grows with the
// conversation and has to be pruned in step with event eviction, which is the coupling
// this design avoids.
func TestIntern_TableDoesNotGrowWithTheSession(t *testing.T) {
	s := New(0, 0, 100)
	defer s.Close()

	for i := 1; i <= 30; i++ {
		s.Append("s1", pipeline.SessionEvent{
			Inference: &pipeline.InferenceExtension{Messages: convo(i)},
		})
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	if got, want := len(s.sessions["s1"].intern.prev), 30; got != want {
		t.Errorf("table holds %d entries after 30 turns, want %d (the last event's messages)", got, want)
	}
}

func trunc(s string) string {
	if len(s) <= 48 {
		return s
	}
	return s[:48] + "…"
}
