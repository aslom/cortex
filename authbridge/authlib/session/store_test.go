package session

import (
	"encoding/json"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/rossoctl/cortex/authbridge/authlib/costevent"
	"github.com/rossoctl/cortex/authbridge/authlib/pipeline"
)

func TestStore_AppendAndView(t *testing.T) {
	s := New(5*time.Minute, 100, 0)

	ev := pipeline.SessionEvent{
		At:        time.Now(),
		Direction: pipeline.Inbound,
		Phase:     pipeline.SessionRequest,
		A2A:       &pipeline.A2AExtension{Method: "message/send"},
	}
	s.Append("sess-1", ev)

	v := s.View("sess-1")
	if v == nil {
		t.Fatal("View returned nil")
	}
	if v.ID != "sess-1" {
		t.Errorf("View.ID = %q, want %q", v.ID, "sess-1")
	}
	if len(v.Events) != 1 {
		t.Fatalf("View.Events len = %d, want 1", len(v.Events))
	}
	if v.Events[0].A2A.Method != "message/send" {
		t.Errorf("Event.A2A.Method = %q, want %q", v.Events[0].A2A.Method, "message/send")
	}
}

func TestStore_ActiveSession(t *testing.T) {
	s := New(5*time.Minute, 100, 0)

	if id := s.ActiveSession(); id != "" {
		t.Errorf("ActiveSession() = %q, want empty", id)
	}

	s.Append("sess-1", pipeline.SessionEvent{At: time.Now(), Direction: pipeline.Inbound, Phase: pipeline.SessionRequest})
	if id := s.ActiveSession(); id != "sess-1" {
		t.Errorf("ActiveSession() = %q, want %q", id, "sess-1")
	}

	s.Append("sess-2", pipeline.SessionEvent{At: time.Now(), Direction: pipeline.Inbound, Phase: pipeline.SessionRequest})
	if id := s.ActiveSession(); id != "sess-2" {
		t.Errorf("ActiveSession() = %q, want %q", id, "sess-2")
	}
}

func TestStore_MultipleSessions(t *testing.T) {
	s := New(5*time.Minute, 100, 0)

	s.Append("sess-1", pipeline.SessionEvent{At: time.Now(), Direction: pipeline.Inbound, Phase: pipeline.SessionRequest, A2A: &pipeline.A2AExtension{Method: "m1"}})
	s.Append("sess-2", pipeline.SessionEvent{At: time.Now(), Direction: pipeline.Inbound, Phase: pipeline.SessionRequest, A2A: &pipeline.A2AExtension{Method: "m2"}})

	v1 := s.View("sess-1")
	v2 := s.View("sess-2")
	if v1 == nil || v2 == nil {
		t.Fatal("expected both sessions to exist")
	}
	if len(v1.Events) != 1 || v1.Events[0].A2A.Method != "m1" {
		t.Errorf("sess-1 unexpected content: %+v", v1.Events)
	}
	if len(v2.Events) != 1 || v2.Events[0].A2A.Method != "m2" {
		t.Errorf("sess-2 unexpected content: %+v", v2.Events)
	}
}

func TestStore_TTLExpiry(t *testing.T) {
	s := New(50*time.Millisecond, 100, 0)

	s.Append("sess-1", pipeline.SessionEvent{At: time.Now(), Direction: pipeline.Inbound, Phase: pipeline.SessionRequest})

	v := s.View("sess-1")
	if v == nil {
		t.Fatal("expected View to return session before expiry")
	}

	time.Sleep(60 * time.Millisecond)

	v = s.View("sess-1")
	if v != nil {
		t.Error("expected View to return nil after TTL expiry")
	}

	if id := s.ActiveSession(); id != "" {
		t.Errorf("ActiveSession() = %q, want empty after expiry", id)
	}
}

func TestStore_MaxEvents(t *testing.T) {
	s := New(5*time.Minute, 3, 0)

	for i := 0; i < 5; i++ {
		s.Append("sess-1", pipeline.SessionEvent{
			At:        time.Now(),
			Direction: pipeline.Outbound,
			Phase:     pipeline.SessionRequest,
			MCP:       &pipeline.MCPExtension{Method: string(rune('a' + i))},
		})
	}

	v := s.View("sess-1")
	if v == nil {
		t.Fatal("View returned nil")
	}
	if len(v.Events) != 3 {
		t.Fatalf("Events len = %d, want 3 (capped at maxEvents)", len(v.Events))
	}
	if v.Events[0].MCP.Method != "c" {
		t.Errorf("Events[0].MCP.Method = %q, want %q (oldest evicted)", v.Events[0].MCP.Method, "c")
	}
}

