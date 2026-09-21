package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSystemctl puts a systemctl on PATH that behaves however body says, mirroring
// fakeLaunchctl (cmd_service_restricted_test.go) for the Linux side of the same
// functions. loadService, controlService, supervisorRunning and unloadService all
// shell out to the real systemctl; until now nothing exercised their reaction to
// systemd's actual vocabulary (active/activating/failed/deactivating, or a
// daemon-reload/enable failure) — only the generated unit TEXT was tested.
//
// All tests in this file pass "linux" explicitly to the functions under test,
// rather than relying on runtime.GOOS, so they run for real on any host —
// including the one these were written on.
func fakeSystemctl(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(body), 0o700); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// fakeLoginctl puts a loginctl on PATH that behaves however body says.
func fakeLoginctl(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "loginctl"), []byte(body), 0o700); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// noSystemctlOnPath points PATH at an empty directory, so exec.LookPath("systemctl")
// fails the same way it would on a box with no systemd at all.
func noSystemctlOnPath(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

// callLog returns a path inside dir and a shell snippet that appends the fake's
// own arguments to it — so a test can assert exactly what our code invoked, not
// just that it received a canned answer back.
func callLog(t *testing.T) (path string, appendLine string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "calls.log")
	return path, fmt.Sprintf(`echo "$@" >> %s`, path)
}

