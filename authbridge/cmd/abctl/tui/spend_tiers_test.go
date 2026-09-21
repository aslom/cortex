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

// NO figure wears the inexact marker, which is the reverse of what this test used to assert.
//
// Every figure here still IS modelled — the mix is the rate table's while the total may be the
// gateway's — so the disclosure was real and was given up rather than made unnecessary. It was
// dropped because this panel has no money column HEADER to hold it, unlike the sessions table and
// the band, which left a glyph on every row as the only alternative. renderTierRows records the
// argument.
//
// Inverted rather than deleted, because the marker coming BACK is a change someone should have to
// make deliberately: it would put a tilde on every row of the panel again.
func TestRenderTierRows_MarksNoFigureInexact(t *testing.T) {
	rows := 0
	for _, line := range renderTierRows(tierCounts(), 60) {
		if line == "" {
			continue
		}
		rows++
		if strings.Contains(line, inexactMarker) {
			t.Errorf("row %q carries %q; this panel states the caveat nowhere on a row", line,
				inexactMarker)
		}
	}
	if rows == 0 {
		t.Fatal("no rows rendered, so the loop asserted nothing")
	}
}

// And they read in cents, like every other scanned money surface.
func TestRenderTierRows_FiguresReadInCents(t *testing.T) {
	joined := strings.Join(renderTierRows(tierCounts(), 60), "\n")
	// tierCounts' output tier apportions to $2.39; four decimals would render "$2.3927".
	if !strings.Contains(joined, "$2.39") {
		t.Errorf("no cents figure in:\n%s", joined)
	}
	if strings.Contains(joined, "$2.3927") {
		t.Errorf("a tier row kept four decimals:\n%s", joined)
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

// A tier absent from the modelled mix shows the unknown cell, NOT $0.0000.
//
// FOUND BY RENDERING, NOT BY A TEST, which is the lesson: tierCounts() populates all four
// tiers, so every assertion above was blind to a partial mix — and a partial mix is the
// normal case, since a window of cache-heavy traffic may report no cache WRITES at all.
// "$0.0000" in a money column asserts the tier was free, which is the lie this package
// refuses in sessionMoneyCell and in `abctl cost`'s headline.
func TestRenderTierRows_ATierAbsentFromTheMixIsUnknownNotFree(t *testing.T) {
	c := usage.Counts{
		Requests: 35, CostMicros: 4_546_200,
		InputCostMicros: 3000, OutputCostMicros: 45000, // no cache tiers in the mix
	}
	lines := renderTierRows(c, 60)
	joined := strings.Join(lines, "\n")

	// Both spellings, because the figures read in cents now and "$0.00" is the one this panel
	// can actually produce — formatUSDTotalMicros(0) returns it, so the emptyCell guard above
	// the formatter is the only thing standing between an absent tier and a claim that it was
	// free. "$0.0000" is kept in the check to catch a revert to the four-decimal formatter.
	if strings.Contains(joined, "$0.0000") || strings.Contains(joined, "$0.00") {
		t.Errorf("a tier absent from the mix rendered as free:\n%s", joined)
	}
	// The two tiers that ARE in the mix keep their figures: this must not blank the column.
	if !strings.Contains(joined, "$") {
		t.Errorf("the tiers that are in the mix lost their figures:\n%s", joined)
	}
	// And the absent ones say so.
	var unknown int
	for _, line := range lines {
		if strings.Contains(line, emptyCell) {
			unknown++
		}
	}
	if unknown != 2 {
		t.Errorf("%d rows show %q, want 2 (cache-read and cache-write):\n%s",
			unknown, emptyCell, joined)
	}
}