func TestStore_ConcurrentAccess(t *testing.T) {
	s := New(5*time.Minute, 1000, 0)
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				s.Append("sess-concurrent", pipeline.SessionEvent{
					At:        time.Now(),
					Direction: pipeline.Outbound,
					Phase:     pipeline.SessionRequest,
					MCP:       &pipeline.MCPExtension{Method: "tools/call"},
				})
			}
		}()
	}

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				s.View("sess-concurrent")
				s.ActiveSession()
			}
		}()
	}

	wg.Wait()

	v := s.View("sess-concurrent")
	if v == nil {
		t.Fatal("expected session to exist after concurrent access")
	}
	if len(v.Events) != 1000 {
		t.Errorf("Events len = %d, want 1000", len(v.Events))
	}
}

func TestStore_Cleanup(t *testing.T) {
	s := New(50*time.Millisecond, 100, 0)

	s.Append("sess-old", pipeline.SessionEvent{At: time.Now(), Direction: pipeline.Inbound, Phase: pipeline.SessionRequest})
	time.Sleep(60 * time.Millisecond)

	s.Append("sess-new", pipeline.SessionEvent{At: time.Now(), Direction: pipeline.Inbound, Phase: pipeline.SessionRequest})

	if v := s.View("sess-old"); v != nil {
		t.Error("expected sess-old to be cleaned up")
	}
	if v := s.View("sess-new"); v == nil {
		t.Error("expected sess-new to still exist")
	}
}

func TestStore_ViewNonExistent(t *testing.T) {
	s := New(5*time.Minute, 100, 0)
	if v := s.View("does-not-exist"); v != nil {
		t.Error("expected nil for non-existent session")
	}
}

func TestView_Intents(t *testing.T) {
	v := &pipeline.SessionView{
		ID: "test",
		Events: []pipeline.SessionEvent{
			{Direction: pipeline.Inbound, Phase: pipeline.SessionRequest, A2A: &pipeline.A2AExtension{Method: "message/send"}},
			{Direction: pipeline.Outbound, Phase: pipeline.SessionRequest, MCP: &pipeline.MCPExtension{Method: "tools/call"}},
			{Direction: pipeline.Inbound, Phase: pipeline.SessionRequest, A2A: &pipeline.A2AExtension{Method: "message/send"}},
			{Direction: pipeline.Outbound, Phase: pipeline.SessionResponse, MCP: &pipeline.MCPExtension{Method: "tools/call"}},
		},
	}

	intents := v.Intents()
	if len(intents) != 2 {
		t.Fatalf("Intents() len = %d, want 2", len(intents))
	}
	for _, e := range intents {
		if e.Direction != pipeline.Inbound || e.Phase != pipeline.SessionRequest || e.A2A == nil {
			t.Errorf("unexpected intent event: %+v", e)
		}
	}
}

func TestView_ToolCalls(t *testing.T) {
	v := &pipeline.SessionView{
		ID: "test",
		Events: []pipeline.SessionEvent{
			{Direction: pipeline.Inbound, Phase: pipeline.SessionRequest, A2A: &pipeline.A2AExtension{}},
			{Direction: pipeline.Outbound, Phase: pipeline.SessionRequest, MCP: &pipeline.MCPExtension{Method: "tools/call"}},
			{Direction: pipeline.Outbound, Phase: pipeline.SessionResponse, MCP: &pipeline.MCPExtension{Method: "tools/call"}},
			{Direction: pipeline.Outbound, Phase: pipeline.SessionRequest, Inference: &pipeline.InferenceExtension{Model: "llama3.1"}},
		},
	}

	calls := v.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("ToolCalls() len = %d, want 1", len(calls))
	}
	if calls[0].MCP.Method != "tools/call" {
		t.Errorf("ToolCalls()[0].MCP.Method = %q, want %q", calls[0].MCP.Method, "tools/call")
	}
}

