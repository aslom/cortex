# session-budget demo assets

Deployable helpers for exercising the `session-budget` plugin. For plugin
configuration and mode semantics, see
[`../../docs/session-budget-plugin.md`](../../docs/session-budget-plugin.md).

## `k8s/pause-webhook-stub.yaml`

Minimal HITL webhook that returns `{"action":"approve"}` for every POST —
enough to smoke-test `on_exceed: pause` end-to-end. Also logs each
incoming request body so you can see exactly what session-budget sent.

```bash
kubectl apply -f k8s/pause-webhook-stub.yaml
```

To exercise the deny path, edit the inline Python in the manifest to
return `{"action":"deny"}` and re-apply.

Follow the webhook stub:

```bash
kubectl logs -n team1 deploy/pause-webhook-stub -f
```

## Prerequisites

- **A Redis-wire-compatible store** reachable from the agent pod. Any
  Valkey/Redis deployment works; point `redis_url` at its Service.
- **An agent with `jwt-validation` and (optionally) `token-exchange`
  configured on its authbridge sidecar.** These are shared across every
  authbridge demo — see the
  [weather-agent demo](../weather-agent/) for the standard inbound
  validation setup, and
  [token-exchange-routes](../token-exchange-routes/) for outbound route
  configuration. This demo assumes those are already in place and focuses
  on adding `session-budget` to the outbound pipeline.

**Ambient-mesh note:** if your namespace has
`istio.io/dataplane-mode: ambient`, the datastore pod needs the
pod-level label `istio.io/dataplane-mode: none`. Ambient's ztunnel drops
non-HBONE connections with `Connection reset by peer`, and Redis RESP is
raw TCP — it can't ride HBONE. The pause webhook stub manifest already
carries the exemption.

## Configuring the plugin

```yaml
pipeline:
  inbound:
    plugins:
      - name: jwt-validation
        config: { ... }
      - name: a2a-parser         # REQUIRED — parses contextId → Session.ID
  outbound:
    plugins:
      - name: token-exchange
        config: { ... }
      - name: session-budget
        config:
          redis_url: "redis://valkey.team1.svc:6379"
          max_calls: 3
          max_duration_seconds: 1800
          on_exceed: pause
          pause_webhook: "http://pause-webhook-stub.team1.svc.cluster.local"
          pause_timeout: 10s
          pause_timeout_action: deny
          pause_grace_period: 5m
      - name: inference-parser
```

**`a2a-parser` on inbound is not optional.** Without it, every request
lands in the `default` session bucket (no `Rekey` from `contextId`), so
session-budget can never distinguish sessions and cold-cache hydrate
looks for the wrong key.
