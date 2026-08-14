// Package sessionbudget enforces per-session lifetime budgets on tokens,
// inference calls, and wall-clock duration. Must run before inference-parser
// in the declared plugin order (response path is reverse: inference-parser
// finalizes counts first, then this plugin reads them).
package sessionbudget

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/rossoctl/cortex/authbridge/authlib/pipeline"
	"github.com/rossoctl/cortex/authbridge/authlib/plugins"
	"github.com/rossoctl/cortex/authbridge/authlib/storage"
)

type config struct {
	RedisURL           string `json:"redis_url" required:"true" description:"Redis/Valkey connection URL."`
	MaxTokens          int64  `json:"max_tokens" description:"Cumulative token ceiling per session. 0 = no limit."`
	MaxCalls           int64  `json:"max_calls" description:"Max inference calls per session. 0 = no limit."`
	MaxDurationSeconds int64  `json:"max_duration_seconds" description:"Wall-clock session lifetime in seconds. 0 = no limit."`
	OnExceed           string `json:"on_exceed" description:"Action on breach: deny, observe (shadow), or pause (HITL webhook approval)." default:"deny" enum:"deny,observe,pause"`
	PauseWebhook       string `json:"pause_webhook" description:"URL to POST for approval when on_exceed=pause. Required when on_exceed=pause."`
	PauseTimeout       string `json:"pause_timeout" description:"How long to wait for webhook response." default:"30s"`
	PauseTimeoutAction string `json:"pause_timeout_action" description:"Action on webhook timeout/error: deny or allow." default:"deny" enum:"deny,allow"`
	PauseGracePeriod   string `json:"pause_grace_period" description:"After approval, suppress further webhooks for this duration." default:"5m"`
	SessionTTLSeconds  int    `json:"session_ttl_seconds" description:"Redis key TTL; should be >= max_duration_seconds." default:"7200"`
	RefreshInterval    string `json:"refresh_interval" description:"How often to sync local cache from Redis." default:"5s"`
	RedisUnavailable   string `json:"redis_unavailable" description:"Behavior when Redis is unreachable. Only fail_open is supported; fail_closed is reserved." default:"fail_open"`
}

type counters struct {
	tokens         int64
	calls          int64
	startedAt      time.Time
	lastApprovedAt time.Time
}

// SessionBudget is the plugin state. Redis provides cross-pod durability;
// the local cache provides zero-I/O enforcement on the request path.
type SessionBudget struct {
	cfg         config
	store       storage.Store
	log         *slog.Logger
	httpClient  *http.Client
	gracePeriod time.Duration

	mu      sync.RWMutex
	cache   map[string]*counters
	stopCh  chan struct{}
	stopped chan struct{}
}

func New() *SessionBudget {
	return &SessionBudget{
		cache:   make(map[string]*counters),
		stopCh:  make(chan struct{}),
		stopped: make(chan struct{}),
		log:     slog.Default().With("plugin", "session-budget"),
	}
}

func init() {
	plugins.RegisterPlugin("session-budget", func() pipeline.Plugin { return New() })
}

func (p *SessionBudget) Name() string { return "session-budget" }

func (p *SessionBudget) Capabilities() pipeline.PluginCapabilities {
	return pipeline.PluginCapabilities{
		Description: "Enforce per-session token, call, and duration budgets via Redis.",
	}
}

