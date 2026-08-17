package sessionbudget

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rossoctl/cortex/authbridge/authlib/pipeline"
	"github.com/rossoctl/cortex/authbridge/authlib/storage"
)

// memStore is a minimal in-memory storage.Store for testing.
type memStore struct {
	mu     sync.Mutex
	hashes map[string]map[string]string
	kvs    map[string]string
	ttls   map[string]time.Duration
}

func newMemStore() *memStore {
	return &memStore{
		hashes: make(map[string]map[string]string),
		kvs:    make(map[string]string),
		ttls:   make(map[string]time.Duration),
	}
}

func (m *memStore) Get(_ context.Context, key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.kvs[key], nil
}

func (m *memStore) Set(_ context.Context, key, value string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.kvs[key] = value
	if ttl > 0 {
		m.ttls[key] = ttl
	}
	return nil
}

func (m *memStore) Incr(_ context.Context, key string, delta int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var cur int64
	if v, ok := m.kvs[key]; ok {
		fmt.Sscanf(v, "%d", &cur)
	}
	cur += delta
	m.kvs[key] = fmt.Sprintf("%d", cur)
	return cur, nil
}

func (m *memStore) HashIncr(_ context.Context, key, field string, delta int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hashes[key] == nil {
		m.hashes[key] = make(map[string]string)
	}
	var cur int64
	if v, ok := m.hashes[key][field]; ok {
		fmt.Sscanf(v, "%d", &cur)
	}
	cur += delta
	m.hashes[key][field] = fmt.Sprintf("%d", cur)
	return cur, nil
}

func (m *memStore) HashGet(_ context.Context, key string) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.hashes[key]
	if h == nil {
		return map[string]string{}, nil
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		out[k] = v
	}
	return out, nil
}

func (m *memStore) HashSetNX(_ context.Context, key, field, value string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hashes[key] == nil {
		m.hashes[key] = make(map[string]string)
	}
	if _, exists := m.hashes[key][field]; exists {
		return false, nil
	}
	m.hashes[key][field] = value
	return true, nil
}

func (m *memStore) Expire(_ context.Context, key string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ttls[key] = ttl
	return nil
}

func (m *memStore) Close() error { return nil }

var _ storage.Store = (*memStore)(nil)

// failingStore always returns errors (simulates total store unavailability).
type failingStore struct{}

func (failingStore) Get(context.Context, string) (string, error)              { return "", context.DeadlineExceeded }
func (failingStore) Set(context.Context, string, string, time.Duration) error { return context.DeadlineExceeded }
func (failingStore) Incr(context.Context, string, int64) (int64, error)       { return 0, context.DeadlineExceeded }
func (failingStore) HashIncr(context.Context, string, string, int64) (int64, error) { return 0, context.DeadlineExceeded }
func (failingStore) HashGet(context.Context, string) (map[string]string, error) { return nil, context.DeadlineExceeded }
func (failingStore) HashSetNX(context.Context, string, string, string) (bool, error) { return false, context.DeadlineExceeded }
func (failingStore) Expire(context.Context, string, time.Duration) error { return context.DeadlineExceeded }
func (failingStore) Close() error { return nil }

func init() {
	storage.Register("mem", func(_ string) (storage.Store, error) {
		return newMemStore(), nil
	})
}

func newTestPlugin(maxTokens, maxCalls, maxDuration int64) *SessionBudget {
	p := New()
	cfg := fmt.Sprintf(`{
		"redis_url": "mem://test",
		"max_tokens": %d,
		"max_calls": %d,
		"max_duration_seconds": %d,
		"refresh_interval": "100ms"
	}`, maxTokens, maxCalls, maxDuration)
	if err := p.Configure(json.RawMessage(cfg)); err != nil {
		panic(err)
	}
	store := newMemStore()
	p.store = store
	return p
}

func makePctx(sessionID string, totalTokens int) *pipeline.Context {
	pctx := &pipeline.Context{
		Direction: pipeline.Outbound,
		Headers:   http.Header{},
		Session:   &pipeline.SessionView{ID: sessionID},
		Extensions: pipeline.Extensions{
			Inference: &pipeline.InferenceExtension{
				TotalTokens: totalTokens,
			},
		},
	}
	return pctx
}

func TestOnRequest_UnderLimit(t *testing.T) {
	p := newTestPlugin(1000, 0, 0)
	pctx := makePctx("sess-1", 0)

	action := p.OnRequest(context.Background(), pctx)
	if action.Type != pipeline.Continue {
		t.Fatalf("expected Continue, got %v", action.Type)
	}
}


