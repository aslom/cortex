package tui

import (
	"strings"
	"testing"

	"github.com/rossoctl/cortex/authbridge/authlib/usage"
)

// tierCounts is one window's totals with a cache-heavy modelled mix.
//
// The mix inverts tokens against money on purpose: cache-read is the largest token tier and
// output the largest money tier. A fixture that ranked both the same way would pass against
// a renderer ordering by token count, which is the mistake this whole feature exists to fix.
func tierCounts() usage.Counts {
	return usage.Counts{
		Requests: 35, CostMicros: 4_546_200,
		InputCostMicros: 3000, CacheWriteCostMicros: 7500,
		CacheReadCostMicros: 30000, OutputCostMicros: 45000,
	}
}

// Ranked by MONEY, descending — not by pricing.Tier's declaration order, which starts with
// input, and not by token count.
func TestRenderTierRows_RanksByCostNotByDeclarationOrder(t *testing.T) {
	lines := renderTierRows(tierCounts(), 60)
	if len(lines) != numTierRows {
		t.Fatalf("lines = %d, want exactly %d: %q", len(lines), numTierRows, lines)
	}
	want := []string{"output", "cache-read", "cache-write", "input"}
	for i, w := range want {
		if !strings.Contains(lines[i], w) {
			t.Errorf("row %d = %q, want %q (full: %q)", i, lines[i], w, lines)
		}
	}
	// pricing.Tier declares input first; if that order leaked through, row 0 is input.
	if strings.HasPrefix(strings.TrimSpace(lines[0]), "input") {
		t.Errorf("row 0 is the input tier, so the rows follow declaration order, not cost")
	}
}

// Every figure wears the inexact marker, because the split is modelled throughout even
// though the column sums to a total that is not.
func TestRenderTierRows_MarksEveryFigureInexact(t *testing.T) {
	for _, line := range renderTierRows(tierCounts(), 60) {
		if line == "" {
			continue
		}
		if !strings.Contains(line, inexactMarker+"$") {
			t.Errorf("row %q carries a figure with no %q marker", line, inexactMarker)
		}
	}
}

// No mix means the "not known here" cell, never $0.00 and never a guess.
func TestRenderTierRows_NoMixRendersTheUnknownCell(t *testing.T) {
	lines := renderTierRows(usage.Counts{Requests: 35, CostMicros: 4_546_200}, 60)
	if len(lines) != numTierRows {
		t.Fatalf("lines = %d, want %d even with no mix", len(lines), numTierRows)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, emptyCell) {
		t.Errorf("no mix but no %q cell:\n%s", emptyCell, joined)
	}
	if strings.Contains(joined, "$0.00") {
		t.Errorf("rendered $0.00 for an unknown tier:\n%s", joined)
	}
}

// Reasoning is never a bar and never a figure here.
//
// It is a SUBSET of output — tokenSplit labels it "reasoning (of output)" — so a fifth row
// would double-count the same money. The fixture reports reasoning tokens precisely so a
// renderer enumerating token KINDS instead of rate TIERS fails: there are five kinds on
// Counts and four tiers, and that difference is the point.
func TestRenderTierRows_ReasoningIsNotATier(t *testing.T) {
	c := tierCounts()
	c.ReasoningTokens = 12_000
	c.OutputTokens = 42_000
	lines := renderTierRows(c, 60)
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "reasoning") {
		t.Errorf("reasoning appears as a tier row, double-counting output:\n%s", joined)
	}
	if len(lines) != numTierRows {
		t.Errorf("lines = %d, want %d — reasoning added a row", len(lines), numTierRows)
	}
}

// The line count is IDENTICAL across every state and width.
//
// The panel sits in a fixed reservation that layout() computes from the terminal height and
// cannot consult this function. A renderer whose height follows its data is what overflowed
// this pane by five rows and under-filled it by six.
func TestRenderTierRows_HeightIsConstant(t *testing.T) {
	for name, c := range map[string]usage.Counts{
		"full mix": tierCounts(),
		"no mix":   {Requests: 35, CostMicros: 4_546_200},
		"empty":    {},
		"one tier": {CostMicros: 4_546_200, OutputCostMicros: 45000},
		"negative": {CostMicros: -5, OutputCostMicros: 45000},
	} {
		for _, w := range []int{10, 20, 34, 46, 60, 100, 200} {
			got := renderTierRows(c, w)
			if len(got) != numTierRows {
				t.Errorf("%s at width %d: %d lines, want %d", name, w, len(got), numTierRows)
			}
			for i, line := range got {
				if n := len([]rune(line)); n > w {
					t.Errorf("%s at width %d: row %d is %d runes: %q", name, w, i, n, line)
				}
			}
		}
	}
}

// A negative total is not a total: the same refusal every other money surface here makes.
func TestRenderTierRows_NegativeTotalIsRefused(t *testing.T) {
	joined := strings.Join(renderTierRows(usage.Counts{
		CostMicros: -5, OutputCostMicros: 45000,
	}, 60), "\n")
	if !strings.Contains(joined, emptyCell) {
		t.Errorf("a negative total produced figures rather than %q:\n%s", emptyCell, joined)
	}
	if strings.Contains(joined, "$-") {
		t.Errorf("rendered a negative figure:\n%s", joined)
	}
}