func (p *SessionBudget) Configure(raw json.RawMessage) error {
	p.cfg = config{
		OnExceed:          "deny",
		SessionTTLSeconds: 7200,
		RefreshInterval:   "5s",
		RedisUnavailable:  "fail_open",
	}
	if err := json.Unmarshal(raw, &p.cfg); err != nil {
		return fmt.Errorf("session-budget config: %w", err)
	}
	if p.cfg.RedisURL == "" {
		return fmt.Errorf("session-budget: redis_url is required")
	}
	if p.cfg.MaxTokens <= 0 && p.cfg.MaxCalls <= 0 && p.cfg.MaxDurationSeconds <= 0 {
		return fmt.Errorf("session-budget: at least one limit (max_tokens, max_calls, max_duration_seconds) must be > 0")
	}
	switch p.cfg.OnExceed {
	case "deny", "observe", "pause":
	default:
		return fmt.Errorf("session-budget: on_exceed must be \"deny\", \"observe\", or \"pause\" (got %q)", p.cfg.OnExceed)
	}
	if p.cfg.OnExceed == "pause" {
		if p.cfg.PauseWebhook == "" {
			return fmt.Errorf("session-budget: pause_webhook is required when on_exceed=\"pause\"")
		}
		if p.cfg.PauseTimeout == "" {
			p.cfg.PauseTimeout = "30s"
		}
		if _, err := time.ParseDuration(p.cfg.PauseTimeout); err != nil {
			return fmt.Errorf("session-budget: invalid pause_timeout %q: %w", p.cfg.PauseTimeout, err)
		}
		if p.cfg.PauseTimeoutAction == "" {
			p.cfg.PauseTimeoutAction = "deny"
		}
		if p.cfg.PauseTimeoutAction != "deny" && p.cfg.PauseTimeoutAction != "allow" {
			return fmt.Errorf("session-budget: pause_timeout_action must be \"deny\" or \"allow\" (got %q)", p.cfg.PauseTimeoutAction)
		}
		if p.cfg.PauseGracePeriod == "" {
			p.cfg.PauseGracePeriod = "5m"
		}
		if d, err := time.ParseDuration(p.cfg.PauseGracePeriod); err != nil {
			return fmt.Errorf("session-budget: invalid pause_grace_period %q: %w", p.cfg.PauseGracePeriod, err)
		} else {
			p.gracePeriod = d
		}
	}
	if d, err := time.ParseDuration(p.cfg.RefreshInterval); err != nil {
		return fmt.Errorf("session-budget: invalid refresh_interval %q: %w", p.cfg.RefreshInterval, err)
	} else if d <= 0 {
		return fmt.Errorf("session-budget: refresh_interval must be > 0 (got %q)", p.cfg.RefreshInterval)
	}
	if p.cfg.RedisUnavailable == "fail_closed" {
		return fmt.Errorf("session-budget: redis_unavailable=fail_closed is not yet implemented; use fail_open")
	}
	return nil
}

func (p *SessionBudget) Init(_ context.Context) error {
	// "redis" driver handles both Redis and Valkey (wire-compatible); URL must use redis:// scheme.
	store, err := storage.Open("redis", p.cfg.RedisURL)
	if err != nil {
		return fmt.Errorf("session-budget: redis connect: %w", err)
	}
	p.store = store

	if p.cfg.OnExceed == "pause" && p.httpClient == nil {
		p.httpClient = &http.Client{Timeout: 0}
	}

	interval, _ := time.ParseDuration(p.cfg.RefreshInterval)
	go p.refreshLoop(interval)
	return nil
}

// In-flight accumulate goroutines get ErrClosed after store.Close — bounded by their 2s ctx.
func (p *SessionBudget) Shutdown(_ context.Context) error {
	close(p.stopCh)
	<-p.stopped
	if p.store != nil {
		return p.store.Close()
	}
	return nil
}

// OnRequest evaluates cached counters against limits and optimistically reserves
// a call slot so concurrent requests on the same session see each other. No I/O.
func (p *SessionBudget) OnRequest(ctx context.Context, pctx *pipeline.Context) pipeline.Action {
	sessionID := p.sessionID(pctx)
	if sessionID == "" {
		return pipeline.Action{Type: pipeline.Continue}
	}

	p.mu.Lock()
	c, ok := p.cache[sessionID]
	if !ok {
		p.mu.Unlock()
		// Cold cache: session not yet seen by this pod. First request passes; refresh loop
		// picks up Redis counters within one interval. Intentional one-request overshoot
		// tradeoff for zero-I/O enforcement on the hot path.
		return pipeline.Action{Type: pipeline.Continue}
	}
	snap := *c
	if reason := p.evaluate(&snap); reason != "" {
		switch p.cfg.OnExceed {
		case "observe":
			c.calls++
			p.mu.Unlock()
			pctx.Observe("shadow_budget_exceeded")
			p.log.Warn("budget exceeded (shadow mode)",
				"session", sessionID,
				"reason", reason,
				"tokens", snap.tokens,
				"calls", snap.calls)
			return pipeline.Action{Type: pipeline.Continue}

		case "pause":
			if p.gracePeriod > 0 && !c.lastApprovedAt.IsZero() && time.Since(c.lastApprovedAt) < p.gracePeriod {
				c.calls++
				p.mu.Unlock()
				return pipeline.Action{Type: pipeline.Continue}
			}
			c.calls++
			p.mu.Unlock()
			p.log.Info("budget exceeded, requesting approval",
				"session", sessionID,
				"reason", reason)
			if p.callPauseWebhook(ctx, sessionID, reason, &snap) {
				p.mu.Lock()
				if cc, ok := p.cache[sessionID]; ok {
					cc.lastApprovedAt = time.Now()
				}
				p.mu.Unlock()
				return pipeline.Action{Type: pipeline.Continue}
			}
			details := p.buildDetails(&snap)
			return pipeline.DenyWithDetails("budget.exceeded", reason+" (approval denied)", details)

		default: // "deny"
			p.mu.Unlock()
			details := p.buildDetails(&snap)
			return pipeline.DenyWithDetails("budget.exceeded", reason, details)
		}
	}
	// Optimistically reserve a call slot so concurrent requests see it.
	c.calls++
	p.mu.Unlock()

	return pipeline.Action{Type: pipeline.Continue}
}