func TestView_ToolResponses(t *testing.T) {
	v := &pipeline.SessionView{
		ID: "test",
		Events: []pipeline.SessionEvent{
			{Direction: pipeline.Outbound, Phase: pipeline.SessionRequest, MCP: &pipeline.MCPExtension{Method: "tools/call"}},
			{Direction: pipeline.Outbound, Phase: pipeline.SessionResponse, MCP: &pipeline.MCPExtension{Method: "tools/call"}},
			{Direction: pipeline.Outbound, Phase: pipeline.SessionResponse, Inference: &pipeline.InferenceExtension{}},
		},
	}

	responses := v.ToolResponses()
	if len(responses) != 1 {
		t.Fatalf("ToolResponses() len = %d, want 1", len(responses))
	}
}

func TestView_LastIntent(t *testing.T) {
	v := &pipeline.SessionView{
		ID: "test",
		Events: []pipeline.SessionEvent{
			{Direction: pipeline.Inbound, Phase: pipeline.SessionRequest, A2A: &pipeline.A2AExtension{Method: "first"}},
			{Direction: pipeline.Outbound, Phase: pipeline.SessionRequest, MCP: &pipeline.MCPExtension{}},
			{Direction: pipeline.Inbound, Phase: pipeline.SessionRequest, A2A: &pipeline.A2AExtension{Method: "second"}},
		},
	}

	last := v.LastIntent()
	if last == nil {
		t.Fatal("LastIntent() returned nil")
	}
	if last.A2A.Method != "second" {
		t.Errorf("LastIntent().A2A.Method = %q, want %q", last.A2A.Method, "second")
	}
}

func TestView_LastIntent_Empty(t *testing.T) {
	v := &pipeline.SessionView{
		ID: "test",
		Events: []pipeline.SessionEvent{
			{Direction: pipeline.Outbound, Phase: pipeline.SessionRequest, MCP: &pipeline.MCPExtension{}},
		},
	}

	if last := v.LastIntent(); last != nil {
		t.Error("LastIntent() should be nil when no A2A inbound events exist")
	}
}

func TestStore_MaxSessionsEviction(t *testing.T) {
	s := New(5*time.Minute, 100, 2)
	defer s.Close()

	s.Append("sess-1", pipeline.SessionEvent{At: time.Now(), A2A: &pipeline.A2AExtension{Method: "m1"}})
	time.Sleep(time.Millisecond)
	s.Append("sess-2", pipeline.SessionEvent{At: time.Now(), A2A: &pipeline.A2AExtension{Method: "m2"}})
	time.Sleep(time.Millisecond)
	s.Append("sess-3", pipeline.SessionEvent{At: time.Now(), A2A: &pipeline.A2AExtension{Method: "m3"}})

	if v := s.View("sess-1"); v != nil {
		t.Error("sess-1 should have been evicted (oldest)")
	}
	if v := s.View("sess-2"); v == nil {
		t.Error("sess-2 should still exist")
	}
	if v := s.View("sess-3"); v == nil {
		t.Error("sess-3 should still exist")
	}
}

func TestStore_Close(t *testing.T) {
	s := New(50*time.Millisecond, 100, 0)
	s.Close()
	// Should not panic after close
	s.Append("sess", pipeline.SessionEvent{At: time.Now(), A2A: &pipeline.A2AExtension{Method: "m"}})
	if v := s.View("sess"); v == nil {
		t.Error("store should still function after Close (only background cleanup stops)")
	}
}

func TestView_InferenceRequests(t *testing.T) {
	v := &pipeline.SessionView{
		ID: "test",
		Events: []pipeline.SessionEvent{
			{Direction: pipeline.Outbound, Phase: pipeline.SessionRequest, Inference: &pipeline.InferenceExtension{Model: "llama3.1"}},
			{Direction: pipeline.Outbound, Phase: pipeline.SessionResponse, Inference: &pipeline.InferenceExtension{}},
			{Direction: pipeline.Outbound, Phase: pipeline.SessionRequest, MCP: &pipeline.MCPExtension{}},
		},
	}

	reqs := v.InferenceRequests()
	if len(reqs) != 1 {
		t.Fatalf("InferenceRequests() len = %d, want 1", len(reqs))
	}
	if reqs[0].Inference.Model != "llama3.1" {
		t.Errorf("InferenceRequests()[0].Model = %q, want %q", reqs[0].Inference.Model, "llama3.1")
	}
}

