package forwardproxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rossoctl/cortex/authbridge/authlib/pipeline"
	"github.com/rossoctl/cortex/authbridge/authlib/session"
)

// TestResolveOutboundSessionID_HeaderWinsOverActiveSession is the core of
// per-session bucketing: two coding-agent sessions running at once must each
// resolve to their own bucket. ActiveSession() cannot do this — it returns one
// global "most recently updated" id, so whichever session spoke last would
// swallow the other's events. A request carrying a client session header must
// resolve to that id regardless of who spoke last.
func TestResolveOutboundSessionID_HeaderWinsOverActiveSession(t *testing.T) {
	store := session.New(5*time.Minute, 100, 0)
	defer store.Close()

	const sidA = "511754a6-63e2-47df-bb22-706dc165c344"
	const sidB = "f4019b9f-3550-442f-9bc7-229809a3b486"

	// Session A spoke most recently, so ActiveSession() points at it.
	store.Append(sidA, pipeline.SessionEvent{
		At:        time.Now(),
		Direction: pipeline.Outbound,
		Phase:     pipeline.SessionRequest,
	})
	if got := store.ActiveSession(); got != sidA {
		t.Fatalf("precondition: ActiveSession() = %q, want %q", got, sidA)
	}

	s := &Server{Sessions: store, SessionIDHeaders: []string{session.ClaudeCodeSessionHeader}}

	// A request from the OTHER concurrent session must not be filed under A.
	pctx := &pipeline.Context{
		Direction: pipeline.Outbound,
		Host:      "ete-litellm.example",
		Headers:   http.Header{session.ClaudeCodeSessionHeader: []string{sidB}},
	}

	if got := s.resolveOutboundSessionID(pctx); got != sidB {
		t.Fatalf("resolveOutboundSessionID() = %q, want %q (header must win over ActiveSession)", got, sidB)
	}
}

// TestForwardProxy_BucketsConcurrentSessionsByHeader is the end-to-end proof
// that the resolver is actually wired into request recording: two proxied
// requests carrying different Claude Code session ids must produce two
// separate buckets, each holding its own request AND its paired response.
// Before this change both landed in "default" — one bucket, interleaved, with
// per-session cost uncomputable.
func TestForwardProxy_BucketsConcurrentSessionsByHeader(t *testing.T) {
	store := session.New(5*time.Minute, 100, 0)
	defer store.Close()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	p, err := pipeline.New([]pipeline.Plugin{})
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{
		OutboundPipeline: pipeline.NewHolder(p),
		Sessions:         store,
		Client:           http.DefaultClient,
		SessionIDHeaders: []string{session.ClaudeCodeSessionHeader},
	}
	proxy := httptest.NewServer(srv.Handler())
	defer proxy.Close()
	proxyClient := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(mustParseURL(proxy.URL))}}

	const sidA = "511754a6-63e2-47df-bb22-706dc165c344"
	const sidB = "f4019b9f-3550-442f-9bc7-229809a3b486"
	for _, sid := range []string{sidA, sidB} {
		req, _ := http.NewRequest("POST", backend.URL+"/v1/messages", strings.NewReader(`{}`))
		req.Header.Set(session.ClaudeCodeSessionHeader, sid)
		resp, err := proxyClient.Do(req)
		if err != nil {
			t.Fatalf("request for %s failed: %v", sid, err)
		}
		_ = resp.Body.Close()
	}

	for _, sid := range []string{sidA, sidB} {
		v := store.View(sid)
		if v == nil {
			t.Fatalf("no bucket for session %s — events did not get attributed", sid)
		}
		if len(v.Events) != 2 {
			t.Fatalf("bucket %s has %d events, want 2 (request + response)", sid, len(v.Events))
		}
		if v.Events[0].Phase != pipeline.SessionRequest || v.Events[1].Phase != pipeline.SessionResponse {
			t.Errorf("bucket %s phases = %v/%v, want request/response",
				sid, v.Events[0].Phase, v.Events[1].Phase)
		}
	}
	// Nothing should have leaked into the shared bucket.
	if v := store.View(session.DefaultSessionID); v != nil && len(v.Events) != 0 {
		t.Errorf("default bucket got %d events, want 0 (all traffic was attributable)", len(v.Events))
	}
}

