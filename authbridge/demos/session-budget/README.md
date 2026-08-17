# session-budget demo assets

Deployable helpers for exercising the `session-budget` plugin. For plugin
configuration and mode semantics, see
[`../../docs/session-budget-plugin.md`](../../docs/session-budget-plugin.md).

## `k8s/pause-webhook-stub.yaml`

Minimal HITL webhook that returns `{"action":"approve"}` for every POST — enough
to smoke-test `on_exceed: pause` end-to-end. Also logs each incoming request
body so you can see exactly what session-budget sent.

```bash
kubectl apply -f k8s/pause-webhook-stub.yaml
```

Then point the plugin at it:

```yaml
pipeline:
  outbound:
    plugins:
      - name: session-budget
        config:
          redis_url: "redis://valkey.infra.svc:6379"
          max_calls: 5
          on_exceed: pause
          pause_webhook: "http://pause-webhook-stub.team1.svc.cluster.local"
```

Follow the webhook logs:

```bash
kubectl logs -n team1 deploy/pause-webhook-stub -f
```

To exercise the deny path, edit the inline Python in the manifest to write
`{"action":"deny"}` and re-apply.
