# Cortex

Cortex is a sidecar framework that **secures and observes the traffic of AI agents and Kubernetes workloads**. It sits in the request path — as a local forward/reverse proxy, an Envoy `ext_proc` filter, or a mesh waypoint — and adds:

- **Identity & tokens** — SPIFFE/SPIRE workload identity, JWT validation, and RFC 8693 token exchange (the original "AuthBridge" capability).
- **Protocol-aware observability** — decrypts egress (TLS bridge) and parses LLM inference, MCP, and A2A calls into a live session view (`abctl`).
- **Egress control & policy** — guardrails (IBAC / OPA) and per-host routing over a workload's outbound calls.

It runs the same way **in Kubernetes** (operator-injected sidecar) or **standalone** on a laptop/VM (a single binary, no cluster).

> Formerly **AuthBridge** — the code lives under [`authbridge/`](./authbridge/) and ships as the `authbridge-proxy` binary and the `abctl` session viewer.

## Quick start (local, no Kubernetes)

See an AI agent's egress — LLM, MCP, and A2A calls — decrypted and parsed live on your laptop. No cluster, no Keycloak, no SPIRE.

1. **Get the binaries.** Download prebuilt `abctl` and `authbridge-proxy` (linux/macOS, amd64/arm64) from the [Releases page](https://github.com/rossoctl/cortex/releases) and put them on your `PATH`. On macOS, clear the quarantine once: `xattr -dr com.apple.quarantine ./abctl ./authbridge-proxy`.
   _(Or build from source: `cd authbridge/cmd/abctl && go build .` then `cd ../authbridge-proxy && go build .`.)_

2. **Start Cortex** with the built-in local preset — a forward-only proxy (loopback-only) with the TLS bridge on and the protocol parsers, no config file needed. On first run it generates a demo CA under `./cortex-ca` (override with `--ca-dir`) and logs the exact `NODE_EXTRA_CA_CERTS=…` line to trust it:

   ```sh
   authbridge-proxy --demo
   ```

   _(It also writes that config to `./cortex-ca/demo.yaml` — edit that file and the running proxy hot-reloads it.)_

3. **Open the session viewer** in another terminal:

   ```sh
   abctl --endpoint http://localhost:9094
   ```

4. **Run your agent through it** — e.g. Claude Code (from the same directory, so `./cortex-ca` resolves — or use the absolute path the proxy logged):

   ```sh
   HTTPS_PROXY=http://localhost:8081 \
     NODE_EXTRA_CA_CERTS="$PWD/cortex-ca/ca.crt" \
     CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 \
     claude
   ```

   Its LLM, MCP, and A2A calls appear live in `abctl`, decrypted and parsed.

> Local / observe-only: the parsers *observe* traffic; nothing is enforced. `generate_ca` and the self-signed CA are for local use — in Kubernetes the CA is a mounted cert-manager Secret.

## Running on Kubernetes

In a cluster, Cortex sidecars are injected automatically by the [operator](https://github.com/rossoctl/operator), with Keycloak + SPIFFE/SPIRE for identity and token exchange. Start with the end-to-end **[Weather Agent walkthrough](./authbridge/demos/weather-agent/demo-ui.md)** (or the [`abctl` version](./authbridge/demos/weather-agent/demo-with-abctl.md)); see the [demos index](./authbridge/demos/README.md) and the [architecture reference](./authbridge/README.md) for all modes and details.

## Related repositories

- [rossoctl](https://github.com/rossoctl/rossoctl) — core platform
- [operator](https://github.com/rossoctl/operator) — sidecar injection + admission webhook

## License

[Apache 2.0](./LICENSE)