// TestResolveOutboundSessionID_RejectsControlCharacters guards the one way a
// client-supplied bucket key can do damage: the id is rendered in abctl's TUI,
// written to structured logs, and echoed in /v1/sessions JSON, so a value
// carrying a newline or an ANSI escape is a log- and terminal-injection vector.
// Such a value must be refused outright and bucketing must degrade to the
// previous behavior, never record under the attacker's string.
func TestResolveOutboundSessionID_RejectsControlCharacters(t *testing.T) {
	store := session.New(5*time.Minute, 100, 0)
	defer store.Close()
	s := &Server{Sessions: store, SessionIDHeaders: []string{session.ClaudeCodeSessionHeader}}

	for _, bad := range []string{
		"abc\ndef",      // log injection: forges a second log line
		"abc\x1b[2Jdef", // terminal injection: clears the operator's screen
		"abc\rdef",      // carriage return
		"abc\x00def",    // NUL
	} {
		pctx := &pipeline.Context{
			Direction: pipeline.Outbound,
			Headers:   http.Header{session.ClaudeCodeSessionHeader: []string{bad}},
		}
		if got := s.resolveOutboundSessionID(pctx); got != session.DefaultSessionID {
			t.Errorf("resolveOutboundSessionID(%q) = %q, want %q (must refuse control characters)",
				bad, got, session.DefaultSessionID)
		}
	}
}

// TestResolveOutboundSessionID_RejectsOverlongID refuses an id longer than the
// store will keep intact rather than letting it be truncated. Store.Append
// truncates at MaxSessionIDLen, so two overlong ids sharing a prefix would
// silently merge into one bucket — the exact confusion this feature exists to
// remove. Falling back is the documented degradation; a half-key is not.
func TestResolveOutboundSessionID_RejectsOverlongID(t *testing.T) {
	store := session.New(5*time.Minute, 100, 0)
	defer store.Close()
	s := &Server{Sessions: store, SessionIDHeaders: []string{session.ClaudeCodeSessionHeader}}

	overlong := strings.Repeat("a", session.MaxSessionIDLen+1)
	pctx := &pipeline.Context{
		Direction: pipeline.Outbound,
		Headers:   http.Header{session.ClaudeCodeSessionHeader: []string{overlong}},
	}
	if got := s.resolveOutboundSessionID(pctx); got != session.DefaultSessionID {
		t.Fatalf("resolveOutboundSessionID(len %d) = %q, want %q",
			len(overlong), got, session.DefaultSessionID)
	}
}

// TestResolveOutboundSessionID_FallsBackWhenHeaderAbsent is a characterization
// test, not a driven one: it pins the pre-existing bucketing so this change
// stays additive. Header-less traffic on this path (an agent's outbound call
// caused by an inbound A2A turn, a health probe, curl) must bucket exactly as
// it did before.
func TestResolveOutboundSessionID_FallsBackWhenHeaderAbsent(t *testing.T) {
	store := session.New(5*time.Minute, 100, 0)
	defer store.Close()
	store.Append("conv-A", pipeline.SessionEvent{
		At:        time.Now(),
		Direction: pipeline.Inbound,
		Phase:     pipeline.SessionRequest,
		A2A:       &pipeline.A2AExtension{Method: "message/stream", SessionID: "conv-A"},
	})
	s := &Server{Sessions: store, SessionIDHeaders: []string{session.ClaudeCodeSessionHeader}}

	pctx := &pipeline.Context{Direction: pipeline.Outbound, Headers: http.Header{}}
	if got := s.resolveOutboundSessionID(pctx); got != "conv-A" {
		t.Fatalf("resolveOutboundSessionID() = %q, want %q (A2A correlation must survive)", got, "conv-A")
	}

	// With nothing active either, the default bucket still catches it.
	empty := session.New(5*time.Minute, 100, 0)
	defer empty.Close()
	s2 := &Server{Sessions: empty, SessionIDHeaders: []string{session.ClaudeCodeSessionHeader}}
	if got := s2.resolveOutboundSessionID(pctx); got != session.DefaultSessionID {
		t.Fatalf("resolveOutboundSessionID() = %q, want %q", got, session.DefaultSessionID)
	}
}

// TestResolveOutboundSessionID_DisabledByEmptyHeaderList confirms the off
// switch: with no headers configured, a request carrying one is ignored.
func TestResolveOutboundSessionID_DisabledByEmptyHeaderList(t *testing.T) {
	store := session.New(5*time.Minute, 100, 0)
	defer store.Close()
	s := &Server{Sessions: store, SessionIDHeaders: nil}

	pctx := &pipeline.Context{
		Direction: pipeline.Outbound,
		Headers:   http.Header{session.ClaudeCodeSessionHeader: []string{"some-session"}},
	}
	if got := s.resolveOutboundSessionID(pctx); got != session.DefaultSessionID {
		t.Fatalf("resolveOutboundSessionID() = %q, want %q (header bucketing disabled)", got, session.DefaultSessionID)
	}
}