func readCallLog(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		return nil
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

func TestSupervisorRunning_Linux(t *testing.T) {
	p := servicePathsFixture(t)

	t.Run("active reads as running", func(t *testing.T) {
		fakeSystemctl(t, "#!/bin/sh\necho 'active'\nexit 0\n")
		running, why := supervisorRunning("linux", p)
		if !running || why != "is-active = active" {
			t.Errorf("running=%v why=%q, want true/%q", running, why, "is-active = active")
		}
	})

	// systemctl is-active exits non-zero for every state except "active", but still
	// prints the real state on stdout. Only the exit-0 case may read as running.
	for _, state := range []string{"activating", "failed", "deactivating", "inactive"} {
		t.Run(state+" does not read as running", func(t *testing.T) {
			fakeSystemctl(t, "#!/bin/sh\necho '"+state+"'\nexit 3\n")
			running, why := supervisorRunning("linux", p)
			if running {
				t.Errorf("state=%s read as running", state)
			}
			if why != "is-active = "+state {
				t.Errorf("why = %q, want %q", why, "is-active = "+state)
			}
		})
	}

	t.Run("no systemctl on PATH does not block", func(t *testing.T) {
		noSystemctlOnPath(t)
		running, why := supervisorRunning("linux", p)
		if !running || why != "" {
			t.Errorf("running=%v why=%q, want true/\"\" — a box with no systemd must not "+
				"be reported as not-running", running, why)
		}
	})

	t.Run("systemctl present but silent on failure", func(t *testing.T) {
		fakeSystemctl(t, "#!/bin/sh\nexit 1\n")
		running, why := supervisorRunning("linux", p)
		if running || why != "systemctl is-active gave no answer" {
			t.Errorf("running=%v why=%q, want false/%q", running, why, "systemctl is-active gave no answer")
		}
	})
}

func TestLoadService_Linux(t *testing.T) {
	t.Run("no systemctl on PATH", func(t *testing.T) {
		p := servicePathsFixture(t)
		noSystemctlOnPath(t)
		err := loadService("linux", p, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "systemctl not found") {
			t.Errorf("err = %v, want it to say systemctl was not found", err)
		}
	})

	t.Run("daemon-reload failure is reported, enable is never attempted", func(t *testing.T) {
		p := servicePathsFixture(t)
		fakeSystemctl(t, `#!/bin/sh
if [ "$2" = "daemon-reload" ]; then
  echo "boom: unit has a syntax error" >&2
  exit 1
fi
echo "enable --now should not have run" >&2
exit 1
`)
		err := loadService("linux", p, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "daemon-reload") || !strings.Contains(err.Error(), "boom") {
			t.Errorf("err = %v, want it to name daemon-reload and the underlying reason", err)
		}
	})

	t.Run("enable --now failure is reported", func(t *testing.T) {
		p := servicePathsFixture(t)
		fakeSystemctl(t, `#!/bin/sh
if [ "$2" = "daemon-reload" ]; then
  exit 0
fi
echo "nope: unit not found" >&2
exit 1
`)
		err := loadService("linux", p, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "enable") || !strings.Contains(err.Error(), "nope") {
			t.Errorf("err = %v, want it to name enable --now and the underlying reason", err)
		}
	})

	t.Run("linger already enabled: skipped, no marker written", func(t *testing.T) {
		p := servicePathsFixture(t)
		fakeSystemctl(t, "#!/bin/sh\nexit 0\n")
		fakeLoginctl(t, `#!/bin/sh
if [ "$1" = "show-user" ]; then
  echo "Linger=yes"
  exit 0
fi
echo "enable-linger should not have run" >&2
exit 1
`)
		if err := loadService("linux", p, io.Discard); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, err := os.Stat(lingerMarker(p)); err == nil {
			t.Error("marker written even though linger was already on")
		}
	})

	t.Run("linger not enabled, enable-linger succeeds: marker written", func(t *testing.T) {
		p := servicePathsFixture(t)
		fakeSystemctl(t, "#!/bin/sh\nexit 0\n")
		fakeLoginctl(t, `#!/bin/sh
if [ "$1" = "show-user" ]; then
  echo "Linger=no"
  exit 0
fi
exit 0
`)
		if err := loadService("linux", p, io.Discard); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, err := os.Stat(lingerMarker(p)); err != nil {
			t.Error("no marker written after abctl enabled linger itself")
		}
	})

	t.Run("linger not enabled, enable-linger fails: caveat, not fatal, no marker", func(t *testing.T) {
		p := servicePathsFixture(t)
		fakeSystemctl(t, "#!/bin/sh\nexit 0\n")
		fakeLoginctl(t, `#!/bin/sh
if [ "$1" = "show-user" ]; then
  echo "Linger=no"
  exit 0
fi
exit 1
`)
		err := loadService("linux", p, io.Discard)
		if !errors.Is(err, errLingerUnavailable) {
			t.Errorf("err = %v, want errLingerUnavailable", err)
		}
		if _, serr := os.Stat(lingerMarker(p)); serr == nil {
			t.Error("marker written even though enable-linger failed")
		}
	})
}

func TestUnloadService_Linux(t *testing.T) {
	t.Run("no systemctl on PATH: nil, nothing attempted", func(t *testing.T) {
		p := servicePathsFixture(t)
		noSystemctlOnPath(t)
		if err := unloadService("linux", p); err != nil {
			t.Errorf("err = %v, want nil — nothing could have been loaded", err)
		}
	})

	t.Run("no marker: loginctl is never called", func(t *testing.T) {
		p := servicePathsFixture(t)
		logPath, logLine := callLog(t)
		fakeLoginctl(t, "#!/bin/sh\n"+logLine+"\nexit 0\n")
		fakeSystemctl(t, "#!/bin/sh\nexit 0\n")
		if err := unloadService("linux", p); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if calls := readCallLog(t, logPath); len(calls) != 0 {
			t.Errorf("loginctl called %v times, want 0 — no marker means we never enabled linger", len(calls))
		}
	})

	t.Run("marker present: disable-linger runs and marker is removed", func(t *testing.T) {
		p := servicePathsFixture(t)
		if err := os.WriteFile(lingerMarker(p), []byte("enabled by abctl\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		logPath, logLine := callLog(t)
		fakeLoginctl(t, "#!/bin/sh\n"+logLine+"\nexit 0\n")
		fakeSystemctl(t, "#!/bin/sh\nexit 0\n")
		if err := unloadService("linux", p); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		calls := readCallLog(t, logPath)
		if len(calls) != 1 || !strings.Contains(calls[0], "disable-linger") {
			t.Errorf("loginctl calls = %v, want exactly one disable-linger", calls)
		}
		if _, err := os.Stat(lingerMarker(p)); err == nil {
			t.Error("marker still present after unload")
		}
	})

	t.Run("disable --now failure is reported", func(t *testing.T) {
		p := servicePathsFixture(t)
		fakeSystemctl(t, `#!/bin/sh
echo "unit not loaded" >&2
exit 1
`)
		err := unloadService("linux", p)
		if err == nil || !strings.Contains(err.Error(), "disable") || !strings.Contains(err.Error(), "unit not loaded") {
			t.Errorf("err = %v, want it to name disable --now and the underlying reason", err)
		}
	})
}

func TestLingerEnabled(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"explicit yes", "#!/bin/sh\necho 'Linger=yes'\nexit 0\n", true},
		{"explicit no", "#!/bin/sh\necho 'Linger=no'\nexit 0\n", false},
		// A parse failure reads as "already on" — errs toward leaving the user's
		// setting alone rather than us silently flipping it.
		{"malformed output reads as already on", "#!/bin/sh\necho 'garbage'\nexit 0\n", true},
		{"loginctl itself fails: reads as already on", "#!/bin/sh\nexit 1\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeLoginctl(t, tc.body)
			if got := lingerEnabled("501"); got != tc.want {
				t.Errorf("lingerEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestControlService_Linux(t *testing.T) {
	p := servicePathsFixture(t)

	t.Run("no systemctl on PATH", func(t *testing.T) {
		noSystemctlOnPath(t)
		err := controlService("linux", "stop", p, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "systemctl not found") {
			t.Errorf("err = %v, want it to say systemctl was not found", err)
		}
	})

	// stop and start are the PERSISTENT forms (disable/enable --now), so a stop
	// survives a reboot the same way launchd's bootout+disable pairing does; only
	// restart stays transient. Nothing previously proved the right verb went with
	// the right action.
	for _, tc := range []struct {
		action string
		want   string
	}{
		{"stop", "--user disable --now cortex.service"},
		{"start", "--user enable --now cortex.service"},
		{"restart", "--user restart cortex.service"},
	} {
		t.Run(tc.action+" invokes the right systemctl verb", func(t *testing.T) {
			logPath, logLine := callLog(t)
			fakeSystemctl(t, "#!/bin/sh\n"+logLine+"\nexit 0\n")
			if err := controlService("linux", tc.action, p, io.Discard); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			calls := readCallLog(t, logPath)
			if len(calls) != 1 || calls[0] != tc.want {
				t.Errorf("systemctl called with %v, want exactly [%q]", calls, tc.want)
			}
		})
	}

	t.Run("underlying failure is reported with the exact command and reason", func(t *testing.T) {
		fakeSystemctl(t, `#!/bin/sh
echo "kaboom" >&2
exit 1
`)
		err := controlService("linux", "stop", p, io.Discard)
		if err == nil ||
			!strings.Contains(err.Error(), "--user disable --now cortex.service") ||
			!strings.Contains(err.Error(), "kaboom") {
			t.Errorf("err = %v, want it to name the exact systemctl invocation and stderr", err)
		}
	})
}
