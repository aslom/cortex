package main

import (
	"os"
	"strings"
	"testing"
)

// TestUnitStampsRoundTrip covers both renderings, because only one of them runs on
// any given machine and the two spellings are the obvious place for them to drift.
func TestUnitStampsRoundTrip(t *testing.T) {
	p := servicePathsFixture(t)
	want := binarySHA256(p.binary)
	if want == "" {
		t.Fatal("the fixture binary did not hash; the rest of this proves nothing")
	}

	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			unit := renderUnitFor(goos, p)
			if !strings.Contains(unit, want) {
				t.Errorf("unit carries no proxy hash:\n%s", unit)
			}
			if err := os.WriteFile(p.unitFile, []byte(unit), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := unitProxySHA256(p.unitFile); got != want {
				t.Errorf("unitProxySHA256 = %q, want %q", got, want)
			}
			// The version stamp still reads, so generalizing the scanner did not
			// break the field it was written for.
			if got := unitWriterVersion(p.unitFile); got != version {
				t.Errorf("unitWriterVersion = %q, want %q", got, version)
			}
		})
	}
}

// TestProxyBinaryUnchanged_NoticesAReplacedBinary is the regression this stamp
// exists for.
//
// Path, config, version and supervisor state all stay identical across a rebuild —
// so before the hash, every clause of serviceIsCurrent passed and install reported
// "Already current" while the old build kept serving. That is the exact failure a dev
// loop hits on every iteration.
func TestProxyBinaryUnchanged_NoticesAReplacedBinary(t *testing.T) {
	p := servicePathsFixture(t)
	if err := os.WriteFile(p.unitFile, []byte(renderUnit(p)), 0o600); err != nil {
		t.Fatal(err)
	}

	if !proxyBinaryUnchanged(p) {
		t.Fatal("freshly written unit does not match its own binary")
	}

	// Rebuild: same path, different bytes.
	if err := os.WriteFile(p.binary, []byte("#!/bin/sh\nsleep 31\n"), 0o755); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
	if proxyBinaryUnchanged(p) {
		t.Error("a replaced binary still reads as unchanged; install would skip the restart")
	}
}

// TestProxyBinaryUnchanged_TreatsAnUnstampedUnitAsChanged pins the direction of the
// default. Reading an absent stamp as "unchanged" would make the first dev install
// after this upgrade silently skip its restart — the one run where the binary has
// certainly moved.
func TestProxyBinaryUnchanged_TreatsAnUnstampedUnitAsChanged(t *testing.T) {
	p := servicePathsFixture(t)
	legacy := "<?xml version=\"1.0\"?>\n<plist><dict>\n" +
		"  <key>AbctlVersion</key><string>" + version + "</string>\n" +
		"</dict></plist>\n"
	if err := os.WriteFile(p.unitFile, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if proxyBinaryUnchanged(p) {
		t.Error("a unit with no proxy hash read as unchanged")
	}
}

// TestProxyBinaryUnchanged_MissingBinaryIsChanged guards the "" collision: a binary
// that cannot be read hashes to "", and so does an absent stamp. Comparing them as
// equal would call a vanished binary current.
func TestProxyBinaryUnchanged_MissingBinaryIsChanged(t *testing.T) {
	p := servicePathsFixture(t)
	if err := os.WriteFile(p.unitFile, []byte(renderUnit(p)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p.binary); err != nil {
		t.Fatal(err)
	}
	if proxyBinaryUnchanged(p) {
		t.Error("a missing binary read as unchanged")
	}
}

// TestInstallCanSkip covers the guard --restart hangs off, including that the health
// probe stays lazy: evaluating it when the answer is already known would spend
// serviceIsCurrent's 2s budget on every run that had decided to restart anyway.
func TestInstallCanSkip(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		configChanged, restart  bool
		current                 bool
		wantSkip, wantProbeCall bool
	}{
		{name: "nothing changed", current: true, wantSkip: true, wantProbeCall: true},
		{name: "not current", current: false, wantSkip: false, wantProbeCall: true},
		{name: "--restart overrides current", restart: true, current: true},
		{name: "--restart with nothing current", restart: true},
		{name: "config changed", configChanged: true, current: true},
		{name: "config changed and --restart", configChanged: true, restart: true, current: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probed := false
			got := installCanSkip(tc.configChanged, tc.restart, func() bool {
				probed = true
				return tc.current
			})
			if got != tc.wantSkip {
				t.Errorf("installCanSkip = %v, want %v", got, tc.wantSkip)
			}
			if probed != tc.wantProbeCall {
				t.Errorf("health probe called = %v, want %v (a needless probe costs 2s)", probed, tc.wantProbeCall)
			}
		})
	}
}

// TestServiceUsageDocumentsRestart keeps the flag discoverable. A flag that only
// exists in a Makefile recipe is one nobody finds from `abctl service --help`.
func TestServiceUsageDocumentsRestart(t *testing.T) {
	if !strings.Contains(serviceUsage, "--restart") {
		t.Error("service usage does not mention --restart")
	}
}