func TestOnResponseFrame_Accumulates(t *testing.T) {
	p := newTestPlugin(1000, 0, 0)
	pctx := makePctx("sess-1", 42)

	action := p.OnResponseFrame(context.Background(), pctx, nil, true)
	if action.Type != pipeline.Continue {
		t.Fatalf("expected Continue, got %v", action.Type)
	}

	// Check in-memory cache was updated.
	p.mu.RLock()
	c := p.cache["sess-1"]
	p.mu.RUnlock()

	if c == nil {
		t.Fatal("expected cache entry")
	}
	if c.tokens != 42 {
		t.Errorf("tokens = %d, want 42", c.tokens)
	}
	if c.calls != 1 {
		t.Errorf("calls = %d, want 1", c.calls)
	}
}

func TestOnResponseFrame_SkipsNonLast(t *testing.T) {
	p := newTestPlugin(1000, 0, 0)
	pctx := makePctx("sess-1", 42)

	action := p.OnResponseFrame(context.Background(), pctx, []byte("data"), false)
	if action.Type != pipeline.Continue {
		t.Fatalf("expected Continue, got %v", action.Type)
	}

	p.mu.RLock()
	_, ok := p.cache["sess-1"]
	p.mu.RUnlock()
	if ok {
		t.Error("expected no cache entry on non-last frame")
	}
}

func TestOnResponseFrame_NoInference(t *testing.T) {
	p := newTestPlugin(1000, 0, 0)
	pctx := &pipeline.Context{
		Direction: pipeline.Outbound,
		Headers:   http.Header{},
		Session:   &pipeline.SessionView{ID: "sess-1"},
	}

	action := p.OnResponseFrame(context.Background(), pctx, nil, true)
	if action.Type != pipeline.Continue {
		t.Fatalf("expected Continue, got %v", action.Type)
	}

	p.mu.RLock()
	_, ok := p.cache["sess-1"]
	p.mu.RUnlock()
	if ok {
		t.Error("expected no cache entry when no inference data")
	}
}

func TestOnRequest_NoSession(t *testing.T) {
	p := newTestPlugin(100, 0, 0)
	pctx := &pipeline.Context{
		Direction: pipeline.Outbound,
		Headers:   http.Header{},
	}

	action := p.OnRequest(context.Background(), pctx)
	if action.Type != pipeline.Continue {
		t.Fatalf("expected Continue for nil session, got %v", action.Type)
	}
}

func TestAccumulate_WritesToStore(t *testing.T) {
	p := newTestPlugin(1000, 0, 0)
	store := newMemStore()
	p.store = store

	p.accumulate("sess-1", 100)

	fields, _ := store.HashGet(context.Background(), "session-budget:sess-1")
	if fields["tokens"] != "100" {
		t.Errorf("tokens in store = %q, want 100", fields["tokens"])
	}
	if fields["calls"] != "1" {
		t.Errorf("calls in store = %q, want 1", fields["calls"])
	}
	if fields["started_at"] == "" {
		t.Error("started_at not set in store")
	}
}

func TestAccumulate_ZeroTokens(t *testing.T) {
	p := newTestPlugin(1000, 10, 0)
	store := newMemStore()
	p.store = store

	p.accumulate("sess-1", 0)

	fields, _ := store.HashGet(context.Background(), "session-budget:sess-1")
	if fields["tokens"] != "" {
		t.Errorf("tokens in store = %q, want empty (no HINCRBY for 0)", fields["tokens"])
	}
	if fields["calls"] != "1" {
		t.Errorf("calls in store = %q, want 1", fields["calls"])
	}
	if fields["started_at"] == "" {
		t.Error("started_at not set in store")
	}
}

func TestOnResponseFrame_ZeroTokensCountsCalls(t *testing.T) {
	p := newTestPlugin(1000, 5, 0)
	pctx := makePctx("sess-1", 0)

	action := p.OnResponseFrame(context.Background(), pctx, nil, true)
	if action.Type != pipeline.Continue {
		t.Fatalf("expected Continue, got %v", action.Type)
	}

	p.mu.RLock()
	c := p.cache["sess-1"]
	p.mu.RUnlock()
	if c == nil {
		t.Fatal("expected cache entry for zero-token response")
	}
	if c.calls != 1 {
		t.Errorf("calls = %d, want 1", c.calls)
	}
	if c.tokens != 0 {
		t.Errorf("tokens = %d, want 0", c.tokens)
	}
}

