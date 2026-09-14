package forwardproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rossoctl/cortex/authbridge/authlib/pipeline"
	"github.com/rossoctl/cortex/authbridge/authlib/session"
)

// identityProbePlugin records the session identity the pipeline handed it, which
// is what every session-aware plugin keys on (sessionbudget's Redis counters,
// contextguru's compaction state, sparc). Capturing it in a plugin rather than
// asserting on the store is deliberate: the store is the recording side, and the
// whole point of these tests is that the two sides agree.
type identityProbePlugin struct {
	sawNil    []bool
	sawID     []string
	sawEvents []int
}

func (p *identityProbePlugin) Name() string { return "identity-probe" }
func (p *identityProbePlugin) Capabilities() pipeline.PluginCapabilities {
	return pipeline.PluginCapabilities{}
}
func (p *identityProbePlugin) OnRequest(_ context.Context, pctx *pipeline.Context) pipeline.Action {
	if pctx.Session == nil {
		p.sawNil = append(p.sawNil, true)
		p.sawID = append(p.sawID, "")
		p.sawEvents = append(p.sawEvents, -1)
		return pipeline.Action{Type: pipeline.Continue}
	}
	p.sawNil = append(p.sawNil, false)
	p.sawID = append(p.sawID, pctx.Session.ID)
	p.sawEvents = append(p.sawEvents, len(pctx.Session.Events))
	return pipeline.Action{Type: pipeline.Continue}
}
func (p *identityProbePlugin) OnResponse(_ context.Context, _ *pipeline.Context) pipeline.Action {
	return pipeline.Action{Type: pipeline.Continue}
}

// newProbedProxy wires a forward proxy whose only plugin is the identity probe,
// with header bucketing on, and returns the probe plus a client that proxies
// through it.
func newProbedProxy(t *testing.T, store *session.Store) (*identityProbePlugin, *http.Client, string) {
	t.Helper()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)

	probe := &identityProbePlugin{}
	p, err := pipeline.New([]pipeline.Plugin{probe})
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
	t.Cleanup(proxy.Close)

	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(mustParseURL(proxy.URL))}}
	return probe, client, backend.URL
}