// OnResponse is a no-op; see OnResponseFrame.
func (p *SessionBudget) OnResponse(_ context.Context, _ *pipeline.Context) pipeline.Action {
	return pipeline.Action{Type: pipeline.Continue}
}

// OnResponseFrame accumulates token counts on finalization (last=true).
func (p *SessionBudget) OnResponseFrame(_ context.Context, pctx *pipeline.Context, _ []byte, last bool) pipeline.Action {
	if !last {
		return pipeline.Action{Type: pipeline.Continue}
	}

	sessionID := p.sessionID(pctx)
	if sessionID == "" {
		return pipeline.Action{Type: pipeline.Continue}
	}

	inf := pctx.Extensions.Inference
	if inf == nil {
		return pipeline.Action{Type: pipeline.Continue}
	}

	tokens := int64(inf.TotalTokens)

	go p.accumulate(sessionID, tokens)

	p.mu.Lock()
	c, ok := p.cache[sessionID]
	if !ok {
		// First response without a prior OnRequest (e.g. cold cache path).
		c = &counters{startedAt: time.Now(), calls: 1}
		p.cache[sessionID] = c
	}
	c.tokens += tokens
	// calls already incremented by OnRequest's optimistic reserve.
	p.mu.Unlock()

	return pipeline.Action{Type: pipeline.Continue}
}

func (p *SessionBudget) buildDetails(snap *counters) map[string]any {
	details := map[string]any{
		"spent_tokens": snap.tokens,
		"spent_calls":  snap.calls,
		"token_limit":  p.cfg.MaxTokens,
		"call_limit":   p.cfg.MaxCalls,
	}
	if p.cfg.MaxDurationSeconds > 0 && !snap.startedAt.IsZero() {
		details["duration_seconds"] = int64(time.Since(snap.startedAt).Seconds())
		details["duration_limit"] = p.cfg.MaxDurationSeconds
	}
	return details
}

type pauseRequest struct {
	SessionID       string `json:"session_id"`
	Reason          string `json:"reason"`
	SpentTokens     int64  `json:"spent_tokens"`
	SpentCalls      int64  `json:"spent_calls"`
	TokenLimit      int64  `json:"token_limit"`
	CallLimit       int64  `json:"call_limit"`
	DurationSeconds int64  `json:"duration_seconds,omitempty"`
	DurationLimit   int64  `json:"duration_limit,omitempty"`
}

type pauseResponse struct {
	Action string `json:"action"`
}

func (p *SessionBudget) callPauseWebhook(ctx context.Context, sessionID, reason string, snap *counters) bool {
	timeout, _ := time.ParseDuration(p.cfg.PauseTimeout)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	body := pauseRequest{
		SessionID:   sessionID,
		Reason:      reason,
		SpentTokens: snap.tokens,
		SpentCalls:  snap.calls,
		TokenLimit:  p.cfg.MaxTokens,
		CallLimit:   p.cfg.MaxCalls,
	}
	if p.cfg.MaxDurationSeconds > 0 && !snap.startedAt.IsZero() {
		body.DurationSeconds = int64(time.Since(snap.startedAt).Seconds())
		body.DurationLimit = p.cfg.MaxDurationSeconds
	}

	payload, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.PauseWebhook, bytes.NewReader(payload))
	if err != nil {
		p.log.Warn("pause webhook request build failed", "session", sessionID, "err", err)
		return p.cfg.PauseTimeoutAction == "allow"
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		p.log.Warn("pause webhook call failed", "session", sessionID, "err", err)
		return p.cfg.PauseTimeoutAction == "allow"
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		p.log.Warn("pause webhook non-200", "session", sessionID, "status", resp.StatusCode)
		return p.cfg.PauseTimeoutAction == "allow"
	}

	var result pauseResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&result); err != nil {
		p.log.Warn("pause webhook response decode failed", "session", sessionID, "err", err)
		return p.cfg.PauseTimeoutAction == "allow"
	}
	return result.Action == "approve"
}