func TestConfigure_Validation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     string
		wantErr bool
	}{
		{"valid", `{"redis_url":"redis://localhost","max_tokens":100}`, false},
		{"missing redis_url", `{"max_tokens":100}`, true},
		{"no limits", `{"redis_url":"redis://localhost"}`, true},
		{"invalid json", `{broken}`, true},
		{"zero refresh_interval", `{"redis_url":"redis://localhost","max_tokens":100,"refresh_interval":"0s"}`, true},
		{"negative refresh_interval", `{"redis_url":"redis://localhost","max_tokens":100,"refresh_interval":"-1s"}`, true},
		{"unparseable refresh_interval", `{"redis_url":"redis://localhost","max_tokens":100,"refresh_interval":"abc"}`, true},
		{"fail_closed rejected", `{"redis_url":"redis://localhost","max_tokens":100,"redis_unavailable":"fail_closed"}`, true},
		{"invalid on_exceed", `{"redis_url":"redis://localhost","max_tokens":100,"on_exceed":"block"}`, true},
		{"pause valid", `{"redis_url":"redis://localhost","max_calls":10,"on_exceed":"pause","pause_webhook":"http://localhost:9999/approve"}`, false},
		{"pause missing webhook", `{"redis_url":"redis://localhost","max_calls":10,"on_exceed":"pause"}`, true},
		{"pause invalid timeout", `{"redis_url":"redis://localhost","max_calls":10,"on_exceed":"pause","pause_webhook":"http://x","pause_timeout":"nope"}`, true},
		{"pause invalid timeout_action", `{"redis_url":"redis://localhost","max_calls":10,"on_exceed":"pause","pause_webhook":"http://x","pause_timeout_action":"maybe"}`, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := New()
			err := p.Configure(json.RawMessage(tt.cfg))
			if (err != nil) != tt.wantErr {
				t.Errorf("Configure() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestOnRequest_ShadowMode(t *testing.T) {
	p := New()
	cfg := `{
		"redis_url": "mem://test",
		"max_tokens": 100,
		"on_exceed": "observe",
		"refresh_interval": "100ms"
	}`
	if err := p.Configure(json.RawMessage(cfg)); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	p.store = newMemStore()

	p.OnResponseFrame(context.Background(), makePctx("sess-1", 60), nil, true)
	p.OnResponseFrame(context.Background(), makePctx("sess-1", 60), nil, true)

	p.mu.RLock()
	c := p.cache["sess-1"]
	p.mu.RUnlock()
	if c.tokens != 120 {
		t.Errorf("tokens = %d, want 120", c.tokens)
	}

	action := p.OnRequest(context.Background(), makePctx("sess-1", 0))
	if action.Type != pipeline.Continue {
		t.Fatalf("shadow mode: expected Continue past limit, got %v", action.Type)
	}

	// Calls are counted in OnResponseFrame (2 responses -> 2 calls).
	// OnRequest in observe mode does not increment.
	p.mu.RLock()
	calls := p.cache["sess-1"].calls
	p.mu.RUnlock()
	if calls != 2 {
		t.Errorf("calls after shadow OnRequest = %d, want 2 (from 2 OnResponseFrame calls)", calls)
	}
}

// TestOnRequest_RejectsAtCallLimit verifies that once cache reflects
// calls >= max_calls (populated by prior OnResponseFrame or hydrate),
// further OnRequests reject. Calls are counted on inference response,
// not on request, so this test seeds the cache directly.
func TestOnRequest_RejectsAtCallLimit(t *testing.T) {
	p := newTestPlugin(1000, 10, 0)

	p.mu.Lock()
	p.cache["sess-1"] = &counters{tokens: 50, calls: 10, startedAt: time.Now()}
	p.mu.Unlock()

	action := p.OnRequest(context.Background(), makePctx("sess-1", 0))
	if action.Type != pipeline.Reject {
		t.Fatalf("at limit: expected Reject, got %v", action.Type)
	}
}

func newPausePlugin(t *testing.T, maxCalls int64, webhookURL, timeoutAction string) *SessionBudget {
	t.Helper()
	p := New()
	cfg := fmt.Sprintf(`{
		"redis_url": "mem://test",
		"max_calls": %d,
		"on_exceed": "pause",
		"pause_webhook": %q,
		"pause_timeout": "200ms",
		"pause_timeout_action": %q,
		"refresh_interval": "100ms"
	}`, maxCalls, webhookURL, timeoutAction)
	if err := p.Configure(json.RawMessage(cfg)); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	p.store = newMemStore()
	p.httpClient = &http.Client{Timeout: 0}
	return p
}

// Approve / deny happy paths are covered by TestE2E_PauseMode in e2e_test.go
// (table-driven, asserts webhook request body + 403 response schema).

// TestOnRequest_PauseWebhookFailureFallback covers every "webhook doesn't
// return a valid approve" path: timeout, non-200, malformed body. All fall
// back to pause_timeout_action. The 'allow' row also proves the allow
// branch (only place it's exercised).
func TestOnRequest_PauseWebhookFailureFallback(t *testing.T) {
	hang := func(done <-chan struct{}) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) { <-done }
	}
	tests := []struct {
		name    string
		handler http.HandlerFunc
		action  string
		want    pipeline.ActionType
	}{
		{"timeout_deny", nil, "deny", pipeline.Reject},
		{"timeout_allow", nil, "allow", pipeline.Continue},
		{"non200_deny", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }, "deny", pipeline.Reject},
		{"badjson_deny", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`not json`))
		}, "deny", pipeline.Reject},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			done := make(chan struct{})
			h := tt.handler
			if h == nil {
				h = hang(done)
			}
			srv := httptest.NewServer(h)
			defer func() { close(done); srv.Close() }()

			p := newPausePlugin(t, 3, srv.URL, tt.action)
			p.mu.Lock()
			p.cache["sess-1"] = &counters{tokens: 0, calls: 3, startedAt: time.Now()}
			p.mu.Unlock()

			got := p.OnRequest(context.Background(), makePctx("sess-1", 0))
			if got.Type != tt.want {
				t.Fatalf("action = %v, want %v", got.Type, tt.want)
			}
		})
	}
}