// get issues a proxied request, optionally carrying a client session header.
func get(t *testing.T, client *http.Client, backendURL, sid string) {
	t.Helper()
	req, _ := http.NewRequest("POST", backendURL+"/v1/messages", strings.NewReader(`{}`))
	if sid != "" {
		req.Header.Set(session.ClaudeCodeSessionHeader, sid)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	_ = resp.Body.Close()
}

// TestPluginIdentity_FollowsClientSessionNotActiveSession is the core of #984.
// Event recording already resolves the client's session header, but plugins were
// handed ActiveSession() — a single global "most recently updated" id. So with
// two coding-agent sessions in flight, telemetry bucketed correctly while every
// plugin attributed both to whichever spoke last. Two sequential requests are
// enough to show it: after the first, ActiveSession() points at session A, so the
// second must still be told it is session B.
func TestPluginIdentity_FollowsClientSessionNotActiveSession(t *testing.T) {
	store := session.New(5*time.Minute, 100, 0)
	defer store.Close()
	probe, client, backendURL := newProbedProxy(t, store)

	const sidA = "511754a6-63e2-47df-bb22-706dc165c344"
	const sidB = "f4019b9f-3550-442f-9bc7-229809a3b486"

	get(t, client, backendURL, sidA)
	if got := store.ActiveSession(); got != sidA {
		t.Fatalf("precondition: ActiveSession() = %q, want %q", got, sidA)
	}
	get(t, client, backendURL, sidB)

	if len(probe.sawID) != 2 {
		t.Fatalf("probe ran %d times, want 2", len(probe.sawID))
	}
	if probe.sawID[0] != sidA {
		t.Errorf("first request: plugin saw session %q, want %q", probe.sawID[0], sidA)
	}
	if probe.sawID[1] != sidB {
		t.Errorf("second request: plugin saw session %q, want %q — ActiveSession() was %q, which is the bug",
			probe.sawID[1], sidB, sidA)
	}
}

// TestPluginIdentity_AgreesWithRecordedBucket pins the invariant that makes the
// two sides one identity rather than two rules that happen to coincide: whatever
// the plugin was told is where the event was filed.
func TestPluginIdentity_AgreesWithRecordedBucket(t *testing.T) {
	store := session.New(5*time.Minute, 100, 0)
	defer store.Close()
	probe, client, backendURL := newProbedProxy(t, store)

	const sid = "511754a6-63e2-47df-bb22-706dc165c344"
	get(t, client, backendURL, sid)

	if len(probe.sawID) != 1 {
		t.Fatalf("probe ran %d times, want 1", len(probe.sawID))
	}
	pluginSaw := probe.sawID[0]
	v := store.View(pluginSaw)
	if v == nil {
		t.Fatalf("plugin was told session %q but no such bucket was recorded", pluginSaw)
	}
	if len(v.Events) == 0 {
		t.Fatalf("bucket %q has no events; the plugin and the recorder disagree", pluginSaw)
	}
}

// TestPluginIdentity_FirstTurnHasIdentityBeforeAnyEvent covers the ordering trap.
// Hydration runs BEFORE recording, so on a session's first request the bucket
// does not exist yet and Store.View returns nil. Plugins must still be told which
// session this is — sessionbudget skips enforcement entirely on an empty id, so a
// nil view on turn one would let every new session's first call through
// unmetered.
func TestPluginIdentity_FirstTurnHasIdentityBeforeAnyEvent(t *testing.T) {
	store := session.New(5*time.Minute, 100, 0)
	defer store.Close()
	probe, client, backendURL := newProbedProxy(t, store)

	const sid = "511754a6-63e2-47df-bb22-706dc165c344"
	get(t, client, backendURL, sid)

	if len(probe.sawID) != 1 {
		t.Fatalf("probe ran %d times, want 1", len(probe.sawID))
	}
	if probe.sawNil[0] {
		t.Fatalf("plugin saw a nil session on the first turn; it must know the id before any event exists")
	}
	if probe.sawID[0] != sid {
		t.Errorf("plugin saw %q, want %q", probe.sawID[0], sid)
	}
	if probe.sawEvents[0] != 0 {
		t.Errorf("first-turn view has %d events, want 0 — an empty view is the truthful answer",
			probe.sawEvents[0])
	}
}

// TestPluginIdentity_NoHeaderKeepsActiveSession is a characterization test, not a
// driven one. It pins the fallback that makes this change safe in-cluster: an A2A
// agent sends no coding-agent header, so plugins must still receive the session
// ActiveSession() names — the inbound turn that caused the outbound call. IBAC
// and sparc depend on that correlation.
func TestPluginIdentity_NoHeaderKeepsActiveSession(t *testing.T) {
	store := session.New(5*time.Minute, 100, 0)
	defer store.Close()
	store.Append("conv-A", pipeline.SessionEvent{
		At:        time.Now(),
		Direction: pipeline.Inbound,
		Phase:     pipeline.SessionRequest,
		A2A:       &pipeline.A2AExtension{Method: "message/send", SessionID: "conv-A"},
	})
	probe, client, backendURL := newProbedProxy(t, store)

	get(t, client, backendURL, "") // no session header

	if len(probe.sawID) != 1 {
		t.Fatalf("probe ran %d times, want 1", len(probe.sawID))
	}
	if probe.sawID[0] != "conv-A" {
		t.Errorf("plugin saw %q, want %q (A2A correlation must survive)", probe.sawID[0], "conv-A")
	}
	if probe.sawEvents[0] != 1 {
		t.Errorf("view has %d events, want 1 (the inbound A2A turn)", probe.sawEvents[0])
	}
}

// TestPluginIdentity_NoSessionAtAllStaysNil pins the other half of the fallback:
// with no header AND nothing active, plugins keep receiving nil rather than being
// handed the default bucket. Handing them "default" would silently satisfy
// sessionbudget's DefaultSessionFallback — an opt-in config — and start enforcing
// budgets where today it deliberately skips.
func TestPluginIdentity_NoSessionAtAllStaysNil(t *testing.T) {
	store := session.New(5*time.Minute, 100, 0)
	defer store.Close()
	probe, client, backendURL := newProbedProxy(t, store)

	get(t, client, backendURL, "")

	if len(probe.sawNil) != 1 {
		t.Fatalf("probe ran %d times, want 1", len(probe.sawNil))
	}
	if !probe.sawNil[0] {
		t.Errorf("plugin saw session %q, want nil (no header, nothing active)", probe.sawID[0])
	}
}
