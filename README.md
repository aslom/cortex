# Cortex

Cortex delivers easy-to-use platform services to agentic workloads. It runs in a workload's request path — a sidecar in Kubernetes, or a standalone binary anywhere else — and provides:

- **Identity & access** — a verifiable identity for each workload, authentication and authorization of its calls, and the right credentials for each downstream service.
- **Guardrails** — block agent actions that stray from the user's intent or aren't grounded in the conversation.
- **Observability** — decrypt and parse a workload's model, tool, and agent-to-agent traffic into a live view.
- **Egress control** — govern which external services a workload can reach.
- **Optimizations** — trim the model context a workload sends and cap its spend, to cut latency and cost.

It ships as a single binary; the identity and access layer is **AuthBridge**, and the code lives under [`authbridge/`](./authbridge/).

## Quick start (local, no Kubernetes)

See an AI agent's egress — LLM, MCP, and A2A calls — decrypted and parsed live on your laptop. No cluster, no Keycloak, no SPIRE.

1. **Install and start it.** One line installs the `abctl` and `authbridge-proxy` binaries (macOS/Linux) and starts the local demo. On first run it mints a demo CA under `./cortex-ca` and logs the `NODE_EXTRA_CA_CERTS=…` line to trust it:

   ```sh
   curl -fsSL https://raw.githubusercontent.com/rossoctl/cortex/main/authbridge/install-demo.sh | sh
   ```

   _(Prefer to inspect first? Read [`install-demo.sh`](./authbridge/install-demo.sh) — or build from source: `cd authbridge/cmd/abctl && go build .` then `cd ../authbridge-proxy && go build . && ./authbridge-proxy --demo`.)_

2. **Open the session viewer** in another terminal:

   ```sh
   abctl --endpoint http://localhost:9094
   ```

3. **Run your agent through it** — e.g. Claude Code (from the same directory, so `./cortex-ca` resolves — or use the absolute path the proxy logged):

   ```sh
   HTTPS_PROXY=http://localhost:8081 \
     NODE_EXTRA_CA_CERTS="$PWD/cortex-ca/ca.crt" \
     CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 \
     claude
   ```

   Its LLM, MCP, and A2A calls appear live in `abctl`, decrypted and parsed.

> Observe-only: the parsers *observe* traffic; nothing is enforced. The self-signed demo CA is for local use — in Kubernetes the CA is a mounted cert-manager Secret.

## Running on Kubernetes

In a cluster, Cortex sidecars are injected automatically by the [operator](https://github.com/rossoctl/operator), with Keycloak + SPIFFE/SPIRE for identity and token exchange. Start with the end-to-end **[Weather Agent walkthrough](./authbridge/demos/weather-agent/demo-ui.md)** (or the [`abctl` version](./authbridge/demos/weather-agent/demo-with-abctl.md)); see the [demos index](./authbridge/demos/README.md) and the [architecture reference](./authbridge/README.md) for all modes and details.

## Related repositories

- [rossoctl](https://github.com/rossoctl/rossoctl) — core platform
- [operator](https://github.com/rossoctl/operator) — sidecar injection + admission webhook

## License

[Apache 2.0](./LICENSE)