func TestOnRequest_PauseWebhookUnreachable(t *testing.T) {
	p := newPausePlugin(t, 3, "http://127.0.0.1:1", "deny")
	p.mu.Lock()
	p.cache["sess-1"] = &counters{tokens: 0, calls: 3, startedAt: time.Now()}
	p.mu.Unlock()

	action := p.OnRequest(context.Background(), makePctx("sess-1", 0))
	if action.Type != pipeline.Reject {
		t.Fatalf("expected Reject on unreachable webhook (pause_timeout_action=deny), got %v", action.Type)
	}
}

func TestRefreshCache_PreservesLastApprovedAt(t *testing.T) {
	webhookCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		webhookCalls++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"action":"approve"}`))
	}))
	defer srv.Close()

	p := New()
	cfg := fmt.Sprintf(`{
		"redis_url": "mem://test",
		"max_calls": 3,
		"on_exceed": "pause",
		"pause_webhook": %q,
		"pause_timeout": "2s",
		"pause_timeout_action": "deny",
		"pause_grace_period": "10m",
		"refresh_interval": "100ms"
	}`, srv.URL)
	if err := p.Configure(json.RawMessage(cfg)); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	store := newMemStore()
	p.store = store
	p.httpClient = &http.Client{Timeout: 0}

	// Seed cache at limit and fire first request to get approval.
	p.mu.Lock()
	p.cache["sess"] = &counters{tokens: 0, calls: 3, startedAt: time.Now()}
	p.mu.Unlock()

	action := p.OnRequest(context.Background(), makePctx("sess", 0))
	if action.Type != pipeline.Continue {
		t.Fatalf("expected Continue after approval, got %v", action.Type)
	}
	if webhookCalls != 1 {
		t.Fatalf("expected 1 webhook call, got %d", webhookCalls)
	}

	// Simulate Redis having authoritative counters.
	ctx := context.Background()
	store.HashIncr(ctx, "session-budget:sess", "tokens", 100)
	store.HashIncr(ctx, "session-budget:sess", "calls", 5)
	store.HashSetNX(ctx, "session-budget:sess", "started_at", "1700000000")

	// Refresh replaces counters from Redis — must preserve lastApprovedAt.
	p.refreshCache()

	// Second request should still be within grace (no new webhook call).
	action = p.OnRequest(context.Background(), makePctx("sess", 0))
	if action.Type != pipeline.Continue {
		t.Fatalf("expected Continue within grace after refresh, got %v", action.Type)
	}
	if webhookCalls != 1 {
		t.Fatalf("expected still 1 webhook call after refresh, got %d", webhookCalls)
	}
}

func TestOnRequest_PauseGraceWindow(t *testing.T) {
	webhookCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		webhookCalls++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"action":"approve"}`))
	}))
	defer srv.Close()

	p := New()
	cfg := fmt.Sprintf(`{
		"redis_url": "mem://test",
		"max_calls": 3,
		"on_exceed": "pause",
		"pause_webhook": %q,
		"pause_timeout": "2s",
		"pause_timeout_action": "deny",
		"pause_grace_period": "10m",
		"refresh_interval": "100ms"
	}`, srv.URL)
	if err := p.Configure(json.RawMessage(cfg)); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	p.store = newMemStore()
	p.httpClient = &http.Client{Timeout: 0}

	p.mu.Lock()
	p.cache["sess"] = &counters{tokens: 0, calls: 3, startedAt: time.Now()}
	p.mu.Unlock()

	// First request fires the webhook.
	action := p.OnRequest(context.Background(), makePctx("sess", 0))
	if action.Type != pipeline.Continue {
		t.Fatalf("first request: expected Continue, got %v", action.Type)
	}
	if webhookCalls != 1 {
		t.Fatalf("expected 1 webhook call, got %d", webhookCalls)
	}

	// Second request within grace window skips the webhook.
	action = p.OnRequest(context.Background(), makePctx("sess", 0))
	if action.Type != pipeline.Continue {
		t.Fatalf("second request (grace): expected Continue, got %v", action.Type)
	}
	if webhookCalls != 1 {
		t.Fatalf("expected still 1 webhook call after grace, got %d", webhookCalls)
	}
}

