package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This file's two tests deliberately don't mirror TestWaitBootedOut_RealLaunchd's
// shape, because the two platforms don't need the same thing proved.
//
// macOS: launchd does not reliably restart an agent added mid-session (measured
// and documented on renderUnitFor's darwin branch), so this codebase runs its own
// supervisor process (supervise.go) and has launchd supervise THAT instead.
// TestWaitBootedOut_RealLaunchd proves our own supervisor's bootout/restart
// handling — a mechanism this repo had to build because launchd would not do it.
//
// Linux: systemd's Restart=on-failure is trusted to work natively, so
// renderUnitFor's linux branch runs the proxy directly — one process, no
// supervisor. The two tests below instead prove systemd's OWN restart mechanism
// actually behaves as documented: TestSupervisorRestartsAfterCrash_RealSystemd is
// a claim about systemd, not about code this repo wrote — which is also why it's
// simpler than the darwin test: there's no supervisor layer or bootout race to
// reproduce, just the bare Restart=on-failure claim itself.

// requireRealSystemd skips (or, with ABCTL_SYSTEMD_TESTS=required, fails) unless
// this process can actually drive a live systemd --user session — mirroring the
// four skip guards in TestWaitBootedOut_RealLaunchd (cmd_service_bootout_test.go).
//
// That macOS test's env-var escape hatch exists because silent skipping is exactly
// how the bootout-race bug (#880) shipped unexercised. Its own workflow never sets
// the var, though, so the test has skipped in every CI run since it was written.
// The one CI job that runs THIS test should set ABCTL_SYSTEMD_TESTS=required after
// setting up a real systemd --user session, so it can't fall into the same trap.
func requireRealSystemd(t *testing.T) {
	t.Helper()
	skip := t.Skipf
	if os.Getenv("ABCTL_SYSTEMD_TESTS") == "required" {
		skip = t.Fatalf
	}
	if runtime.GOOS != "linux" {
		skip("systemd only (GOOS=%s)", runtime.GOOS)
		return
	}
	for _, bin := range []string{"systemctl", "systemd-run"} {
		if _, err := exec.LookPath(bin); err != nil {
			skip("%s not on PATH: %v", bin, err)
			return
		}
	}
	if out, err := exec.Command("systemctl", "--user", "show-environment").CombinedOutput(); err != nil {
		skip("no reachable systemd --user session: %v: %s", err, strings.TrimSpace(string(out)))
		return
	}
}

// slowScript writes a throwaway script that mimics the real proxy's graceful
// shutdown: it ignores nothing, but takes a couple of seconds to actually exit
// once asked to, and otherwise just idles. A trivial script that died instantly
// would hide a slow-teardown bug the same way it did for the darwin bootout race
// (see the comment on TestWaitBootedOut_RealLaunchd).
func slowScript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "slow.sh")
	body := "#!/bin/sh\ntrap 'sleep 2; exit 0' TERM\nwhile :; do sleep 1; done\n"
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	return path
}

// runTransientUnit starts script under a throwaway, uniquely-named unit with the
// same Restart=on-failure/RestartSec our real renderUnitFor writes, and registers
// its own teardown — stop and reset-failed, so a failed assertion never leaves a
// unit respawning after the test process exits.
func runTransientUnit(t *testing.T, name, script string) {
	t.Helper()
	stop := func() {
		_ = exec.Command("systemctl", "--user", "stop", name).Run()         //nolint:errcheck
		_ = exec.Command("systemctl", "--user", "reset-failed", name).Run() //nolint:errcheck
	}
	t.Cleanup(stop)
	stop() // in case a previous, aborted run of this test left it behind

	args := []string{
		"--user", "--unit=" + name,
		"-p", "Restart=on-failure",
		"-p", "RestartSec=1",
		script,
	}
	if out, err := exec.Command("systemd-run", args...).CombinedOutput(); err != nil {
		t.Fatalf("systemd-run: %v: %s", err, strings.TrimSpace(string(out)))
	}
}

// currentMainPID reads MainPID once, or 0 if unset/unparseable.
func currentMainPID(name string) int {
	out, err := exec.Command("systemctl", "--user", "show", name, "--property=MainPID", "--value").Output()
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0
	}
	return pid
}

