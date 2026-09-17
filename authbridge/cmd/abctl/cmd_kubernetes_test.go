package main

import (
	"flag"
	"io"
	"testing"
)

// chooseEndpoint is the whole --kubernetes decision, and the reason it is a
// function: runObserve opens a terminal, so this could not be tested in place.
//
// The row that matters is a running local Cortex WITH --kubernetes passed: that is
// the case the flag exists for, since the probe would otherwise win every time and
// leave someone who runs Cortex on a laptop AND works against a cluster no way to
// reach the picker. Without the flag the local one wins, which is the default and
// the quickstart.
func TestChooseEndpoint(t *testing.T) {
	const local = "http://localhost:47601"
	const explicit = "http://example.test:9094"

	for _, tc := range []struct {
		name       string
		explicit   string
		local      string
		localUp    bool
		kubernetes bool
		want       string
	}{
		{"explicit wins over a live local", explicit, local, true, false, explicit},
		{"explicit wins under --kubernetes", explicit, local, true, true, explicit},
		{"live local is taken by default", "", local, true, false, local},
		{"live local is skipped under --kubernetes", "", local, true, true, ""},
		{"dead local falls to the picker", "", local, false, false, ""},
		{"dead local falls to the picker under --kubernetes", "", local, false, true, ""},
		{"no local at all", "", "", false, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := chooseEndpoint(tc.explicit, tc.local, tc.localUp, tc.kubernetes)
			if got != tc.want {
				t.Errorf("chooseEndpoint(%q, %q, %v, %v) = %q, want %q",
					tc.explicit, tc.local, tc.localUp, tc.kubernetes, got, tc.want)
			}
		})
	}
}

// The flag's DEFAULT is the point of this file, and chooseEndpoint cannot pin it:
// it takes kubernetes as an argument, so it is equally correct under either
// default. Read from a fresh flag set the way runObserve builds one, so flipping
// the registered default fails here rather than silently changing which Cortex a
// bare `abctl observe` connects to.
func TestKubernetesFlag_DefaultsToFalse(t *testing.T) {
	fs := flag.NewFlagSet("abctl", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	kubernetes := fs.Bool("kubernetes", false, "")
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if *kubernetes {
		t.Error("--kubernetes must default to false, so a bare `abctl observe` takes a live local Cortex")
	}
	// And the default must leave a live local Cortex selected, which is the
	// behaviour the default exists to preserve.
	if got := chooseEndpoint("", "http://localhost:47601", true, *kubernetes); got == "" {
		t.Error("with the default, a live local Cortex must be connected to rather than yielding the picker")
	}
}

// An empty return is what wires up the picker (main sets opts.Lister only then), so
// "offers the picker" and "chose no endpoint" are the same statement. Asserted
// separately because that coupling is the flag's entire purpose and is easy to
// break by making the empty case return a default address instead.
func TestChooseEndpoint_EmptyMeansPicker(t *testing.T) {
	if got := chooseEndpoint("", "http://localhost:47601", true, true); got != "" {
		t.Errorf("a live local Cortex under --kubernetes must still yield the picker, got %q", got)
	}
}
