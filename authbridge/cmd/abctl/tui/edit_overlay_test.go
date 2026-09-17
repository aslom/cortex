package tui

import (
	"strings"
	"testing"

	"github.com/rossoctl/cortex/authbridge/cmd/abctl/edit"
)

func TestEditOverlayRender_Fetching(t *testing.T) {
	s := editState{phase: editPhaseFetching}
	out := renderEditOverlay(s, 80, 20)
	if !strings.Contains(out, "Fetching config") {
		t.Fatalf("fetching phase missing message:\n%s", out)
	}
}

// The overlay must name the target it is actually writing. A local edit that
// announced "Fetching ConfigMap…" described a thing that does not exist, and
// the cluster path's kubelet-sync wait is two orders of magnitude wrong for a
// file the proxy already watches.
func TestEditOverlayRender_NamesTheStoresOwnTarget(t *testing.T) {
	for _, tc := range []struct {
		name       string
		store      edit.Store
		wantNoun   string
		wantWait   string
		rejectNoun string
	}{{
		name:       "cluster",
		store:      edit.ConfigMapStore{},
		wantNoun:   "Fetching ConfigMap",
		wantWait:   "kubelet",
		rejectNoun: "config file",
	}, {
		name:       "local",
		store:      edit.FileStore{Path: "/tmp/config.yaml"},
		wantNoun:   "Fetching config file",
		wantWait:   "about a second",
		rejectNoun: "ConfigMap",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got := renderEditOverlay(editState{phase: editPhaseFetching, store: tc.store}, 80, 20)
			if !strings.Contains(got, tc.wantNoun) {
				t.Errorf("fetching overlay = %q, want it to contain %q", got, tc.wantNoun)
			}
			if strings.Contains(got, tc.rejectNoun) {
				t.Errorf("fetching overlay names the other backend (%q):\n%s", tc.rejectNoun, got)
			}
			waiting := renderEditOverlay(editState{phase: editPhaseWaiting, store: tc.store}, 80, 20)
			if !strings.Contains(waiting, tc.wantWait) {
				t.Errorf("waiting overlay = %q, want the %s wait hint (%q)", waiting, tc.name, tc.wantWait)
			}
		})
	}
}

func TestEditOverlayRender_Diff(t *testing.T) {
	s := editState{
		phase: editPhaseDiff,
		diff:  "-old line\n+new line\n",
	}
	out := renderEditOverlay(s, 80, 20)
	if !strings.Contains(out, "old line") || !strings.Contains(out, "new line") {
		t.Fatalf("diff content missing:\n%s", out)
	}
	if !strings.Contains(out, "(y/N)") {
		t.Fatalf("confirm prompt missing:\n%s", out)
	}
}

func TestEditOverlayRender_Applying(t *testing.T) {
	s := editState{phase: editPhaseApplying}
	out := renderEditOverlay(s, 80, 20)
	if !strings.Contains(out, "Applying") {
		t.Fatalf("applying phase missing message:\n%s", out)
	}
}

func TestEditOverlayRender_Waiting(t *testing.T) {
	s := editState{phase: editPhaseWaiting}
	out := renderEditOverlay(s, 80, 20)
	if !strings.Contains(out, "reload") {
		t.Fatalf("waiting phase should mention reload:\n%s", out)
	}
}

func TestEditOverlayRender_Error(t *testing.T) {
	s := editState{phase: editPhaseError, err: "kubectl: forbidden"}
	out := renderEditOverlay(s, 80, 20)
	if !strings.Contains(out, "forbidden") {
		t.Fatalf("error message not surfaced:\n%s", out)
	}
}
