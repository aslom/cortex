package tui

import (
	"strings"
	"testing"
)

// orderOf reports the label line's columns for each label, and fails on any it cannot find.
//
// By index rather than by splitting the line into cells: two labels here contain spaces ("LAST
// 1H", "CACHE HIT 1H"), so splitting would have to know the cell boundaries the renderer
// computed, which is the thing under test.
func orderOf(t *testing.T, labels string, want []string) []int {
	t.Helper()
	at := make([]int, len(want))
	for i, w := range want {
		at[i] = strings.Index(labels, w)
		if at[i] < 0 {
			t.Fatalf("label %q missing from %q", w, labels)
		}
	}
	for i := 1; i < len(at); i++ {
		if at[i] <= at[i-1] {
			t.Errorf("%q is at column %d and %q at %d — the order is wrong:\n  %s",
				want[i-1], at[i-1], want[i], at[i], labels)
		}
	}
	return at
}

// THE DAY'S FIGURES, THEN THE WINDOW'S, each window cell naming its window.
//
// This is the whole of what the grouping buys, and nothing else in this package asserts it:
// TestRenderSpendBand_AlignsValuesUnderTheirLabels finds its labels with strings.Index, so it
// passes on any order and on a suffixed label alike — correctly, since what it tests is
// alignment. Without this test the band could go back to interleaving the two spans, or lose
// the suffixes, with the suite green.
func TestRenderSpendBand_GroupsTheDayBeforeTheWindow(t *testing.T) {
	s := bandSummary()
	s.HasTodaySaved, s.TodaySavedUSD = true, 0.5511 // the day's own saving, so SAVED is day-scoped
	labels := renderSpendBand(s, 120)[0]

	orderOf(t, labels, []string{"TODAY", "SAVED", "LAST 1H", "TOKENS 1H", "CACHE HIT 1H"})

	// And the day's cells carry NO span suffix: TODAY and SAVED say which day they mean by
	// being the day's, and "SAVED 1H" beside "TODAY" is the two-spans-in-one-pair defect that
	// spendSummary.TodaySavedUSD exists to prevent.
	if strings.Contains(labels, "SAVED 1H") {
		t.Errorf("the day's saving was labelled with the window: %q", labels)
	}
	if strings.Contains(labels, "TODAY 1H") {
		t.Errorf("TODAY was labelled with the window: %q", labels)
	}
}

// THE FALLBACK SAVING SITS WITH THE WINDOW, because its span decides where it goes — not its
// name. Reading it as a day figure is what put a window number next to TODAY and understated a
// day's avoided spend by 2.1x; grouping it by name rather than by span would put it back.
func TestRenderSpendBand_TheWindowSavingJoinsTheWindowGroup(t *testing.T) {
	s := bandSummary() // HasSaved without HasTodaySaved: the no-ledger fallback
	if s.HasTodaySaved {
		t.Fatal("fixture drifted: this case needs the window fallback, not the day's saving")
	}
	labels := renderSpendBand(s, 120)[0]

	orderOf(t, labels, []string{"TODAY", "LAST 1H", "SAVED 1H", "TOKENS 1H", "CACHE HIT 1H"})
}

// NO LABEL, NO SUFFIX — and no space where the suffix would have gone.
//
// spendSummary.WindowLabel is populated only when parseWindowSpan could read snap.Window, so an
// unreadable window leaves it empty with every other field intact; the Failed path builds a
// summary with no label at all. Concatenated unguarded, the labels became "LAST ", "SAVED ",
// "TOKENS ", "CACHE HIT ", and bandCell.width() charged a column for the space in the two whose
// label is the wider half. Invisible on screen, which is exactly why it needs a test.
func TestRenderSpendBand_NoWindowLabelLeavesNoTrailingSpace(t *testing.T) {
	s := bandSummary()
	s.WindowLabel = ""

	lines := renderSpendBand(s, 120)

	// The whole label line, because the defect is a WIDTH and only the exact string pins one.
	// Each cell is max(label, value) + the gutter, and with no suffix that is:
	//
	//   TODAY     5 against "$3.8402"  -> 7+2
	//   LAST      4 against "$4.5462"  -> 7+2   (the value absorbed the old trailing space)
	//   SAVED     5 against "~$0.2091" -> 8+2   (likewise; this fixture's SAVED is the fallback)
	//   TOKENS    6 against "5.6M"     -> 6+2   (the label decides, so a space cost a column)
	//   CACHE HIT 9 against "93%"      -> 9     (last cell, no gutter)
	const wantLabels = "TODAY    LAST     SAVED     TOKENS  CACHE HIT"
	if lines[0] != wantLabels {
		t.Errorf("label line\n  got  %q\n  want %q", lines[0], wantLabels)
	}
	if lines[1] != "$3.8402  $4.5462  ~$0.2091  5.6M    93%" {
		t.Errorf("values moved with the labels: %q", lines[1])
	}
}

// The suffix is the SNAPSHOT'S window, not the string "1H". A band that hardcoded it would
// mislabel every figure the moment `w` changed the span.
func TestRenderSpendBand_SuffixFollowsTheWindow(t *testing.T) {
	s := bandSummary()
	s.WindowLabel = "7d"
	labels := renderSpendBand(s, 120)[0]

	orderOf(t, labels, []string{"TODAY", "LAST 7D", "SAVED 7D", "TOKENS 7D", "CACHE HIT 7D"})
	if strings.Contains(labels, "1H") {
		t.Errorf("a 7d window still labelled cells 1H: %q", labels)
	}
}