func (p *SessionBudget) evaluate(c *counters) string {
	if p.cfg.MaxTokens > 0 && c.tokens >= p.cfg.MaxTokens {
		return fmt.Sprintf("token limit reached: %d/%d", c.tokens, p.cfg.MaxTokens)
	}
	if p.cfg.MaxCalls > 0 && c.calls >= p.cfg.MaxCalls {
		return fmt.Sprintf("call limit reached: %d/%d", c.calls, p.cfg.MaxCalls)
	}
	if p.cfg.MaxDurationSeconds > 0 && !c.startedAt.IsZero() {
		elapsed := time.Since(c.startedAt).Seconds()
		if int64(elapsed) >= p.cfg.MaxDurationSeconds {
			return fmt.Sprintf("duration limit reached: %ds/%ds", int64(elapsed), p.cfg.MaxDurationSeconds)
		}
	}
	return ""
}

// accumulate writes counters to Redis. On failure, writes are dropped (fail-open).
func (p *SessionBudget) accumulate(sessionID string, tokens int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	key := p.redisKey(sessionID)
	ttl := time.Duration(p.cfg.SessionTTLSeconds) * time.Second

	if tokens > 0 {
		if _, err := p.store.HashIncr(ctx, key, "tokens", tokens); err != nil {
			p.log.Warn("redis HashIncr tokens failed", "session", sessionID, "err", err)
		}
	}

	if _, err := p.store.HashIncr(ctx, key, "calls", 1); err != nil {
		p.log.Warn("redis HashIncr calls failed", "session", sessionID, "err", err)
	}

	set, _ := p.store.HashSetNX(ctx, key, "started_at", strconv.FormatInt(time.Now().Unix(), 10))
	if set {
		_ = p.store.Expire(ctx, key, ttl)
	}
}

func (p *SessionBudget) refreshLoop(interval time.Duration) {
	defer close(p.stopped)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
			p.refreshCache()
		}
	}
}

// refreshCache replaces local counters with authoritative Redis values.
func (p *SessionBudget) refreshCache() {
	p.mu.RLock()
	keys := make([]string, 0, len(p.cache))
	for k := range p.cache {
		keys = append(keys, k)
	}
	p.mu.RUnlock()

	for _, sessionID := range keys {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		fields, err := p.store.HashGet(ctx, p.redisKey(sessionID))
		cancel()

		if err != nil {
			p.log.Warn("redis refresh failed", "session", sessionID, "err", err)
			continue
		}

		if len(fields) == 0 {
			p.mu.Lock()
			delete(p.cache, sessionID)
			p.mu.Unlock()
			continue
		}

		tokens, _ := strconv.ParseInt(fields["tokens"], 10, 64)
		calls, _ := strconv.ParseInt(fields["calls"], 10, 64)
		var startedAt time.Time
		if ts, err := strconv.ParseInt(fields["started_at"], 10, 64); err == nil {
			startedAt = time.Unix(ts, 0)
		}

		p.mu.Lock()
		var lastApprovedAt time.Time
		if existing, ok := p.cache[sessionID]; ok {
			// Take the max of local and Redis to avoid regressing counters when
			// in-flight accumulate goroutines haven't committed to Redis yet.
			if tokens < existing.tokens {
				tokens = existing.tokens
			}
			if calls < existing.calls {
				calls = existing.calls
			}
			if startedAt.IsZero() && !existing.startedAt.IsZero() {
				startedAt = existing.startedAt
			}
			lastApprovedAt = existing.lastApprovedAt
		}
		p.cache[sessionID] = &counters{tokens: tokens, calls: calls, startedAt: startedAt, lastApprovedAt: lastApprovedAt}
		p.mu.Unlock()
	}
}

func (p *SessionBudget) sessionID(pctx *pipeline.Context) string {
	if pctx.Session != nil && pctx.Session.ID != "" {
		return pctx.Session.ID
	}
	return ""
}

func (p *SessionBudget) redisKey(sessionID string) string {
	return "session-budget:" + sessionID
}

var (
	_ pipeline.Plugin             = (*SessionBudget)(nil)
	_ pipeline.Configurable       = (*SessionBudget)(nil)
	_ pipeline.Initializer        = (*SessionBudget)(nil)
	_ pipeline.Shutdowner         = (*SessionBudget)(nil)
	_ pipeline.StreamingResponder = (*SessionBudget)(nil)
)
