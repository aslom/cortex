package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// assetPath is the committed asset the README points at. GitHub cannot build the
// SVG, so it has to be in the tree.
const assetPath = "../../../docs/assets/cortex-demo.svg"

// volatile masks the parts of a capture that move with the wall clock: the
// UPDATED ages, the TIME column, the ISO timestamps in the detail pane, and the
// usage chart's axis labels.
//
// The capturer already rewrites these to fixed digits of the same length, so a
// regenerated asset is normally byte-identical. This masking is the backstop for
// the one case it cannot equalise — a digit COUNT that changes, such as an age
// crossing from 9s to 10s — and it costs little, because the check exists to catch
// the UI changing under the demo (a renamed column, a reordered pane, a reformatted
// figure) and those are all non-digit changes. TestCapture_* asserts the figures.
var volatile = regexp.MustCompile(`[0-9]+`)

func normalize(b []byte) string { return volatile.ReplaceAllString(string(b), "#") }

// TestCommittedAssetIsCurrent fails when the committed SVG no longer matches what
// demo.yaml and the current abctl produce.
//
// A fabricated demo is only worth having if it cannot quietly drift from the
// product it claims to show; this converts a TUI change that would have made the
// asset a lie into a failing check.
func TestCommittedAssetIsCurrent(t *testing.T) {
	committed, err := os.ReadFile(assetPath)
	if err != nil {
		t.Fatalf("read committed asset: %v", err)
	}

	fresh := filepath.Join(t.TempDir(), "fresh.svg")
	if err := run("demo.yaml", fresh); err != nil {
		t.Fatalf("regenerate: %v", err)
	}
	got, err := os.ReadFile(fresh)
	if err != nil {
		t.Fatal(err)
	}

	if normalize(committed) != normalize(got) {
		t.Errorf(`%s is stale.

Regenerate it:
    go -C authbridge/scripts/readme-demo run .

(committed %d bytes, regenerated %d bytes)`, assetPath, len(committed), len(got))
	}
}

// TestCommittedAssetIsWithinBudget keeps the README from growing a megabyte of
// markup unnoticed.
func TestCommittedAssetIsWithinBudget(t *testing.T) {
	info, err := os.Stat(assetPath)
	if err != nil {
		t.Fatal(err)
	}
	const budget = 500 << 10
	if info.Size() > budget {
		t.Errorf("asset is %d KB, over the %d KB budget", info.Size()>>10, budget>>10)
	}
}