// unitMainPID polls for a MainPID, since it is briefly 0 right after start.
func unitMainPID(t *testing.T, name string, within time.Duration) (int, bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if pid := currentMainPID(name); pid > 0 {
			return pid, true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return 0, false
}

// waitForNewMainPID polls until MainPID is both nonzero and different from oldPID.
// Checking is-active alone is not enough: systemd can still report a unit "active"
// in the brief window right after a kill, before it has noticed the death and
// respawned — is-active going true first, then a same-old-PID read right behind
// it, would misreport a real restart as a failure to restart. Requiring a genuinely
// new PID is the direct claim ("something new is running"), not the reachable proxy
// for it.
func waitForNewMainPID(t *testing.T, name string, oldPID int, within time.Duration) (int, bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if pid := currentMainPID(name); pid > 0 && pid != oldPID {
			return pid, true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return 0, false
}

func unitIsActive(name string) bool {
	out, err := exec.Command("systemctl", "--user", "is-active", name).Output()
	return err == nil && strings.TrimSpace(string(out)) == "active"
}

func waitUntil(within time.Duration, ok func() bool) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if ok() {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

// TestSupervisorRestartsAfterCrash_RealSystemd drives a real systemd --user session
// to prove Restart=on-failure actually restarts a crashed unit — the assumption
// renderUnitFor's comment states ("systemd needs no such trade: on-failure covers
// signal death") but this repo had never verified against a real systemd, unlike the
// darwin KeepAlive claim, which WAS tested and found false (see the comment on
// renderUnitFor's darwin branch, and TestWaitBootedOut_RealLaunchd).
func TestSupervisorRestartsAfterCrash_RealSystemd(t *testing.T) {
	requireRealSystemd(t)

	unit := "cortex-test-crash-" + strconv.Itoa(os.Getpid()) + ".service"
	runTransientUnit(t, unit, slowScript(t))

	pid, ok := unitMainPID(t, unit, 5*time.Second)
	if !ok {
		t.Fatal("unit never reported a main PID")
	}

	// -9, not a plain stop: this must bypass the script's own TERM trap entirely,
	// so it looks like a real crash (a segfault, an OOM kill) rather than a
	// deliberate, distinguishable stop — which the next test proves does NOT restart.
	if err := exec.Command("kill", "-9", strconv.Itoa(pid)).Run(); err != nil {
		t.Fatalf("kill -9 %d: %v", pid, err)
	}

	// The authoritative signal is a genuinely new PID, not is-active: is-active can
	// still read "active" in the brief window right after the kill, before systemd
	// has noticed the death and respawned, which would let a same-old-PID read slip
	// through as a false "it restarted."
	if _, ok := waitForNewMainPID(t, unit, pid, 10*time.Second); !ok {
		t.Fatal("no new main PID within 10s — Restart=on-failure did not fire")
	}
	if !unitIsActive(unit) {
		t.Error("got a new PID but the unit does not report active")
	}
}

// TestSupervisorStaysStoppedAfterDeliberateStop_RealSystemd proves the other half
// of the same comment: "a `systemctl stop` is distinguishable from a crash, so a
// stop stays stopped." Restart=on-failure must NOT fire for a deliberate stop, or
// `abctl service stop` would look exactly like the "stop that does not stop" bug
// this whole feature exists to avoid on the launchd side.
func TestSupervisorStaysStoppedAfterDeliberateStop_RealSystemd(t *testing.T) {
	requireRealSystemd(t)

	unit := "cortex-test-stop-" + strconv.Itoa(os.Getpid()) + ".service"
	runTransientUnit(t, unit, slowScript(t))

	if _, ok := unitMainPID(t, unit, 5*time.Second); !ok {
		t.Fatal("unit never reported a main PID")
	}

	if out, err := exec.Command("systemctl", "--user", "stop", unit).CombinedOutput(); err != nil {
		t.Fatalf("systemctl --user stop: %v: %s", err, strings.TrimSpace(string(out)))
	}

	// The script's own TERM trap takes ~2s; give it real room, then assert it STAYS
	// inactive rather than just checking once immediately after stop returns.
	if waitUntil(1*time.Second, func() bool { return unitIsActive(unit) }) {
		t.Fatal("unit is active immediately after a deliberate stop")
	}
	time.Sleep(4 * time.Second) // past RestartSec=1; a wrongly-firing restart would show by now
	if unitIsActive(unit) {
		t.Error("unit restarted after a deliberate `systemctl stop` — Restart=on-failure should not cover this")
	}
}