func TestStore_Rekey(t *testing.T) {
	s := New(5*time.Minute, 100, 0)

	ev1 := pipeline.SessionEvent{Direction: pipeline.Inbound, A2A: &pipeline.A2AExtension{Method: "message/send"}}
	ev2 := pipeline.SessionEvent{Direction: pipeline.Outbound, MCP: &pipeline.MCPExtension{Method: "tools/call"}}
	s.Append(DefaultSessionID, ev1)
	s.Append(DefaultSessionID, ev2)

	s.Rekey(DefaultSessionID, "ctx-abc")

	if v := s.View(DefaultSessionID); v != nil {
		t.Error("old session should be gone after rekey")
	}
	v := s.View("ctx-abc")
	if v == nil {
		t.Fatal("new session should hold the rekeyed events")
	}
	if len(v.Events) != 2 {
		t.Errorf("events len = %d, want 2", len(v.Events))
	}
	if id := s.ActiveSession(); id != "ctx-abc" {
		t.Errorf("ActiveSession = %q, want %q — activeID should follow rekey", id, "ctx-abc")
	}

	// Next turn appends to the real contextId and still sees the earlier events.
	s.Append("ctx-abc", pipeline.SessionEvent{Direction: pipeline.Inbound})
	if v := s.View("ctx-abc"); v == nil || len(v.Events) != 3 {
		t.Errorf("expected 3 events after second turn, got %v", v)
	}
}

func TestStore_Rekey_NoOpCases(t *testing.T) {
	s := New(5*time.Minute, 100, 0)
	s.Append("sess-a", pipeline.SessionEvent{Direction: pipeline.Inbound})

	// oldID absent: no-op.
	s.Rekey("missing", "sess-b")
	if v := s.View("sess-b"); v != nil {
		t.Error("rekey of missing oldID should not create newID")
	}

	// newID already exists: preserve it, do not clobber.
	s.Append("sess-b", pipeline.SessionEvent{Direction: pipeline.Outbound})
	s.Rekey("sess-a", "sess-b")
	if v := s.View("sess-a"); v == nil {
		t.Error("sess-a should still exist because sess-b was already taken")
	}
	vb := s.View("sess-b")
	if vb == nil || len(vb.Events) != 1 {
		t.Errorf("sess-b should be preserved with its own event, got %v", vb)
	}

	// Empty IDs: no-op, no panic.
	s.Rekey("", "x")
	s.Rekey("x", "")
	s.Rekey("sess-a", "sess-a")
}

func TestStore_Rekey_ActiveIDUntouchedWhenNotOldID(t *testing.T) {
	s := New(5*time.Minute, 100, 0)
	s.Append("sess-a", pipeline.SessionEvent{})
	s.Append("sess-b", pipeline.SessionEvent{}) // activeID is now sess-b

	s.Rekey("sess-a", "sess-a-renamed")

	if id := s.ActiveSession(); id != "sess-b" {
		t.Errorf("ActiveSession = %q, want sess-b (unchanged — rekey did not touch active)", id)
	}
	if v := s.View("sess-a-renamed"); v == nil {
		t.Error("sess-a-renamed should exist")
	}
}

func TestView_FailedEvents(t *testing.T) {
	s := New(5*time.Minute, 100, 0)
	defer s.Close()

	s.Append("sess", pipeline.SessionEvent{Phase: pipeline.SessionRequest, A2A: &pipeline.A2AExtension{}})
	s.Append("sess", pipeline.SessionEvent{Phase: pipeline.SessionResponse, StatusCode: 200})
	s.Append("sess", pipeline.SessionEvent{
		Phase:      pipeline.SessionResponse,
		StatusCode: 503,
		Error:      &pipeline.EventError{Kind: "backend_error", Code: "503"},
	})
	s.Append("sess", pipeline.SessionEvent{
		Phase: pipeline.SessionResponse,
		Error: &pipeline.EventError{Kind: "blocked", Message: "pii"},
	})

	v := s.View("sess")
	if v == nil {
		t.Fatal("View returned nil")
	}

	failed := v.FailedEvents()
	if len(failed) != 2 {
		t.Fatalf("FailedEvents len = %d, want 2", len(failed))
	}
	if failed[0].Error.Kind != "backend_error" {
		t.Errorf("first failed Kind = %q", failed[0].Error.Kind)
	}

	last := v.LastError()
	if last == nil || last.Error.Kind != "blocked" {
		t.Errorf("LastError = %+v, want blocked", last)
	}
}

