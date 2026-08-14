# session-budget Plugin

Enforces per-session lifetime budgets on tokens, inference calls, and
wall-clock duration. Supports three `on_exceed` modes:

- `deny` — return 403 (default)
- `observe` — shadow mode: log without blocking
- `pause` — HITL: fire a webhook for human/system approval before continuing

Uses Redis for cross-pod durable counters; evaluates limits from a local
cache with zero I/O on the hot path.

A "session" maps to the AuthBridge session ID (typically one A2A conversation
or agent task invocation).

## Build Tag

This plugin is **opt-IN**. Build with `-tags include_plugin_sessionbudget`
to include it (and its `storage/redis` dependency) in the binary:

```bash
cd authbridge
docker build -f cmd/authbridge-proxy/Dockerfile \
  --build-arg GO_BUILD_TAGS="include_plugin_sessionbudget" \
  -t authbridge:latest .
```

Without the tag, neither session-budget nor go-redis are linked.

The same build tag works for the envoy-sidecar image:

```bash
docker build -f cmd/authbridge-envoy/Dockerfile \
  --build-arg GO_BUILD_TAGS="include_plugin_sessionbudget" \
  -t authbridge-envoy:latest .
```

## Configuration

```yaml
pipeline:
  outbound:
    plugins:
      - name: token-exchange
        config: { ... }
      - name: session-budget
        config:
          redis_url: "redis://valkey.infra.svc:6379"
          max_tokens: 50000
          max_calls: 100
          max_duration_seconds: 1800
      - name: inference-parser
```

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `redis_url` | yes | — | Redis/Valkey connection URL |
| `max_tokens` | no | 0 | Cumulative token ceiling per session (0 = no limit) |
| `max_calls` | no | 0 | Max inference calls per session (0 = no limit) |
| `max_duration_seconds` | no | 0 | Wall-clock session lifetime in seconds (0 = no limit) |
| `on_exceed` | no | "deny" | `deny`, `observe`, or `pause` |
| `pause_webhook` | no | — | URL to POST for approval (required when `on_exceed=pause`) |
| `pause_timeout` | no | "30s" | How long to wait for webhook response |
| `pause_timeout_action` | no | "deny" | Action on webhook timeout/error: `deny` or `allow` |
| `pause_grace_period` | no | "5m" | After approval, suppress further webhooks for this duration |
| `session_ttl_seconds` | no | 7200 | Redis key TTL; should be >= `max_duration_seconds` |
| `refresh_interval` | no | "5s" | How often to sync local cache from Redis |
| `redis_unavailable` | no | "fail_open" | Only `fail_open` supported (stale cache retained on failure). `fail_closed` reserved. |

At least one of `max_tokens`, `max_calls`, or `max_duration_seconds` must be > 0.

**Local Redis/Valkey** for development: `docker run -d --name valkey -p 6379:6379 valkey/valkey:latest`.
Use `redis://localhost:6379` or `redis://host.docker.internal:6379` (container mode).

## Shadow Mode (observe)

Set `on_exceed: "observe"` to run the plugin in shadow mode. The plugin
still accumulates counters and evaluates limits, but instead of blocking
requests it logs a WARN and continues the pipeline. Use this to calibrate
limits under real workloads before enabling enforcement.

Rollout workflow:
1. Deploy with `on_exceed: "observe"` and conservative limits
2. Monitor logs for `"budget exceeded (shadow mode)"` entries
3. Adjust `max_tokens` / `max_calls` / `max_duration_seconds` based on observed patterns
4. Flip to `on_exceed: "deny"` when confident in the thresholds

## Pause Mode (HITL)

Set `on_exceed: "pause"` to enable human-in-the-loop approval. When budget
is exceeded, the plugin fires a synchronous HTTP POST to `pause_webhook`
and blocks the request until a response arrives (up to `pause_timeout`).

### Webhook request body (POST)

```json
{
  "session_id": "abc-123",
  "reason": "call limit reached: 50/50",
  "spent_tokens": 48200,
  "spent_calls": 50,
  "token_limit": 100000,
  "call_limit": 50,
  "duration_seconds": 1205,
  "duration_limit": 1800
}
```

### Webhook expected response

```json
{"action": "approve"}
```
or
```json
{"action": "deny", "reason": "operator rejected"}
```

### Behavior on approval

After `{"action": "approve"}`, the request continues and subsequent requests
within `pause_grace_period` (default 5m) pass without re-invoking the webhook.
This prevents per-request webhook spam once a session is approved to continue.

### Behavior on timeout/error

If the webhook is unreachable, returns non-200, returns invalid JSON, or
exceeds `pause_timeout`, the plugin falls back to `pause_timeout_action`:
- `"deny"` (default) — reject with 403
- `"allow"` — continue the pipeline

### Design notes

- The request goroutine blocks during the webhook call (same pattern as IBAC's LLM judge)
- Client disconnect cancels the request context, which cancels the webhook call
- The grace period is pod-local (in-memory cache); worst case after pod restart is one extra webhook call
- Concurrent requests from the same session may both fire webhooks (acceptable for v1)

## Pipeline Position

Must be declared **before** `inference-parser` in the outbound plugin list.
The response path runs in reverse order, so inference-parser finalizes token
counts first, then session-budget reads them. Both plugins implement
`StreamingResponder` so they work with streaming SSE responses (Ollama,
LiteLLM, OpenAI) and buffered JSON responses alike.

## Response Format (403)

```json
{
  "error": "budget.exceeded",
  "message": "token limit reached: 50200/50000",
  "details": {
    "spent_tokens": 50200,
    "spent_calls": 42,
    "token_limit": 50000,
    "call_limit": 100,
    "duration_seconds": 1205,
    "duration_limit": 1800
  }
}
```

`duration_seconds` and `duration_limit` are included only when `max_duration_seconds` is configured.

## Redis Key Schema

```text
session-budget:<session-id>  (Hash, TTL = session_ttl_seconds)
  tokens      int   cumulative TotalTokens
  calls       int   inference call count
  started_at  unix  first-call timestamp (set-if-not-exists)
```

**Migration note:** The Redis key prefix changed from `token-budget:` to
`session-budget:`. Existing keys from prior deployments will be orphaned
and expire within `session_ttl_seconds`.

## Failure Modes

| Scenario | Behavior |
|----------|----------|
| Redis down at startup | `Init` succeeds (no connectivity check); enforcement fail-open until first refresh populates cache |
| Redis fails mid-session | Local cache continues enforcing; writes dropped silently |
| Pod restarts | First request passes (cold cache); refresh picks up Redis counters within one interval |
| Provider returns no usage data | `max_tokens` not enforced; `max_calls` and `max_duration_seconds` still work |
| Pause webhook unreachable | Falls back to `pause_timeout_action` (default: deny) |

**Fail-open guarantee:** The plugin never blocks requests due to its own infrastructure failures. Redis unavailability degrades enforcement (local cache only, no cross-pod consistency) but never causes false denials.

**Note on token counting:** Token accumulation requires the LLM provider to
return `usage` (prompt/completion token counts) in responses. Providers that
omit usage from streaming chunks (e.g. Anthropic via LiteLLM) will show
`promptTokens=0` in inference-parser logs — `max_tokens` enforcement won't
trigger for these providers, but `max_calls` and `max_duration_seconds` still
apply. Ollama, OpenAI, and Azure OpenAI include usage in streaming responses
and work fully.

## Testing

```bash
cd authbridge/authlib
go test ./plugins/sessionbudget/... -v -count=1
```

No external dependencies — tests use in-memory stores.
