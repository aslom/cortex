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
		"--user", "run", "--unit=" + name,
		"-p", "Restart=on-failure",
		"-p", "RestartSec=1",
		script,
	}
	if out, err := exec.Command("systemd-run", args...).CombinedOutput(); err != nil {
		t.Fatalf("systemd-run: %v: %s", err, strings.TrimSpace(string(out)))
	}
}

// unitMainPID polls for a MainPID, since it is briefly 0 right after start.
func unitMainPID(t *testing.T, name string, within time.Duration) (int, bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		out, err := exec.Command("systemctl", "--user", "show", name, "--property=MainPID", "--value").Output()
		if err == nil {
			if pid, perr := strconv.Atoi(strings.TrimSpace(string(out))); perr == nil && pid > 0 {
				return pid, true
			}
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

	if !waitUntil(10*time.Second, func() bool { return unitIsActive(unit) }) {
		t.Error("unit did not report active again after the crash — Restart=on-failure did not fire")
	}

	newPID, ok := unitMainPID(t, unit, 5*time.Second)
	if !ok {
		t.Fatal("restarted unit never reported a main PID")
	}
	if newPID == pid {
		t.Error("same PID after the crash — nothing actually restarted")
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
