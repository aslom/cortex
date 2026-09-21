# Issue #945 — Verified Linux install and systemd service lifecycle

**Status:** research + gap analysis complete. Landed 2026-09-17: the `TimeoutStopSec` fix
(bullet 7) and the Tier 2 `fakeSystemctl`/`fakeLoginctl` test harness (bullet 6), which
required refactoring four functions to take `goos` explicitly. Landed 2026-09-20: the
Tier 3 real-systemd integration test (bullet 5) — written, compiles, correctly skips on
non-Linux, but **not yet actually run against a real systemd** (needs a real Linux box
or CI wiring — see "Suggested next steps" #5). Tier 4 (install.sh smoke test, #957) and
Tier 5 (reboot check, #964) are separate issues, not started.
**Owner:** Alan Cha (per epic #962 owner table: "Linux install, release smoke tests, reboot
verification, CI").
**Repo:** rossoctl/cortex. This doc lives in the same directory as other planning docs
(`authbridge/docs/superpowers/plans/`) for consistency, but is a research/gap-analysis
reference, not yet an execution-ready task plan.

## Why this doc exists

Assigned three issues (#945, #956, #957) under epic #962 ("Cortex v0.9.0 — stable local tool
for coding agents", due 2026-09-30). This doc captures the research done to plan #945
specifically, so the findings don't have to be re-derived later. #956/#957 have their own
dependencies (see "Relationship to other issues" below) and are intentionally out of scope
here.

## The three assigned issues and priority order

| # | Title | Why this order |
|---|---|---|
| **#945** | Verified Linux install and systemd service lifecycle | **Do first.** Self-contained, no dependency on other owners' unfinished work. #957 automates verifying this. |
| **#957** | Release smoke test on Linux CI runners | **Second.** Automates #945; same owner, same platform, lowest marginal complexity once #945's gaps are closed. Partial dependency on #955 (unattended capture path, owned by @esnible) for one checklist item (asserting a non-zero token count). |
| **#956** | Release smoke test on macOS CI runners | **Last.** Depends on #944 (macOS install, owned by @huang195) and #955 — two external blockers outside this owner's control, on a platform (launchd) not owned here. |

## Correcting an early false start: this is not greenfield

Initial read of the issue text (and of a stale local git checkout, ~452 commits behind
`origin/main`) suggested none of this existed yet. After syncing to `origin/main`, that's
wrong: the core service-lifecycle feature already shipped in **PR #876** ("Feat: Keep Cortex
running across crashes and logins (launchd / systemd)"), with follow-on fixes:

- **#880** — Fix: upgrades fail with launchd `EIO` (bootout race) — macOS-only issue.
- **#897** — Fix: detect environments that cannot run a service, and say what to do.
- **#931** — Fix: say when a new CA leaves running agents unobservable.
- **#911** — Fix: trust the bridge CA where it matters, stop intercepting dev tooling.

So #945 is a **verification and gap-closing** issue on top of substantially-built
functionality, not a build-from-scratch feature. The lesson for next time: always confirm
`git fetch` / compare against `origin/main` before concluding something doesn't exist.

## Conceptual background: what systemd and launchd are for, and why the two platforms differ

A service manager's job: start a background process, notice when it dies (including
crashes, not just deliberate stops), and decide whether to relaunch it — without a human
watching a terminal.

**The key asymmetry that shapes the whole design:**

- **systemd** (Linux): a unit file's `Restart=on-failure` is honored by systemd's own
  supervisor loop, reliably, even for a unit just `systemctl --user start`ed while already
  logged in (exactly Cortex's `curl | sh` scenario).
- **launchd** (macOS): the team found — and documented in code comments — that launchd's
  equivalent (`KeepAlive`, tried alongside `StartInterval` and `RunAtLoad`) does **not**
  reliably restart a LaunchAgent added mid-session (as opposed to one present at boot when
  launchd first scans `LaunchAgents`). Since Cortex is always installed mid-session, this
  gap is squarely in the installer's path.

**Consequence for the architecture:**

- **macOS** runs **two processes**: launchd starts a small Go-written supervisor
  (`authbridge-proxy --supervise`, in `authbridge/cmd/authbridge-proxy/supervise.go`) that
  itself watches and restarts the actual proxy child. launchd supervises the supervisor;
  the supervisor does the real crash-recovery job launchd won't reliably do.
- **Linux** runs **one process**: `authbridge-proxy` directly as the systemd unit's
  `ExecStart=`, with `Restart=on-failure` doing all the crash-recovery work natively. No
  supervisor binary involved. Confirmed in code:
  `cmd_service_platform.go:92-93` and `TestSupervisionIsPlatformCorrect`
  (`cmd_service_test.go:312-332`) explicitly assert Linux does **not** use `--supervise`.

This means **#945 is structurally simpler than #944** on the crash-recovery axis — but it
also means the Linux "Restart=on-failure actually works" assumption has never been
put through the same real-world verification (and design correction) that produced the
macOS supervisor. That's the single biggest theme in the gap analysis below.

## Where things live (for orientation)

- `authbridge/install.sh` — the `curl | sh` installer (~1300 lines, POSIX `sh`).
- `authbridge/install_test.sh` — installer unit tests (shell functions in isolation, no
  real network/systemctl).
- `authbridge/cmd/abctl/cmd_service.go` — cross-platform `abctl service` command logic
  (install/uninstall/status/start/stop/restart), ~870 lines.
- `authbridge/cmd/abctl/cmd_service_platform.go` — the OS-specific half: systemd unit
  rendering, launchd plist rendering, `systemctl`/`launchctl`/`loginctl` invocations, ~723
  lines.
- `authbridge/cmd/authbridge-proxy/supervise.go` — the macOS-only (and unsupervised-fallback)
  Go restart loop, ~118 lines.
- `authbridge/cmd/authbridge-proxy/main.go` — the proxy itself; on SIGTERM/SIGINT does a
  graceful shutdown with a **15-second drain** (`context.WithTimeout(..., 15*time.Second)`,
  `main.go:671`) — every downstream "does stop tolerate the drain" question traces back to
  this constant.
- `authbridge/docs/laptop-service.md` — user-facing doc for `abctl service *`,
  `~/.cortex/` layout, restricted-environment fallback (`authbridge-proxy --local`), and
  manual-removal instructions. Good source of ground truth for expected behavior.
- `authbridge/cmd/abctl/cmd_service_*_test.go` — several files split by concern (bootout,
  durable, recovery, restricted, stamp, perm). See gap analysis for which of these actually
  exercise Linux.

## Gap analysis against #945's checklist

Full detail came from a source-code audit (Explore agent, 25 tool calls, full read of
`cmd_service.go`, `cmd_service_platform.go`, `supervise.go`, `install.sh`, and every
`cmd_service_*_test.go` file). Condensed per checklist bullet:

### 1. Fresh install via `curl | sh`, amd64 and arm64
- **Exists:** OS/arch detection (`install.sh:816-828`); both Linux arches are actually
  published by `release-binaries.yaml:156`; checksum verification prefers `shasum`, falls
  back to `sha256sum`; `supervisor_usable()` (`install.sh:645-653`) does a *live* preflight
  (`systemctl --user show-environment`, not just `command -v systemctl`) — this is what
  catches containers/minimal images where the binary exists but no user session does.
- **Gap:** No CI job or test actually installs and runs the real released binaries against a
  live Linux box, for either architecture. `install_test.sh` only unit-tests shell functions
  with system commands stubbed out. There is no arm64 CI runner at all — arm64 is only
  exercised at *build* time, never at *install/run* time. The issue text says this is
  "automated by the Linux release smoke test" — **no such smoke-test workflow exists yet**
  (this is exactly #957's job, and #957 depends on #945 being solid first).

### 2. Upgrade over an older install: service restarted, config preserved
- **Exists:** `serviceInstall` idempotency via `installCanSkip`/`serviceIsCurrent`
  (`cmd_service.go:774-836`); config migration (`migrateConfig`, `cmd_service.go:343`) before
  restart; binary-swap detection via SHA-256 stamp file
  (`proxyBinaryUnchanged`, `cmd_service_platform.go:806-824`, platform-agnostic and well
  tested). Linux restart is `systemctl --user daemon-reload` then `enable --now`
  (`cmd_service_platform.go:216-247`) — no bootout-race workaround needed, per explicit
  comment (`cmd_service_platform.go:92-93`) that systemd's own `Restart=`/`StartLimit*`
  make the launchd EIO workaround (#880) unnecessary on Linux.
- **Gap:** Nothing drives an actual upgrade against a real running `systemd --user` unit to
  confirm the restart genuinely leaves Cortex serving afterward. macOS has a dedicated
  real-launchd integration test for its equivalent scenario
  (`TestWaitBootedOut_RealLaunchd`); there is no Linux analog.

### 3. Uninstall leaves no unit file, no `~/.cortex`, no modified agent settings
- **Exists:** `serviceUninstall` (`cmd_service.go:503-538`) removes the unit + stamp file,
  deliberately leaves `~/.cortex` alone (that's a separate, manual step per
  `laptop-service.md`). Linux `unloadService` (`cmd_service_platform.go:269-291`) tracks
  whether *it* enabled lingering (via a marker file) so uninstall doesn't clobber a linger
  setting the user had already set for unrelated units — a thoughtful, Linux-specific detail.
- **Gap:** None of this Linux uninstall logic (the linger-marker branch,
  `systemctl --user disable --now`) has a test with faked `systemctl`/`loginctl` — the
  existing `TestStopIsDurable` only greps source for expected strings, it doesn't run the
  code path. "No modified agent settings" is really an `abctl claude-code disable` concern,
  outside the files audited here — worth confirming separately.

### 4. Re-running install is idempotent
- **Exists:** `installCanSkip` (well tested, platform-agnostic); `install.sh`'s
  already-at-this-version guard (`install.sh:885-889`); `lingerEnabled()`
  (`cmd_service_platform.go:259-267`) avoids re-toggling linger on repeat installs.
- **Gap:** `lingerEnabled`'s parsing of `loginctl show-user --property=Linger` output has
  zero test coverage — a parsing regression could silently re-enable linger every install,
  or silently skip enabling it when needed.

### 5. Service survives reboot and a crash — **Tier 3 test written 2026-09-20, not yet CI-verified**
- **Added:** `cmd_service_systemd_integration_test.go` — mirrors
  `TestWaitBootedOut_RealLaunchd`'s structure exactly: a throwaway `systemd-run --user`
  transient unit, a synthetic slow-to-exit script (not the real proxy, for the same
  isolation reason the darwin test uses "slow.sh"), the same four skip guards
  (wrong OS, missing binaries, no reachable `systemctl --user` session) plus an
  `ABCTL_SYSTEMD_TESTS=required` escape hatch. Two tests:
  `TestSupervisorRestartsAfterCrash_RealSystemd` (`kill -9` the main PID, confirm the
  unit comes back with a *different* PID — proving `Restart=on-failure` actually fires)
  and `TestSupervisorStaysStoppedAfterDeliberateStop_RealSystemd` (a deliberate
  `systemctl stop` must NOT trigger a restart — the other half of the claim in
  `renderUnitFor`'s comment: *"a `systemctl stop` is distinguishable from a crash, so a
  stop stays stopped"*).
- **Honest limit:** written and confirmed to compile, `go vet` cleanly, and correctly
  *skip* (not silently pass, not fail) on a non-Linux host with a clear reason — that's
  the full extent of what's verifiable from a Mac. The actual real-systemd behavior these
  tests assert has **not yet been confirmed to pass on a real machine**. Next: either run
  manually on a real Linux box, or wire the CI setup (`enable-linger` +
  `XDG_RUNTIME_DIR` + setting `ABCTL_SYSTEMD_TESTS=required` for this job) discussed in
  "Suggested next steps" below and watch it run there.
- Still the biggest historical gap this closes: unlike the darwin `KeepAlive` claim
  (tested, found false, drove the supervisor redesign — see below), the Linux
  `Restart=on-failure` assumption had never been through an equivalent real check at all.
- **Exists (and well-reasoned):** unit rendering (`renderUnitFor("linux", ...)`,
  `cmd_service_platform.go:131-148`) sets `Restart=on-failure`, `RestartSec=10`,
  `StartLimitIntervalSec=300`, `StartLimitBurst=5` — correctly placed in `[Unit]`, not
  `[Service]` (a comment explains systemd v229+ ignores/deprecates it in `[Service]`, which
  would silently void the throttle). `[Install] WantedBy=default.target` handles
  reboot/login persistence. Good unit-level string-shape test coverage
  (`TestRenderUnit_BothPlatforms`, `cmd_service_test.go:65-106`).
- **Gap:** No test — unit, integration, or even a documented manual-verification note —
  proves that a real `systemd --user` instance actually restarts the unit after `kill -9`,
  or survives a real reboot/logout with lingering enabled. The macOS side has exactly this
  kind of verification on record: comments at `cmd_service_platform.go:44-58` document that
  `KeepAlive` was tested, found **not** to work end-to-end, and that finding drove the
  supervisor-process redesign. **No equivalent verification episode exists for Linux's
  `Restart=on-failure` assumption** — it's currently trusted, not proven. This is precisely
  the class of gap #945 (and its manual-reboot sibling, #964) exists to close.

### 6. `abctl service status | start | stop | restart` accurate in every state — **CLOSED (Tier 2) 2026-09-17**
- **Exists:** `serviceStatus`/`serviceControl` (`cmd_service.go:540-665`) are
  platform-agnostic; Linux `controlService` maps stop→`disable --now` (persistent),
  start→`enable --now`, restart→plain `systemctl --user restart` (transient, by design).
  `supervisorRunning`'s Linux branch uses `systemctl --user is-active cortex.service`, with a
  sane fallback ("no systemctl → treat as unknown, don't block") for systemd-less boxes.
- **Fixed:** `loadService`, `controlService`, `supervisorRunning`, and `unloadService`
  took `runtime.GOOS` directly (unlike `renderUnitFor`, which already took `goos` as an
  explicit parameter for exactly this reason). Refactored all four to take `goos string`
  explicitly, updated every call site in `cmd_service.go` to pass `runtime.GOOS`, and added
  `cmd_service_systemd_test.go` — a `fakeSystemctl`/`fakeLoginctl` harness mirroring
  `fakeLaunchctl`, plus tests that actually run (not skip) on any host by passing `"linux"`
  explicitly. Confirmed by running for real (not just reading the code) that `is-active`
  already correctly treats `activating`/`failed`/`deactivating`/`inactive` as "not
  running" — only `active` (exit 0) reads as running. That logic was already correct; it
  was just unverified. Also covers: `loadService`'s daemon-reload/enable failure messages,
  the linger-enable/marker-write logic (skip when already on, write marker only when *we*
  turn it on, caveat-not-fatal when `enable-linger` fails), `unloadService`'s
  marker-gated `disable-linger` (never called without a marker), and `controlService`'s
  exact verb-per-action mapping. 22 subtests, all passing.
- **Still open:** none of this proves the *real* `systemctl`/`loginctl` actually behave
  this way — only that our own code reacts correctly to inputs we scripted. That's Tier 3
  (real systemd integration test), still not built.

### 7. `stop` tolerates the proxy's ~15s drain — **CLOSED 2026-09-17**
- **Exists:** the proxy's own 15s shutdown timeout (`main.go:671`) is the anchor value
  everything else has to respect. macOS handles this *explicitly* in Go
  (`serviceBootoutTimeout = 30*time.Second`, `cmd_service.go:39-42`, plus the supervisor's
  own 20s-before-SIGKILL logic in `supervise.go:88-95`, deliberately longer than 15s).
- **Fixed:** added an explicit `TimeoutStopSec=20` to `renderUnitFor("linux", ...)`
  (`cmd_service_platform.go`), matching the macOS supervisor's 20s headroom over the
  proxy's 15s drain, with a rationale comment in the same style as the surrounding
  `StartLimit*`/`network-online.target` comments. `TestRenderUnit_BothPlatforms`'s
  `"linux restarts on failure only"` subtest now asserts the line is present, so a future
  regression that drops it fails CI instead of silently reverting to systemd's undocumented
  default. Previously it "worked" only by accident of systemd's 90s default exceeding 15s —
  now it's an explicit, tested value.

### 8. Works under user systemd, and states what happens where systemd is absent
- **Exists (this is the best-handled bullet):** `loadService` gives a clear,
  actionable message when `systemctl` isn't found at all, naming WSL1/containers as likely
  causes (`cmd_service_platform.go:216-219`). `install.sh`'s `supervisor_usable()` does the
  more realistic check — `systemctl` present but user manager/D-Bus unreachable — and falls
  back to `start_unsupervised` (a plain backgrounded process using the proxy's own
  `--supervise` loop), printing manual start/stop/status/logs instructions. Well tested at
  the shell-function level (`install_test.sh:570-575`).
- **Gap:** On the Go side, there's no Linux equivalent of `launchdUsable()`
  (`cmd_service_platform.go:680-696`, explicitly darwin-only) — the function that lets
  macOS refuse cleanly *before writing anything to disk* with a dedicated, tested exit code
  (`exitNoSupervisor`, exit 4). On Linux, "no systemd" is discovered later, inside
  `loadService`, after the unit file has already been written (then cleaned up on failure).
  End behavior is probably fine — `install.sh`'s generic fallback classifier still buckets
  it correctly — but it's fine by accident/genericness, not by an intentional, tested,
  Linux-aware preflight the way darwin gets. No Go test asserts the exact message or exit
  code for "systemctl absent" the way `TestServiceInstall_RestrictedEnvironment` does for
  darwin.

## Cross-cutting themes

1. **The systemd rendering/string-shape logic is solid and well tested.** The gap is almost
   entirely at the "does this actually work against a real system service manager" layer —
   nothing fakes or drives real `systemctl`/`loginctl` for Linux, while macOS has both a
   `fakeLaunchctl` unit-test harness *and* a real-launchd integration test.
2. **The Linux path is architecturally simpler** (no supervisor process, no bootout-race
   workaround) for a legitimate reason — but that simplicity has never been backed by the
   same real-world verification that justified and shaped the macOS design.
3. **One concrete, low-risk fix stands out:** add `TimeoutStopSec=` to the Linux unit. Small,
   self-contained, directly addresses checklist bullet 7.
4. **No real Linux CI smoke test exists yet anywhere in the repo** — this is #957's job, but
   #957 can't be trusted until the gaps above are closed, since a smoke test built on top of
   an unverified `Restart=on-failure` assumption would just as confidently pass.

## Suggested next steps (not yet sequenced into a task plan)

1. ~~Add an explicit `TimeoutStopSec=` to `renderUnitFor("linux", ...)`~~ — **done
   2026-09-17**, see bullet 7 above.
2. ~~Build a `fakeSystemctl`/`fakeLoginctl` test harness~~ — **done 2026-09-17**, see
   bullet 6 above. Required refactoring `loadService`/`controlService`/`supervisorRunning`/
   `unloadService` to take `goos` explicitly first (same fix `renderUnitFor` already had) —
   otherwise these functions can't be exercised from a non-Linux host at all.
3. ~~Get real verification that `Restart=on-failure` actually recovers the unit after
   `kill -9`~~ — **test written 2026-09-20** (`cmd_service_systemd_integration_test.go`,
   see bullet 5 above), but **not yet run against a real systemd** — only confirmed to
   compile and correctly skip on a non-Linux host. Still needed: actually run it on a real
   Linux box or in CI, and separately verify lingering survives a real logout/reboot
   (that second half is not covered by this test at all — it only proves crash-recovery
   within a live session, not the lingering/reboot-survival claim; see #964).
4. Decide whether Linux needs its own `launchdUsable()`-equivalent preflight (tested, named
   exit code) or whether the current "discover it inside `loadService`, clean up, fall
   through to the generic fallback" behavior is an acceptable, intentional asymmetry.
5. Wire `cmd_service_systemd_integration_test.go` into CI: add an `enable-linger` +
   `XDG_RUNTIME_DIR` setup step to the `abctl` leg of `go-ci-authbridge-cmd` in `ci.yaml`,
   and set `ABCTL_SYSTEMD_TESTS=required` for that job — unlike the darwin equivalent
   (`ABCTL_LAUNCHD_TESTS=required`), which exists in code but is never set by any
   workflow, so `TestWaitBootedOut_RealLaunchd` has skipped in every CI run since it was
   written. Doing this step, unlike macOS, needs no new runner type — `ubuntu-latest`
   already has a real systemd. This is also the foundation #957's smoke test can build on.

## Relationship to other issues (for context, not in scope here)

- **#944** — macOS mirror of this issue. Owned by @huang195. Already has real end-to-end
  verification behind it (the KeepAlive finding, bootout-race fix #880) that Linux still
  lacks — worth mining `cmd_service_platform.go`'s darwin comments and tests as a template
  for what "verified" should look like on Linux.
- **#955** — unattended agent workloads / headless capture path, owned by @esnible. Both
  #956 and #957 need it for the "non-zero token count" smoke-test assertion. Not required
  for #945 itself.
- **#964** — manual reboot verification (macOS + Linux), explicitly deferred from #944/#945
  because CI runners don't reboot. Shares the exact "does Restart=on-failure/launchd
  KeepAlive really survive a restart" question raised above — worth coordinating rather than
  duplicating.
- **#966** — plugin build-tag convention cleanup (retired `exclude_plugin_*` form →
  `include_plugin_*`; allow-legacy-plugin-tag: this reference is design history, not a
  live usage)
  and a smaller desktop artifact. Touches the same install/upgrade path (H2 in that issue:
  upgrading to a build that dropped a plugin an existing config still names must be handled
  gracefully) — flagged there as something #944/#945/#964 must cover. Worth checking in on
  this once the plugin-set change lands, since it could break an assumption made above about
  upgrade always being config-preserving.
