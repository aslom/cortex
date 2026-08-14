# session-budget Plugin

Enforces per-session budgets on tokens, inference calls, and wall-clock
duration. Supports three `on_exceed` modes:

- `deny` — return 403 (default)
- `observe` — shadow mode: log without blocking, useful for calibrating limits
- `pause` — HITL: POST to a webhook for human/system approval before continuing

A "session" is the AuthBridge session ID (typically one A2A conversation
or agent task invocation). Redis holds durable counters across pods; a
local cache serves the hot path with zero I/O.

## Build

Opt-in — build with `-tags include_plugin_sessionbudget`:

```bash
docker build -f cmd/authbridge-proxy/Dockerfile \
  --build-arg GO_BUILD_TAGS="include_plugin_sessionbudget" \
  -t authbridge:latest .
```

Same tag works for `cmd/authbridge-envoy/Dockerfile`.

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

| Field | Default | Description |
|-------|---------|-------------|
| `redis_url` | — (required) | Redis/Valkey URL |
| `max_tokens` | 0 | Token ceiling (0 = no limit) |
| `max_calls` | 0 | Inference call cap (0 = no limit) |
| `max_duration_seconds` | 0 | Session lifetime cap (0 = no limit) |
| `on_exceed` | `deny` | `deny` (403), `observe` (log only), or `pause` (webhook) |
| `pause_webhook` | — | URL to POST on breach (required when `on_exceed=pause`) |
| `pause_timeout` | `30s` | Max wait for webhook response |
| `pause_timeout_action` | `deny` | Fallback on timeout/error: `deny` or `allow` |
| `pause_grace_period` | `5m` | Suppress repeat webhooks after approval |
| `session_ttl_seconds` | 7200 | Redis key TTL; must be ≥ `max_duration_seconds` |
| `refresh_interval` | `5s` | Local-cache sync interval |
| `redis_unavailable` | `fail_open` | Only `fail_open` supported today |

At least one of `max_tokens`, `max_calls`, `max_duration_seconds` must be > 0.

**Pipeline position:** must appear **before** `inference-parser` on the
outbound pipeline. Both must be present for token counting (inference-parser
supplies the token counts session-budget accumulates).

## Modes

### `deny` (default)

Returns 403 with a JSON body:

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

`duration_seconds` / `duration_limit` are included only when
`max_duration_seconds` is set.

### `observe` (shadow mode)

Counters still accumulate and limits are still evaluated, but breaches
only emit a WARN log (`"budget exceeded (shadow mode)"`) and the request
continues. Use to calibrate limits before enforcing:

1. Deploy with `on_exceed: observe` and conservative limits.
2. Watch logs for shadow-mode entries.
3. Adjust `max_tokens` / `max_calls` / `max_duration_seconds` to fit real workloads.
4. Flip to `on_exceed: deny` (or `pause`) once confident.

### `pause` (HITL webhook)

On breach, POST to `pause_webhook` and block the request until the
webhook responds or `pause_timeout` fires.

**Request body:**
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

**Expected response:**
```json
{"action": "approve"}
```
or
```json
{"action": "deny", "reason": "operator rejected"}
```

**On approval:** the request continues, and subsequent requests from
the same session skip the webhook for `pause_grace_period` (default
`5m`). This prevents per-request webhook spam once a session is
approved. Concurrent breaches during an in-flight webhook piggyback on
the pending call rather than each firing their own.

**On timeout / non-200 / bad JSON / unreachable:** falls back to
`pause_timeout_action` (`deny` returns 403; `allow` continues).

The grace window is pod-local (in-memory). In multi-pod deployments
without sticky sessions, each pod fires one webhook before its own
grace kicks in.

## Failure Modes

| Scenario | Behavior |
|----------|----------|
| Redis down at startup | Fail-open until refresh populates cache |
| Redis fails mid-session | Local cache keeps enforcing; writes dropped |
| Pod restart | First request passes (cold cache); refresh restores counters within `refresh_interval` |
| Webhook unreachable | Falls back to `pause_timeout_action` |

Infrastructure failures never produce false denials — Redis
unavailability degrades enforcement to local-cache-only (no cross-pod
consistency) rather than blocking requests.

**Token counting requires `usage` in provider responses.** Providers
that omit `usage` from streaming chunks (e.g. Anthropic via LiteLLM)
will show `promptTokens=0` in inference-parser logs — `max_tokens`
enforcement won't trigger, but `max_calls` and `max_duration_seconds`
still apply. Ollama, OpenAI, and Azure OpenAI include usage in
streaming responses and work fully.

## Redis Keys

```
session-budget:<session-id>   (Hash, TTL = session_ttl_seconds)
  tokens       cumulative token count
  calls        inference call count
  started_at   first-call unix timestamp
```

## Local Development

```bash
docker run -d --name valkey -p 6379:6379 valkey/valkey:latest
# redis_url: redis://localhost:6379  (or host.docker.internal from a container)

cd authbridge/authlib
go test ./plugins/sessionbudget/... -v -count=1
```