func TestOnRequest_PauseGraceExpired(t *testing.T) {
	webhookCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		webhookCalls++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"action":"approve"}`))
	}))
	defer srv.Close()

	p := New()
	cfg := fmt.Sprintf(`{
		"redis_url": "mem://test",
		"max_calls": 3,
		"on_exceed": "pause",
		"pause_webhook": %q,
		"pause_timeout": "2s",
		"pause_timeout_action": "deny",
		"pause_grace_period": "10m",
		"refresh_interval": "100ms"
	}`, srv.URL)
	if err := p.Configure(json.RawMessage(cfg)); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	p.store = newMemStore()
	p.httpClient = &http.Client{Timeout: 0}

	// Seed cache with lastApprovedAt already expired (11 minutes ago > 10m grace).
	p.mu.Lock()
	p.cache["sess"] = &counters{
		tokens:         0,
		calls:          3,
		startedAt:      time.Now(),
		lastApprovedAt: time.Now().Add(-11 * time.Minute),
	}
	p.mu.Unlock()

	// Request after grace expired fires webhook.
	p.OnRequest(context.Background(), makePctx("sess", 0))
	if webhookCalls != 1 {
		t.Fatalf("expected 1 webhook call after grace expired, got %d", webhookCalls)
	}
}

func TestOnRequest_PausePendingApprovalSentinel(t *testing.T) {
	var webhookCalls int32
	started := make(chan struct{})
	proceed := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&webhookCalls, 1)
		close(started)
		<-proceed
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"action":"approve"}`))
	}))
	defer srv.Close()

	p := newPausePlugin(t, 3, srv.URL, "deny")
	p.mu.Lock()
	p.cache["sess"] = &counters{tokens: 0, calls: 3, startedAt: time.Now()}
	p.mu.Unlock()

	// First goroutine fires the webhook and blocks.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.OnRequest(context.Background(), makePctx("sess", 0))
	}()
	<-started // webhook is in-flight

	// Second concurrent request should piggyback (pendingApproval=true).
	action := p.OnRequest(context.Background(), makePctx("sess", 0))
	if action.Type != pipeline.Continue {
		t.Fatalf("concurrent request: expected Continue (piggyback), got %v", action.Type)
	}

	close(proceed) // unblock the webhook
	wg.Wait()

	if c := atomic.LoadInt32(&webhookCalls); c != 1 {
		t.Errorf("webhook called %d times, want exactly 1 (sentinel prevents thundering herd)", c)
	}
}

func TestEvaluate_MultipleLimits(t *testing.T) {
	p := newTestPlugin(100, 10, 60)

	tests := []struct {
		name    string
		c       *counters
		wantDeny bool
	}{
		{"all under", &counters{tokens: 50, calls: 5, startedAt: time.Now()}, false},
		{"tokens over", &counters{tokens: 100, calls: 5, startedAt: time.Now()}, true},
		{"calls over", &counters{tokens: 50, calls: 10, startedAt: time.Now()}, true},
		{"duration over", &counters{tokens: 50, calls: 5, startedAt: time.Now().Add(-90 * time.Second)}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason := p.evaluate(tt.c)
			if tt.wantDeny && reason == "" {
				t.Error("expected denial reason, got empty")
			}
			if !tt.wantDeny && reason != "" {
				t.Errorf("expected no denial, got %q", reason)
			}
		})
	}
}