func TestView_LastError_NoErrors(t *testing.T) {
	s := New(5*time.Minute, 100, 0)
	defer s.Close()
	s.Append("sess", pipeline.SessionEvent{Phase: pipeline.SessionResponse, StatusCode: 200})
	v := s.View("sess")
	if v.LastError() != nil {
		t.Error("LastError should be nil when no errors present")
	}
	if v.FailedEvents() != nil {
		t.Error("FailedEvents should be nil when no errors present")
	}
}

func TestStore_Subscribe_ReceivesAppendedEvents(t *testing.T) {
	s := New(5*time.Minute, 100, 0)
	defer s.Close()

	sub, cancel := s.Subscribe()
	defer cancel()

	s.Append("sess", pipeline.SessionEvent{
		A2A: &pipeline.A2AExtension{Method: "message/send"},
	})

	select {
	case e := <-sub.Events():
		if e.A2A == nil || e.A2A.Method != "message/send" {
			t.Errorf("received wrong event: %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no event received within 1s")
	}
}

func TestStore_Subscribe_CancelStopsDelivery(t *testing.T) {
	s := New(5*time.Minute, 100, 0)
	defer s.Close()

	sub, cancel := s.Subscribe()
	cancel()

	s.Append("sess", pipeline.SessionEvent{A2A: &pipeline.A2AExtension{}})

	// Channel should be closed; receive returns zero value with ok=false.
	select {
	case _, ok := <-sub.Events():
		if ok {
			t.Error("received event after cancel")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("channel never closed after cancel")
	}
}

func TestStore_Subscribe_SlowConsumerDropsWithoutBlocking(t *testing.T) {
	s := New(5*time.Minute, 10000, 0)
	defer s.Close()

	sub, cancel := s.Subscribe()
	defer cancel()

	// Flood well beyond buffer capacity without consuming — must not block Append.
	total := subscriberChanBuf + 50
	done := make(chan struct{})
	go func() {
		for i := 0; i < total; i++ {
			s.Append("sess", pipeline.SessionEvent{})
		}
		close(done)
	}()

	select {
	case <-done:
		// Good — Append did not block.
	case <-time.After(time.Second):
		t.Fatal("Append blocked on slow consumer")
	}

	if drops := sub.Drops(); drops == 0 {
		t.Errorf("expected some drops, got 0")
	}
}

func TestStore_Subscribe_FanOutToMultipleSubscribers(t *testing.T) {
	s := New(5*time.Minute, 100, 0)
	defer s.Close()

	a, cancelA := s.Subscribe()
	defer cancelA()
	b, cancelB := s.Subscribe()
	defer cancelB()

	s.Append("sess", pipeline.SessionEvent{A2A: &pipeline.A2AExtension{Method: "ping"}})

	gotA := waitEvent(t, a.Events(), time.Second)
	gotB := waitEvent(t, b.Events(), time.Second)
	if gotA.A2A == nil || gotA.A2A.Method != "ping" {
		t.Errorf("A received wrong event: %+v", gotA)
	}
	if gotB.A2A == nil || gotB.A2A.Method != "ping" {
		t.Errorf("B received wrong event: %+v", gotB)
	}
}

func TestStore_Subscribe_CloseStoreUnblocksSubscribers(t *testing.T) {
	s := New(5*time.Minute, 100, 0)
	sub, cancel := s.Subscribe()
	defer cancel()

	s.Close() // must close subscriber channels, not just the cleanup goroutine

	select {
	case _, ok := <-sub.Events():
		if ok {
			t.Error("expected closed channel after Store.Close")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Store.Close did not close subscriber channel")
	}
}

func TestStore_Append_StampsSessionID(t *testing.T) {
	// Consumers (snapshot or stream) need to attribute events — especially
	// outbound ones with no protocol-native session field — to a bucket.
	s := New(5*time.Minute, 100, 0)
	defer s.Close()

	s.Append("bucket-42", pipeline.SessionEvent{MCP: &pipeline.MCPExtension{Method: "tools/call"}})

	v := s.View("bucket-42")
	if v == nil || len(v.Events) != 1 {
		t.Fatalf("expected 1 event, got %+v", v)
	}
	if v.Events[0].SessionID != "bucket-42" {
		t.Errorf("SessionID = %q, want %q", v.Events[0].SessionID, "bucket-42")
	}
}

func TestStore_Rekey_RewritesExistingEventSessionIDs(t *testing.T) {
	// After Rekey, snapshot reads of the new key must show the original
	// events with SessionID updated to the new bucket ID — otherwise
	// downstream consumers see a mismatch between the session's outer ID
	// and each event's inner SessionID.
	s := New(5*time.Minute, 100, 0)
	defer s.Close()

	s.Append(DefaultSessionID, pipeline.SessionEvent{A2A: &pipeline.A2AExtension{}})
	s.Append(DefaultSessionID, pipeline.SessionEvent{MCP: &pipeline.MCPExtension{}})
	s.Rekey(DefaultSessionID, "ctx-abc")

	v := s.View("ctx-abc")
	if v == nil || len(v.Events) != 2 {
		t.Fatalf("expected 2 events under new key, got %+v", v)
	}
	for i, e := range v.Events {
		if e.SessionID != "ctx-abc" {
			t.Errorf("events[%d].SessionID = %q, want %q", i, e.SessionID, "ctx-abc")
		}
	}
}

// TestSumTokens verifies that SessionSummary.TotalTokens aggregates only
// over inference response events. Request events, non-inference responses,
// and events without Inference populated must not contribute.
func TestSumTokens(t *testing.T) {
	evs := []pipeline.SessionEvent{
		// Inference response — counted.
		{
			Phase:     pipeline.SessionResponse,
			Inference: &pipeline.InferenceExtension{TotalTokens: 100},
		},
		// Inference request (has tokens field set, unusual, but must be skipped
		// because request-phase token counts aren't usage).
		{
			Phase:     pipeline.SessionRequest,
			Inference: &pipeline.InferenceExtension{TotalTokens: 99},
		},
		// MCP response — no inference, skipped.
		{
			Phase: pipeline.SessionResponse,
			MCP:   &pipeline.MCPExtension{Method: "tools/call"},
		},
		// Inference response with zero tokens — contributes zero.
		{
			Phase:     pipeline.SessionResponse,
			Inference: &pipeline.InferenceExtension{TotalTokens: 0},
		},
		// Second inference response — counted.
		{
			Phase:     pipeline.SessionResponse,
			Inference: &pipeline.InferenceExtension{TotalTokens: 250},
		},
	}
	got := sumTokens(evs)
	if got != 350 {
		t.Errorf("sumTokens = %d, want 350", got)
	}
	if sumTokens(nil) != 0 {
		t.Error("sumTokens(nil) should be 0")
	}
	if sumTokens([]pipeline.SessionEvent{}) != 0 {
		t.Error("sumTokens([]) should be 0")
	}
}

// costRecord builds the plugin map one session event carries its cost record in.
func costRecord(t *testing.T, ev costevent.Event) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]json.RawMessage{costevent.Key: raw}
}

// TestSumCost covers the four rules sumCost applies, which sumTokens beside it does not have
// to: pricedness gates the money and not the saving, and a denial can carry either.
//
// One event list rather than four, because the rules interact — the point is what a session
// holding a realistic mixture reports, and a per-rule fixture would pass while the sum over
// all of them was wrong.
func TestSumCost(t *testing.T) {
	evs := []pipeline.SessionEvent{
		// An ordinary priced response with a saving on it. Both figures counted.
		{
			Phase: pipeline.SessionResponse,
			Plugins: costRecord(t, costevent.Event{CostUSD: 0.25, Settled: true, Provenance: "configured",
				Avoided: []costevent.Saving{{Component: "tool-prune", TokensAvoided: 100, USD: 0.01, Tier: "input"}}}),
		},
		// UNPRICED, and carrying a saving. No dollars, and the saving still counts: the
		// prompt was pruned whether or not anything managed to price the response.
		{
			Phase: pipeline.SessionResponse,
			Plugins: costRecord(t, costevent.Event{Source: costevent.SourceUsageFallback,
				Avoided: []costevent.Saving{{Component: "tool-prune", TokensAvoided: 200, USD: 0.02, Tier: "input"}}}),
		},
		// A DENIAL carrying a settled figure — the proxy charges for a response whose body
		// never arrived. Counted, which is why this is not restricted to SessionResponse.
		{
			Phase:   pipeline.SessionDenied,
			Plugins: costRecord(t, costevent.Event{CostUSD: 0.05, Settled: true, Provenance: "authoritative"}),
		},
		// A REQUEST-phase record. Skipped: the cost is settled on the response pass, and
		// counting both halves would double-charge every request that has one.
		{
			Phase:   pipeline.SessionRequest,
			Plugins: costRecord(t, costevent.Event{CostUSD: 99, Settled: true, Provenance: "configured"}),
		},
		// A PROJECTED saving: observe mode left every byte on the wire, so this is money
		// that WAS spent and must not be reported as avoided.
		{
			Phase: pipeline.SessionResponse,
			Plugins: costRecord(t, costevent.Event{Source: costevent.SourceUsageFallback,
				Avoided: []costevent.Saving{{Component: "tool-prune", TokensAvoided: 5000, USD: 5, Tier: "input", Projected: true}}}),
		},
		// A response with no cost record at all — the common case for non-inference traffic.
		{Phase: pipeline.SessionResponse, MCP: &pipeline.MCPExtension{Method: "tools/call"}},
	}

	cost, avoided := sumCost(evs)
	// 0.25 + 0.05. The request event's $99 is the discriminator: 99_300_000 means the
	// request half was counted too.
	if want := int64(300_000); cost != want {
		t.Errorf("cost = %d, want %d", cost, want)
	}
	// 0.01 + 0.02, the priced and the unpriced request alike. 30_000 with the $5 projected
	// entry excluded — 5_030_000 would mean observe-mode figures are being reported as
	// savings.
	if want := int64(30_000); avoided != want {
		t.Errorf("avoided = %d, want %d", avoided, want)
	}

	if c, a := sumCost(nil); c != 0 || a != 0 {
		t.Errorf("sumCost(nil) = %d, %d; want 0, 0", c, a)
	}
}

// A lifetime total must not WRAP, which is the one failure mode that turns a cost column into
// a negative number an operator cannot explain. A gateway's own figure is bounded per request
// and nothing bounds how many requests a session holds, so the ceiling is reachable in
// principle and the clamp is what makes the column safe.
//
// Two maximal figures, which is the smallest case that overflows.
func TestSumCost_SaturatesRatherThanWrapping(t *testing.T) {
	// Just under pricing.MaxCostMicros in dollars, so each record prices at close to the
	// largest figure costevent will represent. Two of them exceed int64 nowhere near, so the
	// list is padded to reach the ceiling.
	big := costevent.Event{CostUSD: 9e9, Settled: true, Provenance: "authoritative"}
	one := costRecord(t, big)
	evs := make([]pipeline.SessionEvent, 4096)
	for i := range evs {
		evs[i] = pipeline.SessionEvent{Phase: pipeline.SessionResponse, Plugins: one}
	}

	cost, _ := sumCost(evs)
	if cost < 0 {
		t.Errorf("cost = %d: a lifetime total wrapped negative, which is the one answer a "+
			"cost column must never print", cost)
	}
	if cost != math.MaxInt64 {
		t.Errorf("cost = %d, want the int64 ceiling: the sum should clamp there", cost)
	}
}

// TestListSessions_TotalTokens verifies the aggregate lands on the summary.
func TestListSessions_TotalTokens(t *testing.T) {
	s := New(5*time.Minute, 100, 0)
	s.Append("sess-a", pipeline.SessionEvent{
		Phase:     pipeline.SessionResponse,
		Inference: &pipeline.InferenceExtension{TotalTokens: 42},
	})
	s.Append("sess-a", pipeline.SessionEvent{
		Phase: pipeline.SessionResponse,
		MCP:   &pipeline.MCPExtension{Method: "tools/call"},
	})
	s.Append("sess-b", pipeline.SessionEvent{
		Phase:     pipeline.SessionResponse,
		Inference: &pipeline.InferenceExtension{TotalTokens: 17},
	})
	sums := s.ListSessions()
	if len(sums) != 2 {
		t.Fatalf("expected 2 summaries, got %d", len(sums))
	}
	byID := map[string]int{}
	for _, sum := range sums {
		byID[sum.ID] = sum.TotalTokens
	}
	if byID["sess-a"] != 42 {
		t.Errorf("sess-a TotalTokens = %d, want 42", byID["sess-a"])
	}
	if byID["sess-b"] != 17 {
		t.Errorf("sess-b TotalTokens = %d, want 17", byID["sess-b"])
	}
}

func waitEvent(t *testing.T, ch <-chan pipeline.SessionEvent, d time.Duration) pipeline.SessionEvent {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(d):
		t.Fatalf("timeout waiting for event after %v", d)
		return pipeline.SessionEvent{}
	}
}
