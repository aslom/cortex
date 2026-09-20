package tui

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/rossoctl/cortex/authbridge/authlib/usage"
	"github.com/rossoctl/cortex/authbridge/cmd/abctl/apiclient"
)

func TestRenderSpendStrip_NeverExceedsTheWidth(t *testing.T) {
	// The strip lives in the chrome. A line that overflows wraps, and a wrapped
	// chrome line costs a row of the table below it -- the same failure
	// fitHintLine exists to prevent.
	s := spendSummary{WindowUSD: 1.12, WindowLabel: "1h", Priced: true}
	for _, w := range []int{120, 100, 80, 64, 48, 32, 24, 16, 8, 1} {
		got := renderSpendStrip(s, w)
		if gw := lipgloss.Width(got); gw > w {
			t.Errorf("width %d: rendered %d columns: %q", w, gw, got)
		}
		if strings.Contains(got, "\n") {
			t.Errorf("width %d: strip contains a newline: %q", w, got)
		}
	}
}

func TestRenderSpendStrip_DropsWholeFiguresNeverClipsANumber(t *testing.T) {
	// #953: "no truncated numbers". A half-rendered dollar amount is worse than a
	// missing one -- it reads as a real, smaller figure.
	// TWO figures, and the second one is why: the "$0." probe below was written for the
	// per-minute burn rate, which is gone, and the fixture that replaced it set no sub-dollar
	// figure at all — so half the loop could never fire. The saving is a real sub-dollar money
	// figure and keeps the probe live.
	s := spendSummary{
		WindowUSD: 1.12, WindowLabel: "1h", Priced: true,
		SavedUSD: 0.0187, HasSaved: true,
	}
	for _, w := range []int{120, 100, 80, 64, 48, 32, 24, 16, 8} {
		got := renderSpendStrip(s, w)
		if got == "" {
			continue
		}
		// Any figure that appears at all must appear IN FULL, as formatUSDCell
		// actually renders it: "$1.1200", not "$1.12". Asserting the short form could
		// not detect clipping -- output truncated to "SPEND  $1.12" contains both
		// "$1.1" and "$1.12", so it passed, and only a clip to "$1.1" or shorter was
		// ever caught. The prefix probes stop just past the "$" so a figure clipped
		// anywhere in its digits still trips them.
		for prefix, whole := range map[string]string{
			"$1.": "$1.1200", // the window figure
			"$0.": "$0.0187", // the saving
		} {
			if strings.Contains(got, prefix) && !strings.Contains(got, whole) {
				t.Errorf("width %d: %q has a clipped %q figure (want the whole %q)", w, got, prefix, whole)
			}
		}
		if strings.HasSuffix(strings.TrimSpace(got), "$") {
			t.Errorf("width %d: %q ends mid-figure", w, got)
		}
	}
}

func TestRenderSpendStrip_TheClipAssertionCanActuallyFail(t *testing.T) {
	// Guards the guard above. That test is only worth anything if its assertion fails on clipped
	// input, so feed it clipped input directly. This is the defect class that already bit this
	// branch twice: a test that cannot detect the thing it is named for.
	//
	// DERIVED FROM formatUSDCell, not written out. With hardcoded literals this called no
	// production symbol at all — it ran strings.Contains over three string constants, so
	// deleting spend_strip.go left it green, and changing the formatter to two decimals would
	// have made the test it guards wrong while this one went on passing. The whole point is to
	// track the format, so it has to ask the formatter.
	whole := formatUSDCell(1.12)
	prefix := whole[:3] // "$1." — the probe the guarded test actually uses
	if !strings.HasPrefix(whole, prefix) || len(whole) <= len(prefix) {
		t.Fatalf("formatUSDCell(1.12) = %q, which the %q probe cannot describe: the guarded "+
			"test's prefix probes need rewriting alongside the formatter", whole, prefix)
	}

	// EVERY truncation that still trips the prefix probe, rather than three hand-picked ones:
	// a formatter with more digits gains more clipped forms, and they all have to be caught.
	for i := len(prefix); i < len(whole); i++ {
		clipped := "SPEND  " + whole[:i]
		if strings.Contains(clipped, prefix) && strings.Contains(clipped, whole) {
			t.Errorf("%q satisfied the whole-figure assertion; the clip test is blind to it", clipped)
		}
	}
	// ...and passes on the real, unclipped rendering, so it is not vacuously strict.
	if line := "SPEND  1h: " + whole; strings.Contains(line, prefix) && !strings.Contains(line, whole) {
		t.Errorf("%q failed the whole-figure assertion; the clip test rejects correct output", line)
	}
}

// The agreed line, in the agreed order: the day's spend and the day's saving, then the window
// group — its total first, then the volume readings a reader checks that total against.
//
// THE ORDER CHANGED ONCE, and this is the change: `saved` used to stand between the two money
// figures, reading as the day's partner while carrying the HOUR's number. It is now the day's own
// figure and sits inside the day's group, which is what makes the adjacency true rather than
// merely suggestive. The window's saving is the fallback and lives in the window group — see
// TestRenderSpendStrip_WindowGroupLeadsWhenThereIsNoDay.
//
// ORDER IS ASSERTED, not just presence, because the order IS the design — the ladder drops
// whole figures from the right, so position determines what a narrow terminal keeps. A test
// that only checked membership would pass with the line reversed, which would make `saved`
// the first thing lost and the least important figure the last.
func TestRenderSpendStrip_WideShowsEveryFigureInPriorityOrder(t *testing.T) {
	s := spendSummary{
		TodayUSD: 30.935, HasToday: true, TodayPriceable: 100, TodayIncomplete: 0,
		TodaySavedUSD: 0.1804, HasTodaySaved: true,
		WindowUSD: 2.91, WindowLabel: "1h", Priced: true,
		CacheHitPct: 81, HasCacheHit: true,
		Tokens: 9_890_000,
		Errors: 2,
	}
	got := renderSpendStrip(s, 200)

	// Left to right. Each must appear AFTER the previous one.
	want := []string{"SPEND", "$30.9350", "today", "saved", "~$0.1804", "1h:", "$2.9100",
		"cache 81%", "9.9M", "tokens", "2 err"}
	assertInOrder(t, got, want)
	// The saving must not have been folded into either dollar figure.
	if strings.Contains(got, "$31.1154") || strings.Contains(got, "$3.0904") {
		t.Errorf("strip %q added the saving to a spend figure", got)
	}
}

// assertInOrder fails unless every want appears in line, each after the previous one.
//
// A helper because three tests assert an order now, and because the loop they each wrote by hand
// had a bug in its FAILURE path: it reported the previous expectation as want[at-1], indexing the
// expectation slice by a byte offset into the line. On the first real ordering failure — the one
// this grouping change produced — that panicked with "index out of range [51] with length 11"
// instead of naming the two figures that had swapped.
func assertInOrder(t *testing.T, line string, want []string) {
	t.Helper()
	at := 0
	for n, w := range want {
		i := strings.Index(line[at:], w)
		if i < 0 {
			prev := "the start of the line"
			if n > 0 {
				prev = strconv.Quote(want[n-1])
			}
			t.Fatalf("strip %q is missing %q, or has it before %s", line, w, prev)
		}
		at += i + len(w)
	}
}

func TestRenderSpendStrip_NarrowKeepsTheHeadlineFigure(t *testing.T) {
	// Degradation drops from the RIGHT: the leftmost figure is the headline and is
	// the last thing to go. (fitHintLine drops from the front for the opposite
	// reason -- its essential hints are last.)
	s := spendSummary{
		WindowUSD: 1.12, WindowLabel: "1h", Priced: true,
		Tokens: 9_890_000, Errors: 2,
	}
	got := renderSpendStrip(s, 24)
	if !strings.Contains(got, "$1.12") {
		t.Errorf("narrow strip %q dropped the headline figure", got)
	}
	// A REAL trailing figure, so this cannot pass vacuously. It replaced an assertion
	// about the removed per-minute rate, which no longer renders under any width and so
	// was satisfied by the figure's non-existence rather than by the ladder.
	if strings.Contains(got, "err") {
		t.Errorf("narrow strip %q kept the error count; the tail drops before the headline", got)
	}
}

func TestRenderSpendStrip_UnpricedSaysSoAndNeverShowsZero(t *testing.T) {
	// HasSnapshot, because Priceable comes FROM a snapshot: spendSummary reads it off
	// snap.Totals, so a non-zero count with no snapshot is a state it cannot produce. The
	// renderer now checks "has a poll answered at all" before anything else, so an
	// under-specified fixture renders "" — which is right for the state it describes and
	// wrong for the state this test is about.
	s := spendSummary{WindowLabel: "1h", Priced: false, Unpriced: 12, Priceable: 318, HasSnapshot: true}
	got := renderSpendStrip(s, 120)
	if !strings.Contains(got, "unavailable") {
		t.Errorf("strip %q does not say cost is unavailable", got)
	}
	if strings.Contains(got, "$0.00") {
		t.Errorf("strip %q renders $0.00 for an unknown cost", got)
	}
}

func TestRenderSpendStrip_PartiallyPricedDisclosesTheGap(t *testing.T) {
	// A dollar total covering only the priced subset must say so; presenting a
	// subtotal as the whole spend is the failure the coverage counters exist for.
	s := spendSummary{
		WindowUSD: 4.17, WindowLabel: "1h", HasSnapshot: true,
		Priced: true, Unpriced: 12, Priceable: 318,
	}
	got := renderSpendStrip(s, 120)
	// The NOTE as coverageNote actually spells it, not a bare "12": that substring matches any
	// figure containing those digits, so it could pass on a line that disclosed nothing. One
	// form, not a disjunction — an alternative the producer cannot emit is a dead probe.
	if !strings.Contains(got, "12 of 318 unpriced") {
		t.Errorf("strip %q does not disclose the 12 unpriced requests", got)
	}
	// And the figure it qualifies is still there, so this cannot pass on an empty line.
	if !strings.Contains(got, "$4.1700") {
		t.Errorf("strip %q lost the figure the gap is about", got)
	}
}

func TestRenderSpendStrip_FullyPricedIsNotAnnotated(t *testing.T) {
	// A correctly configured deployment must not carry a permanent warning; that
	// is what trains an operator to ignore the one signal that matters.
	s := spendSummary{
		WindowUSD: 4.17, WindowLabel: "1h", HasSnapshot: true,
		Priced: true, Unpriced: 0, Priceable: 318,
	}
	got := renderSpendStrip(s, 120)
	// ANCHORED FIRST. "Does not contain 'unpriced'" is satisfied by an empty string, so without
	// this a regression that suppressed the whole line would read as a clean deployment.
	if !strings.Contains(got, "$4.1700") {
		t.Fatalf("strip %q did not render the figure at all, so the absence below proves nothing", got)
	}
	if strings.Contains(got, "unpriced") {
		t.Errorf("fully priced strip %q still warns about coverage", got)
	}
}

func TestRenderSpendStrip_NoDataYetRendersNothingUseful(t *testing.T) {
	// Before the first poll returns. An empty strip is honest; "$0.00" is not.
	got := renderSpendStrip(spendSummary{}, 120)
	// EXACTLY empty, which is the documented answer for "no poll has answered yet" — asserted
	// as an equality rather than as the absence of one substring, because absence of "$0.00" is
	// also satisfied by every wrong non-empty line this could have produced.
	if got != "" {
		t.Errorf("strip = %q before any poll answered, want empty: the only state where silence "+
			"is honest is the one where nothing has been looked at", got)
	}
}

func TestRenderSpendStrip_NoSavedFigureWhenUnmeasured(t *testing.T) {
	s := spendSummary{WindowUSD: 1.12, WindowLabel: "1h", Priced: true, HasSaved: false}
	if got := renderSpendStrip(s, 120); strings.Contains(got, "saved") {
		t.Errorf("strip %q shows a saved figure with nothing measuring it", got)
	}
}

func TestRenderSpendStrip_ShowsSavedWhenMeasured(t *testing.T) {
	// Proves the field is wired now so a later commit adds data, not a branch.
	s := spendSummary{
		WindowUSD: 1.12, WindowLabel: "1h", Priced: true,
		SavedUSD: 0.24, HasSaved: true,
	}
	got := renderSpendStrip(s, 120)
	if !strings.Contains(got, "saved") || !strings.Contains(got, "$0.24") {
		t.Errorf("strip %q does not show the measured saving", got)
	}
}

// THE SAVING BELONGS TO THE SPAN IT SITS BESIDE. The day's saving is rendered in the day's
// group; the hour's is not rendered at all while the day's exists, because two saved figures on
// one line is how a reader learns to read neither.
//
// The figures are the ones a local proxy served at one instant: the hour had avoided $1.0291 and
// the day $2.1891, so publishing the hour's beside "today" understated the day by 2.1x.
func TestRenderSpendStrip_SavedFigureBelongsToTheDayItSitsBeside(t *testing.T) {
	s := spendSummary{
		TodayUSD: 64.1765, HasToday: true, TodayPriceable: 537,
		TodaySavedUSD: 2.1891, HasTodaySaved: true,
		SavedUSD: 1.0291, HasSaved: true,
		WindowUSD: 36.5723, WindowLabel: "1h", Priced: true, Priceable: 290,
		CacheHitPct: 99, HasCacheHit: true,
		Tokens: 84_576_928,
	}
	got := renderSpendStrip(s, 200)

	if !strings.Contains(got, "saved ~$2.1891") {
		t.Errorf("strip %q does not carry the DAY's saving beside the day's cost", got)
	}
	// The hour's saving must not appear anywhere: the day's is the one on the line, and a second
	// saved figure four columns to the right would be the same misattribution in reverse.
	if strings.Contains(got, "$1.0291") {
		t.Errorf("strip %q publishes the hour's saving as well as the day's", got)
	}
	// Left to right: the day's cost, then the day's saving, then the window group.
	assertInOrder(t, got, []string{"$64.1765", "today", "saved ~$2.1891", "1h:", "$36.5723"})
}

// ONE LABEL FOR THE GROUP, not one per figure. The window's cost, its cache ratio and its token
// count are all the same span, and labelling each would spend three times the width to say one
// thing — on the line whose entire design problem is width.
//
// The prefix costs exactly what the old suffix cost: "1h: $1.1200" and "$1.1200 /1h" are both
// eleven columns, so the documented width at which the strip falls silent does not move.
func TestRenderSpendStrip_LabelsTheWindowGroupOnceAsAPrefix(t *testing.T) {
	s := spendSummary{
		TodayUSD: 64.1765, HasToday: true, TodayPriceable: 537,
		WindowUSD: 36.5723, WindowLabel: "1h", Priced: true, Priceable: 290,
		CacheHitPct: 99, HasCacheHit: true,
		Tokens: 84_576_928,
		Errors: 2,
	}
	got := renderSpendStrip(s, 200)

	if !strings.Contains(got, "1h: $36.5723") {
		t.Errorf("strip %q does not introduce the window group with its span", got)
	}
	// The trailing form is gone: it labelled the money figure and left the three volume figures
	// after it wearing nothing, which is what let a reader compare the hour's token count against
	// a day's total and a lifetime table.
	if strings.Contains(got, "/1h") {
		t.Errorf("strip %q still suffixes a figure with its span; the group prefix replaces it", got)
	}
	// And the volume figures are INSIDE the group, after the label.
	// "84M", not "84.6M": humanizeCount renders a whole number of millions from 10M up, which is
	// also why the line the reader reported said "85M tokens" for 85.0-85.9M.
	label := strings.Index(got, "1h:")
	for _, w := range []string{"cache 99%", "84M", "2 err"} {
		if i := strings.Index(got, w); i < label {
			t.Errorf("strip %q renders %q ahead of the window label, so it reads as the day's", got, w)
		}
	}
}

// WITH NO DAY FIGURE the window group leads the line, and it still says which span it is. This is
// every Kubernetes deployment: no durable ledger, so applyTodayFigure never sets HasToday.
func TestRenderSpendStrip_WindowGroupLeadsWhenThereIsNoDay(t *testing.T) {
	s := spendSummary{
		WindowUSD: 36.5723, WindowLabel: "1h", Priced: true, Priceable: 290,
		SavedUSD: 1.0291, HasSaved: true,
		Tokens: 84_576_928,
	}
	got := renderSpendStrip(s, 200)

	if !strings.Contains(got, "1h: $36.5723") {
		t.Errorf("strip %q lost the span label when the window group led the line", got)
	}
	// The window's saving is the fallback, and its partner is the figure immediately before it.
	if !strings.Contains(got, "saved ~$1.0291") {
		t.Errorf("strip %q dropped the window's saving, which is the only one it has", got)
	}
	if strings.Contains(got, "today") {
		t.Errorf("strip %q mentions a day it has no figure for", got)
	}
}

// The group label survives to the narrowest width that renders a figure at all, because it rides
// ON the figure rather than beside it — the same rule the partiality markers follow. A bare
// "$36.5723" on a narrow terminal would be a figure of unknown span.
func TestRenderSpendStrip_NarrowKeepsTheSpanWithTheFigure(t *testing.T) {
	s := spendSummary{
		WindowUSD: 1.12, WindowLabel: "1h", Priced: true, Priceable: 290,
		Tokens: 84_576_928,
	}
	// 11 columns is the whole figure with its prefix and no room for the "SPEND" label, which is
	// the documented floor: fitStripFigures drops the label before it drops a number. It is also
	// the exact figure renderSpendStrip's own doc measures — "$1.1200 /1h" is 11 columns, and
	// "1h: $1.1200" is 11 too, which is the arithmetic that makes the prefix width-neutral.
	got := renderSpendStrip(s, 11)
	if got != "1h: $1.1200" {
		t.Errorf("strip at width 11 = %q, want %q — the span must not be what gets dropped",
			got, "1h: $1.1200")
	}
	// One column narrower drops the whole figure rather than clipping it or shedding the span.
	if got := renderSpendStrip(s, 10); got != "" {
		t.Errorf("strip at width 10 = %q, want empty: a clipped figure is a wrong figure", got)
	}
}

func TestRenderSpendStrip_ShowsTodayWhenAvailable(t *testing.T) {
	s := spendSummary{
		WindowUSD: 1.12, WindowLabel: "1h", Priced: true,
		TodayUSD: 4.17, HasToday: true,
	}
	got := renderSpendStrip(s, 120)
	if !strings.Contains(got, "today") || !strings.Contains(got, "$4.17") {
		t.Errorf("strip %q does not show today's total", got)
	}
	// Today becomes the headline when present, so it must survive a narrow width.
	if narrow := renderSpendStrip(s, 24); !strings.Contains(narrow, "$4.17") {
		t.Errorf("narrow strip %q dropped today's total, which is the headline", narrow)
	}
}

func TestRenderSpendStrip_WideCharacterSafety(t *testing.T) {
	// footer.go:88-92 records the bug: a budget computed in display columns but
	// sliced by rune index overflowed on any wide character. The strip's own chrome is
	// ASCII, but the width arithmetic must be column-based regardless.
	//
	// It is not hypothetical either: WindowLabel is server-supplied and reaches the line
	// verbatim whenever parseWindowSpan cannot read it as a duration (spend.go), so the wide
	// character can arrive off the wire. The ASCII case alone proved only the loop bound —
	// this test was named for behaviour it never exercised. Each CJK glyph below is TWO
	// display columns and ONE rune, so any len()- or rune-based budget renders wider than it
	// claims and these cases catch it.
	for _, tc := range []struct {
		name string
		s    spendSummary
	}{
		{"ascii", spendSummary{WindowUSD: 1.12, WindowLabel: "1h", Priced: true}},
		{"cjk window label", spendSummary{WindowUSD: 1.12, WindowLabel: "一時間", Priced: true}},
		{"cjk label under a today headline", spendSummary{
			WindowUSD: 1.12, WindowLabel: "過去一時間", Priced: true,
			TodayUSD: 4.17, HasToday: true,
		}},
		{"cjk in the unpriced path", spendSummary{
			WindowLabel: "過去一時間", HasSnapshot: true, Unpriced: 12, Priceable: 318,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for w := 1; w <= 60; w++ {
				got := renderSpendStrip(tc.s, w)
				if gw := lipgloss.Width(got); gw > w {
					t.Fatalf("width %d: rendered %d columns: %q", w, gw, got)
				}
				if strings.Contains(got, "\n") {
					t.Fatalf("width %d: strip contains a newline: %q", w, got)
				}
			}
		})
	}
}

// The strip's width guarantee is expressed in lipgloss.Width, and lipgloss.Width measures
// the WIDEST LINE: lipgloss.Width("abc\nabcdef") is 6, not 10. So a WindowLabel carrying a
// newline sails through fitStripFigures' budget check and renderSpendStrip returns a
// TWO-LINE string — the exact "a wrapped chrome line costs a row of the table below it"
// failure its own godoc opens with.
//
// Reachable, not theoretical: spendSummary copies snap.Window verbatim when parseWindowSpan
// cannot read it as a duration, and that field is server-supplied JSON. Fixed at the
// boundary, in spendSummary, rather than guarded at each use — a wire string is sanitised
// once, where it enters. Asserted here because this is where the consequence shows.
func TestSpendSummary_SanitisesTheServersWindowLabel(t *testing.T) {
	m := &model{}
	m.spend.snap = &usage.Snapshot{
		Window: "1h\nEVIL",
		Totals: usage.Counts{Requests: 1, CostMicros: 1_120_000, PricedRequests: 1, PriceableRequests: 1},
		Priced: true,
	}

	s := m.spendSummary()
	// Errorf, not Fatalf: the label check is the cause and the loop below is the
	// consequence, and a reader of a failure wants both. Stopping at the cause would let
	// someone "fix" this by trimming the label in the renderer and never learn that the
	// two-line output was the thing that mattered.
	if strings.ContainsAny(s.WindowLabel, "\n\r\x1b") {
		t.Errorf("WindowLabel = %q still carries a control character straight off the wire", s.WindowLabel)
	}
	for _, w := range []int{120, 100, 80, 64, 48, 32, 24, 16, 8, 1} {
		got := renderSpendStrip(s, w)
		if strings.Contains(got, "\n") {
			t.Errorf("width %d: strip rendered two lines and stole a row from the table: %q", w, got)
		}
		if gw := lipgloss.Width(got); gw > w {
			t.Errorf("width %d: rendered %d columns: %q", w, gw, got)
		}
	}
}

// An escape sequence is the same defect with a worse payload: it can reposition the cursor,
// recolour the pane, or erase the coverage warning it is rendered beside. CWE-150, the same
// reason sanitizeLabel exists for the usage pane's model keys.
func TestSpendSummary_NeutralisesAnEscapeSequenceInTheWindowLabel(t *testing.T) {
	m := &model{}
	m.spend.snap = &usage.Snapshot{
		Window: "1h\x1b[2J\x1b[H",
		Totals: usage.Counts{Requests: 1, CostMicros: 1_120_000, PricedRequests: 1, PriceableRequests: 1},
		Priced: true,
	}

	s := m.spendSummary()
	if strings.Contains(s.WindowLabel, "\x1b") {
		t.Errorf("WindowLabel = %q carries an ESC; it will be written straight to the terminal", s.WindowLabel)
	}
	if got := renderSpendStrip(s, 120); strings.Contains(got, "\x1b[2J") {
		t.Errorf("strip %q clears the screen on behalf of the server", got)
	}
}

func TestSpendStripVisible_HiddenOnPreConnectionPickers(t *testing.T) {
	// The namespace and pod pickers run before any session exists, so there is no
	// cost to show. They also return early from paneView with their own layout.
	for _, p := range []paneID{paneNamespaces, panePods} {
		m := &model{height: 40}
		m.pane = p
		if m.spendStripVisible() {
			t.Errorf("pane %v: strip visible on a pre-connection picker", p)
		}
	}
}

func TestSpendStripVisible_ShownOnTheDataPanes(t *testing.T) {
	for _, p := range []paneID{paneSessions, paneEvents, paneDetail, panePipeline, paneUsage, paneCatalog} {
		m := &model{height: 40}
		m.pane = p
		if !m.spendStripVisible() {
			t.Errorf("pane %v: strip hidden on a data pane", p)
		}
	}
}

func TestSpendStripVisible_FoldsAwayOnAShortTerminal(t *testing.T) {
	// The spec's rule: below 20 rows the strip yields its row to the table, which
	// needs it more than the chrome does.
	m := &model{height: 19}
	m.pane = paneEvents
	if m.spendStripVisible() {
		t.Error("strip took a row on a 19-row terminal")
	}
	m.height = 20
	if !m.spendStripVisible() {
		t.Error("strip hidden at 20 rows, the documented threshold")
	}
}

func TestLayout_ReservesExactlyTheBandsRowsForTheStrip(t *testing.T) {
	// Get this wrong and every table renders one row too tall, pushing the footer
	// off-screen. The row is ADDED to the existing title(1) + footer(2) budget --
	// layout's old comment said "title + blank + footer" but there was never a
	// blank row to borrow.
	tall := &model{width: 100, height: 40}
	tall.pane = paneEvents
	tall.layout()

	short := &model{width: 100, height: 19} // below the fold threshold
	short.pane = paneEvents
	short.layout()

	if want := 40 - 3 - spendBandLines; tall.bodyHeight != want {
		t.Errorf("bodyHeight with the band = %d, want %d (height - title - 2 footer rows - "+
			"spendBandLines)", tall.bodyHeight, want)
	}
	if want := 19 - 3; short.bodyHeight != want {
		t.Errorf("bodyHeight below the fold = %d, want %d (no band rows at all)",
			short.bodyHeight, want)
	}
}

func TestLayout_PickerPanesStillReserveTheStripRow(t *testing.T) {
	// The ruling: reserve by HEIGHT, not per pane. layout() runs on resize, so a
	// pane-aware reservation would need re-running on every pane transition -- and
	// a picker one row shorter than it could be is invisible next to an events
	// table that is wrong by a row and pushes the footer off the bottom.
	picker := &model{width: 100, height: 40}
	picker.pane = panePods
	picker.layout()

	if want := 40 - 3 - spendBandLines; picker.bodyHeight != want {
		t.Errorf("picker bodyHeight = %d, want %d: the reservation must not depend on the pane",
			picker.bodyHeight, want)
	}
	if picker.spendStripVisible() {
		t.Error("the picker reserves the row but must not DRAW the strip")
	}
}

func TestPaneView_DrawsTheStrip(t *testing.T) {
	// The mutation guard for task 3 step 7. A renderSpendStrip call that nothing
	// asserts on is the failure mode here: the strip would be written, reviewed,
	// and never reach the screen. Deleting the append in paneView must fail this.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx, apiclient.New("http://127.0.0.1:1")).(*model)
	m.width, m.height = 100, 40
	m.layout()
	m.spend.snap = &usage.Snapshot{
		Window: "1h",
		Totals: usage.Counts{
			Requests: 10, CostMicros: 1_120_000,
			PricedRequests: 10, PriceableRequests: 10,
		},
		Priced: true,
	}

	got := m.paneView()
	// The band carries no "SPEND" label: the column labels are the identity now, which is
	// why they sit above the values rather than beside them.
	if !strings.Contains(got, "LAST 1H") {
		t.Errorf("paneView output has no band label row; the band is not wired to the screen:\n%s", got)
	}
	if !strings.Contains(got, "$1.12") {
		t.Error("paneView output has no spend figure; the band is rendered from something other than spendSummary")
	}
	// The strip must be the SECOND row, directly under the title bar. "In the
	// chrome, read before the data" is the whole requirement -- a strip rendered
	// below the table is just the per-row cost figure again, one pane over.
	lines := strings.Split(got, "\n")
	if len(lines) < 2 {
		t.Fatalf("paneView rendered %d lines; expected at least a title and a strip", len(lines))
	}
	// Row 1 is the band's LABEL line and row 2 its values: labels above values is the whole
	// point, so a view with the two swapped is wrong even though both are present.
	if !strings.Contains(lines[1], "LAST 1H") {
		t.Errorf("row 1 is %q, want the band's label row directly under the title", lines[1])
	}
	if len(lines) < 3 || !strings.Contains(lines[2], "$1.12") {
		t.Errorf("row 2 is %q, want the value row under its labels", lines[2])
	}
}

func TestPaneView_NoStripRowOnAShortTerminal(t *testing.T) {
	// Below the fold the row is not drawn AND not reserved; drawing it without the
	// reservation is what pushes the footer off the bottom of the terminal.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx, apiclient.New("http://127.0.0.1:1")).(*model)
	m.width, m.height = 100, 19
	m.layout()
	m.spend.snap = &usage.Snapshot{
		Window: "1h",
		Totals: usage.Counts{Requests: 1, CostMicros: 1_120_000, PricedRequests: 1, PriceableRequests: 1},
		Priced: true,
	}

	if got := m.paneView(); strings.Contains(got, stripLabel) {
		t.Errorf("19-row terminal drew the strip row it did not reserve: %q", got)
	}
}

func TestPaneView_PickerPanesDrawNoStrip(t *testing.T) {
	// paneNamespaces and panePods return early from paneView with their own
	// JoinVertical, so this also guards against the strip being added there.
	for _, p := range []paneID{paneNamespaces, panePods} {
		ctx, cancel := context.WithCancel(context.Background())
		m := New(ctx, apiclient.New("http://127.0.0.1:1")).(*model)
		m.width, m.height = 100, 40
		m.layout()
		m.pane = p
		m.spend.snap = &usage.Snapshot{
			Window: "1h",
			Totals: usage.Counts{Requests: 1, CostMicros: 1_120_000, PricedRequests: 1, PriceableRequests: 1},
			Priced: true,
		}
		if got := m.paneView(); strings.Contains(got, stripLabel) {
			t.Errorf("pane %v drew the strip: %q", p, got)
		}
		cancel()
	}
}

func TestRenderSpendStrip_FailedPollSaysUnavailableNotNothing(t *testing.T) {
	// Finding 2. A failing /v1/usage used to render "" on every poll forever, while
	// layout() went on reserving the row: a permanent blank line above the footer
	// and no diagnostic anywhere on screen. Silence is the one unacceptable answer,
	// because the row is spent either way.
	s := spendSummary{Failed: true}
	got := renderSpendStrip(s, 120)

	if got == "" {
		t.Fatal("a failed poll rendered nothing; the reserved row becomes a permanent blank line")
	}
	if !strings.Contains(got, "unavailable") {
		t.Errorf("strip %q does not say cost is unavailable", got)
	}
	if strings.Contains(got, "$") {
		t.Errorf("strip %q shows a dollar amount for a poll that never answered", got)
	}
}

func TestRenderSpendStrip_FailedPollFitsEveryWidth(t *testing.T) {
	// The failure path is the one an operator sees for as long as the endpoint is
	// broken, so it must obey the width contract like any other.
	s := spendSummary{Failed: true}
	for w := 1; w <= 80; w++ {
		got := renderSpendStrip(s, w)
		if gw := lipgloss.Width(got); gw > w {
			t.Fatalf("width %d: rendered %d columns: %q", w, gw, got)
		}
		if strings.Contains(got, "\n") {
			t.Fatalf("width %d: strip contains a newline: %q", w, got)
		}
	}
}

func TestRenderSpendStrip_FailedPollOutranksTheNoDataPath(t *testing.T) {
	// Failed must not be inferred from the counters. A failed poll has none, so if
	// the renderer reached the Priceable == 0 branch it would return "" -- which is
	// exactly the bug. Pin the precedence.
	failed := renderSpendStrip(spendSummary{Failed: true}, 120)
	quiet := renderSpendStrip(spendSummary{}, 120)

	if failed == quiet {
		t.Errorf("a failed poll renders identically to no-data-yet (%q); the two are different answers", failed)
	}
	if quiet != "" {
		t.Errorf("no-data-yet rendered %q, want the empty string", quiet)
	}
}

func TestRenderSpendStrip_SettledZeroShowsZeroNotUnavailable(t *testing.T) {
	// Finding 3, the half that was unpinned. A window that WAS priced and cost
	// exactly nothing is a known answer: the gateway declared the traffic free.
	// authlib/usage asserts "a settled zero IS priced", and formatUSDCell renders
	// "<$0.0001" for any positive amount under the floor -- so "$0.0000" in the
	// strip can only ever mean an exact, settled zero.
	//
	// Without this test, someone could "fix" the zero into an unavailable branch
	// and the suite would stay green, silently conflating free traffic with
	// unmeasured traffic -- the precise distinction the strip exists to draw.
	s := spendSummary{WindowUSD: 0, WindowLabel: "1h", Priced: true, Priceable: 10}
	got := renderSpendStrip(s, 120)

	if got == "" {
		t.Fatal("a settled zero rendered nothing; free traffic is a real, knowable answer")
	}
	if strings.Contains(got, "unavailable") {
		t.Errorf("strip %q reports a SETTLED zero as unavailable; that conflates free with unknown", got)
	}
	if !strings.Contains(got, "$0.0000") {
		t.Errorf("strip %q does not state the settled zero as an amount", got)
	}
	// And it must be distinguishable from the unknown case, which is the whole point.
	if unknown := renderSpendStrip(spendSummary{WindowLabel: "1h", Priceable: 10}, 120); got == unknown {
		t.Errorf("a settled zero and an unknown cost render identically as %q", got)
	}
}

func TestRenderSpendStrip_NoPriceableTrafficSaysSoRatherThanNothing(t *testing.T) {
	// A poll answered and found no inference traffic at all. That is a finding, not
	// an absence: the row is reserved on height alone, so rendering "" here buys a
	// blank line above the footer that reads as a broken UI.
	s := spendSummary{WindowLabel: "1h", HasSnapshot: true, Priceable: 0}
	got := renderSpendStrip(s, 120)

	if got == "" {
		t.Fatal("a window with no priceable traffic rendered nothing, wasting its reserved row")
	}
	if !strings.Contains(got, "no priceable traffic") {
		t.Errorf("strip %q does not say there was no priceable traffic", got)
	}
	if strings.Contains(got, "$") {
		t.Errorf("strip %q shows a dollar amount for a window with nothing to price", got)
	}
	if strings.Contains(got, "unavailable") {
		t.Errorf("strip %q says cost is unavailable; the cost is knowable and there simply was none to price", got)
	}
}

func TestRenderSpendStrip_BeforeTheFirstPollStaysSilent(t *testing.T) {
	// The one case where "" is right. "We have not looked" is honest, brief and
	// self-correcting within a poll interval -- and it must stay distinguishable
	// from "we looked and found nothing to price", which is the test above.
	quiet := renderSpendStrip(spendSummary{}, 120)
	if quiet != "" {
		t.Errorf("pre-first-poll rendered %q, want the empty string", quiet)
	}

	looked := renderSpendStrip(spendSummary{WindowLabel: "1h", HasSnapshot: true}, 120)
	if looked == quiet {
		t.Error("'not looked yet' and 'looked, nothing priceable' render identically")
	}
}

func TestRenderSpendStrip_NoPriceableTrafficFitsEveryWidth(t *testing.T) {
	// It is a steady state for a proxy handling only non-LLM traffic, so it obeys
	// the width contract like every other branch.
	s := spendSummary{WindowLabel: "1h", HasSnapshot: true}
	for w := 1; w <= 80; w++ {
		got := renderSpendStrip(s, w)
		if gw := lipgloss.Width(got); gw > w {
			t.Fatalf("width %d: rendered %d columns: %q", w, gw, got)
		}
		if strings.Contains(got, "\n") {
			t.Fatalf("width %d: strip contains a newline: %q", w, got)
		}
	}
}

func TestRenderSpendStrip_UnpricedGapStillOutranksTheNoTrafficLine(t *testing.T) {
	// Priceable > 0 with nothing priced is a COVERAGE problem and must keep saying
	// so; the no-traffic line is only for Priceable == 0. Pins the precedence so
	// the new branch cannot swallow the coverage warning.
	s := spendSummary{WindowLabel: "1h", HasSnapshot: true, Unpriced: 12, Priceable: 318}
	got := renderSpendStrip(s, 120)

	if strings.Contains(got, "no priceable traffic") {
		t.Errorf("strip %q claims no priceable traffic while reporting 318 priceable requests", got)
	}
	if !strings.Contains(got, "unavailable") {
		t.Errorf("strip %q lost the coverage warning", got)
	}
}

// A LATENT bug, armed by the commit that first sets HasToday. The "nothing was
// priced" branch is guarded on `!s.Priced && !s.HasToday`, so a summary carrying a
// today figure over a window that priced nothing falls through to the figures
// path — where the window figure was rendered unconditionally and read "$0.0000
// /1h". That states a settled zero for a cost nobody knows, which is the one thing
// this whole feature forbids, and it is exactly the misreading formatUSDCell's
// floor and the strip's "cost unavailable" branch both exist to prevent.
//
// Unreachable while HasToday is never set, which is why it survived review twice.
// Pinned here rather than left for the ledger commit to trip over: a test is the
// only artefact that a later author cannot skip reading.
func TestRenderSpendStrip_TodayWithAnUnpricedWindowStatesNoWindowZero(t *testing.T) {
	s := spendSummary{
		TodayUSD: 4.17, HasToday: true,
		WindowUSD: 0, WindowLabel: "1h", Priced: false,
		HasSnapshot: true, Priceable: 40, Unpriced: 40,
	}
	got := renderSpendStrip(s, 120)

	if strings.Contains(got, "$0.0000") {
		t.Errorf("strip %q reports an UNPRICED window as a settled $0.0000", got)
	}
	// The today figure is the one thing here that IS known, so it must survive.
	if !strings.Contains(got, "4.17") {
		t.Errorf("strip %q dropped the today figure, which is the only known cost", got)
	}
	// And the coverage gap still qualifies the total.
	if !strings.Contains(got, "40 of 40 unpriced") {
		t.Errorf("strip %q lost the coverage warning for an unpriced window", got)
	}
}

// THE critical finding: a partial day published as a complete total.
//
// One priced request out of four hundred. The dollar figure is real and the day's real
// cost is unknown and far larger, and the strip printed "$0.0031 today" with no marker
// anywhere on the line — the branch's headline figure, presented as settled. Asserted at
// EVERY width, because the marker's whole justification is that it is one column and so
// cannot be squeezed out: wherever the figure appears, the fact that it is partial
// appears with it.
func TestRenderSpendStrip_APartialTodayIsNeverPublishedAsComplete(t *testing.T) {
	s := spendSummary{
		TodayUSD: 0.0031, HasToday: true, TodayUnpriced: 399, TodayPriceable: 400,
		WindowUSD: 1.12, WindowLabel: "1h", Priced: true,
		HasSnapshot: true, Priceable: 400,
	}
	for w := 1; w <= 200; w++ {
		got := renderSpendStrip(s, w)
		if !strings.Contains(got, "$0.0031") {
			// Dropped whole, which is the honest degradation. It is the figure appearing
			// UNQUALIFIED that is forbidden.
			continue
		}
		if !strings.Contains(got, "$0.0031"+partialMarker) {
			t.Fatalf("width %d: %q states today's partial total as a complete one", w, got)
		}
	}
	// Where there is room, the caveat is spelled out — and in TODAY's own numbers, not
	// the hour's.
	wide := renderSpendStrip(s, 200)
	if !strings.Contains(wide, "399 of 400 unpriced") {
		t.Errorf("wide strip %q does not disclose the day's own coverage gap", wide)
	}
}

// The second verified misread, at the renderer: "SPEND $4.1700 today  $1.1200 /1h
// 40 of 40 unpriced" — a warning that reads as qualifying the DAY and describes the
// HOUR. Here the day is complete and the hour has the gap, so the day must carry no
// marker and the gap must be attached to the figure it is about.
func TestRenderSpendStrip_TheHoursGapDoesNotQualifyTheDay(t *testing.T) {
	s := spendSummary{
		TodayUSD: 4.17, HasToday: true, TodayUnpriced: 0, TodayPriceable: 318,
		WindowUSD: 1.12, WindowLabel: "1h", Priced: true,
		HasSnapshot: true, Unpriced: 40, Priceable: 40,
	}
	got := renderSpendStrip(s, 200)

	if !strings.Contains(got, "$4.1700 today") {
		t.Errorf("strip %q does not state the day's complete total plainly", got)
	}
	if strings.Contains(got, "$4.1700"+partialMarker) {
		t.Errorf("strip %q marks a fully priced day as partial", got)
	}
	// The gap rides on the hour's own figure, inside the hour's group — the span is now the
	// group's prefix rather than a suffix on this figure, so the whole reading is one unit.
	if !strings.Contains(got, "1h: $1.1200"+partialMarker+" (40 of 40 unpriced)") {
		t.Errorf("strip %q does not attach the hour's gap to the hour's own figure", got)
	}
	// And the old shape must be gone: an unlabelled coverage note at the end of the line
	// is the misattribution itself.
	if strings.HasSuffix(got, "40 of 40 unpriced") {
		t.Errorf("strip %q still trails an unlabelled coverage note after the day's figure", got)
	}
}

// A window that priced NOTHING has no figure for its gap to ride on, and the gap still
// has to be stated. It leads the window group, so the group's span names it and it cannot be
// read as qualifying the today figure beside it.
//
// THE NOTE ITSELF NO LONGER CARRIES A LABEL: it is the first figure of the window group, so the
// prefix that names the span is the group's. The hand-appended "/1h" this used to assert was the
// narrower fix for the same misreading, made before there was a group to belong to.
func TestRenderSpendStrip_ASuppressedWindowsGapWearsTheWindowsLabel(t *testing.T) {
	s := spendSummary{
		TodayUSD: 4.17, HasToday: true, TodayPriceable: 318,
		WindowLabel: "1h", Priced: false,
		HasSnapshot: true, Unpriced: 40, Priceable: 40,
	}
	got := renderSpendStrip(s, 200)

	if !strings.Contains(got, "1h: 40 of 40 unpriced") {
		t.Errorf("strip %q does not name the window the 40-request gap belongs to", got)
	}
	if strings.Contains(got, "$4.1700"+partialMarker) {
		t.Errorf("strip %q marked the day partial from the HOUR's gap", got)
	}
}

// A truncated stream's floor must never be published as an exact total.
//
// That is the title of a commit on this branch, and the strip ignored it: the figure
// went out as "$4.1700 today" and "$1.1200 /1h" to four decimal places, with no
// annotation, for a total the aggregator itself reports as a lower bound. Asserted at
// every width for the reason the partial marker is: one column is exactly what it takes
// to make the qualification undroppable.
func TestRenderSpendStrip_AnInexactTotalIsMarkedAtEveryWidth(t *testing.T) {
	s := spendSummary{
		TodayUSD: 4.17, HasToday: true, TodayPriceable: 318, TodayIncomplete: 4,
		WindowUSD: 1.12, WindowLabel: "1h", Priced: true,
		HasSnapshot: true, Priceable: 10, Incomplete: 3,
	}
	for w := 1; w <= 200; w++ {
		got := renderSpendStrip(s, w)
		for amount, label := range map[string]string{"$4.1700": "today", "$1.1200": "the hour"} {
			if !strings.Contains(got, amount) {
				continue
			}
			if !strings.Contains(got, inexactMarker+amount) {
				t.Fatalf("width %d: %q states %s's inexact total as an exact figure", w, got, label)
			}
		}
	}
	wide := renderSpendStrip(s, 200)
	if !strings.Contains(wide, "4 inexact") {
		t.Errorf("wide strip %q does not say how many of the day's figures are inexact", wide)
	}
	if !strings.Contains(wide, "3 inexact") {
		t.Errorf("wide strip %q does not say how many of the hour's figures are inexact", wide)
	}
}

// The other half: an exact total carries no marker. Without this the fix could be
// "made to pass" by marking everything, which is the same as marking nothing.
func TestRenderSpendStrip_AnExactTotalCarriesNoInexactMarker(t *testing.T) {
	s := spendSummary{
		TodayUSD: 4.17, HasToday: true, TodayPriceable: 318,
		WindowUSD: 1.12, WindowLabel: "1h", Priced: true, HasSnapshot: true, Priceable: 10,
	}
	got := renderSpendStrip(s, 200)

	if strings.Contains(got, inexactMarker+"$4.1700") || strings.Contains(got, inexactMarker+"$1.1200") {
		t.Errorf("strip %q marks an exact total as inexact", got)
	}
	if strings.Contains(got, "inexact") {
		t.Errorf("strip %q carries an exactness caveat with nothing to act on", got)
	}
}

// Exactness and coverage are different claims about the same number and both can be
// true at once, so a figure that is both must say both — and in the order cmd_cost.go
// fixed: the figure itself first, then what it covers.
func TestRenderSpendStrip_AFigureCanBeBothInexactAndPartial(t *testing.T) {
	s := spendSummary{
		TodayUSD: 0.0031, HasToday: true, TodayUnpriced: 399, TodayPriceable: 400, TodayIncomplete: 2,
		HasSnapshot: true,
	}
	got := renderSpendStrip(s, 200)

	if !strings.Contains(got, inexactMarker+"$0.0031"+partialMarker+" today") {
		t.Errorf("strip %q does not carry both markers on the figure they qualify", got)
	}
	if !strings.Contains(got, "(2 inexact, 399 of 400 unpriced)") {
		t.Errorf("strip %q does not state both caveats, exactness first", got)
	}
	// And both markers survive the compact form, which is what a narrow terminal gets.
	narrow := renderSpendStrip(s, 24)
	if !strings.Contains(narrow, inexactMarker+"$0.0031"+partialMarker) {
		t.Errorf("narrow strip %q dropped a marker; the figure now reads as settled", narrow)
	}
}

// The width matrix, with both caveats live. The strip's contract is one line, never
// wider than its budget, and no clipped number — and a caveat is the newest thing that
// can break it. The CJK label is two display columns per rune and one rune, so any
// len()- or rune-based arithmetic in the caveat path renders wider than it claims.
func TestRenderSpendStrip_CaveatsObeyTheWidthContract(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    spendSummary
	}{
		{"partial day and partial hour", spendSummary{
			TodayUSD: 0.0031, HasToday: true, TodayUnpriced: 399, TodayPriceable: 400,
			WindowUSD: 1.12, WindowLabel: "1h", Priced: true,
			HasSnapshot: true, Unpriced: 12, Priceable: 318,
			SavedUSD: 0.24, HasSaved: true,
		}},
		{"every figure both inexact and partial", spendSummary{
			TodayUSD: 0.0031, HasToday: true, TodayUnpriced: 399, TodayPriceable: 400, TodayIncomplete: 7,
			WindowUSD: 1.12, WindowLabel: "1h", Priced: true,
			HasSnapshot: true, Unpriced: 12, Priceable: 318, Incomplete: 3,
			SavedUSD: 0.24, HasSaved: true,
		}},
		{"every caveat at once, plus a stale age", spendSummary{
			TodayUSD: 0.0031, HasToday: true, TodayUnpriced: 399, TodayPriceable: 400, TodayIncomplete: 7,
			WindowUSD: 1.12, WindowLabel: "1h", Priced: true,
			HasSnapshot: true, Unpriced: 12, Priceable: 318, Incomplete: 3,
			SavedUSD: 0.24, HasSaved: true,
			Age: 3 * time.Minute, Stale: true,
		}},
		{"cjk window label under a partial day", spendSummary{
			TodayUSD: 0.0031, HasToday: true, TodayUnpriced: 399, TodayPriceable: 400,
			WindowUSD: 1.12, WindowLabel: "過去一時間", Priced: true,
			HasSnapshot: true, Unpriced: 12, Priceable: 318,
		}},
		{"suppressed window whose gap wears a cjk label", spendSummary{
			TodayUSD: 4.17, HasToday: true, TodayUnpriced: 2, TodayPriceable: 318,
			WindowLabel: "過去一時間", Priced: false,
			HasSnapshot: true, Unpriced: 40, Priceable: 40,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, w := range []int{40, 60, 80, 96, 120, 200} {
				assertStripFits(t, tc.s, w)
			}
			// Every width from 1 up, because the interesting failures are at the seams
			// where a form stops fitting.
			for w := 1; w <= 200; w++ {
				assertStripFits(t, tc.s, w)
			}
		})
	}
}

// A wedged poll chain must be visible. The strip is always on, so the failure mode is
// silent: the last good figure keeps rendering and nothing says when it was fetched.
func TestRenderSpendStrip_AStaleFigureIsDated(t *testing.T) {
	s := spendSummary{
		WindowUSD: 1.12, WindowLabel: "1h", Priced: true, HasSnapshot: true, Priceable: 10,
		Age: 3 * time.Minute, Stale: true,
	}
	got := renderSpendStrip(s, 200)

	if !strings.Contains(got, "3m ago") {
		t.Errorf("strip %q does not date a figure fetched 3m ago", got)
	}
	// The figure stays: it is old, not wrong.
	if !strings.Contains(got, "1h: $1.1200") {
		t.Errorf("strip %q withheld a stale figure instead of dating it", got)
	}
	// And the age is NOT inside the window group. applyAges takes the older of both poll chains
	// precisely so one reading describes the whole answer, so a "1h:" in front of it would hand a
	// both-chains figure the hour's span.
	if strings.Contains(got, "1h: polled") || strings.Contains(got, "1h: 3m") {
		t.Errorf("strip %q put the staleness note inside the window group; it belongs to neither "+
			"span", got)
	}
}

// And a fresh one is not dated, which is the half that keeps the age worth reading.
func TestRenderSpendStrip_AFreshFigureIsNotDated(t *testing.T) {
	s := spendSummary{WindowUSD: 1.12, WindowLabel: "1h", Priced: true, HasSnapshot: true, Priceable: 10}
	got := renderSpendStrip(s, 200)

	if strings.Contains(got, "ago") {
		t.Errorf("strip %q dates a current figure; a permanent timestamp is noise", got)
	}
}

// The age is the LAST figure, so it is the first thing a narrow terminal gives up — unlike
// a partiality marker, which is undroppable. It is recoverable information: the next poll
// either lands or the age keeps growing.
func TestRenderSpendStrip_TheAgeYieldsBeforeAFigure(t *testing.T) {
	s := spendSummary{
		TodayUSD: 4.17, HasToday: true, TodayPriceable: 318,
		WindowUSD: 1.12, WindowLabel: "1h", Priced: true, HasSnapshot: true, Priceable: 10,
		Age: 3 * time.Minute, Stale: true,
	}
	narrow := renderSpendStrip(s, 24)

	if !strings.Contains(narrow, "$4.1700") {
		t.Errorf("narrow strip %q dropped the headline figure before the age", narrow)
	}
	if strings.Contains(narrow, "ago") {
		t.Errorf("narrow strip %q kept the age at the expense of a reading", narrow)
	}
}

func TestFormatSpendAge_RoundsToTheCoarsestUsefulUnit(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{45 * time.Second, "45s"},
		{90 * time.Second, "1m"},
		{3 * time.Minute, "3m"},
		{59*time.Minute + 59*time.Second, "59m"},
		{2 * time.Hour, "2h"},
	} {
		if got := formatSpendAge(tc.in); got != tc.want {
			t.Errorf("formatSpendAge(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// assertStripFits is the strip's whole contract in one place: one line, inside the
// budget, and no half-rendered dollar amount.
func assertStripFits(t *testing.T, s spendSummary, w int) {
	t.Helper()
	got := renderSpendStrip(s, w)
	if gw := lipgloss.Width(got); gw > w {
		t.Fatalf("width %d: rendered %d columns: %q", w, gw, got)
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("width %d: strip contains a newline and costs the table a row: %q", w, got)
	}
	// A dollar sign that is not followed by a whole four-decimal figure is a clipped
	// amount. formatUSDCell is the only producer of a "$" on this line.
	for _, tail := range []string{"$", "$0", "$0.", "$0.0"} {
		if strings.HasSuffix(got, tail) {
			t.Fatalf("width %d: %q ends mid-figure", w, got)
		}
	}
}

// A newline in the server's window label, with a caveat live beside it. The label is
// sanitised in spendSummary, and this is the assertion that the caveats did not open a
// second path for a wire string to reach the terminal unmeasured: lipgloss.Width
// measures the WIDEST LINE, so a two-line result passes a budget check and silently
// steals a row from the table below.
func TestSpendSummary_CaveatsSurviveANewlineBearingWindowLabel(t *testing.T) {
	m := &model{}
	m.spend.snap = &usage.Snapshot{
		Window: "1h\nEVIL",
		Totals: usage.Counts{
			Requests: 318, CostMicros: 1_120_000,
			PricedRequests: 306, PriceableRequests: 318,
		},
		Priced: true,
	}
	m.spend.todaySnap = &usage.Snapshot{
		Window: usage.WindowToday,
		Totals: usage.Counts{
			Requests: 400, CostMicros: 3_100,
			PricedRequests: 1, PriceableRequests: 400,
		},
		Priced: true,
	}

	s := m.spendSummary()
	for _, w := range []int{40, 60, 80, 96, 120, 200} {
		assertStripFits(t, s, w)
	}
	for w := 1; w <= 200; w++ {
		assertStripFits(t, s, w)
	}
}

// The mirror of the above: a PRICED window keeps its figure when today is present.
// Without this the guard could be "fixed" by dropping the window figure whenever
// HasToday is set, which would silently delete a correct reading.
func TestRenderSpendStrip_TodayWithAPricedWindowKeepsBoth(t *testing.T) {
	s := spendSummary{
		TodayUSD: 4.17, HasToday: true,
		WindowUSD: 1.12, WindowLabel: "1h", Priced: true,
		HasSnapshot: true, Priceable: 40,
	}
	got := renderSpendStrip(s, 120)

	if !strings.Contains(got, "$4.1700 today") {
		t.Errorf("strip %q lost the today figure", got)
	}
	if !strings.Contains(got, "1h: $1.1200") {
		t.Errorf("strip %q lost the priced window figure", got)
	}
}

// TestRenderSpendStrip_NoFigureCarriesAMinusSign.
//
// The rendered end of the negative-total refusal, on the line that is always on screen.
// Both figure paths at once, because they reach the strip through different code: the
// window figure through spendSummary and the day figure through applyTodayFigure.
//
// "$-5.0000" is not a smaller number, it is a claim that money came back. The strip says
// "cost unavailable" instead, which is its established spelling for a figure nobody can
// vouch for.
func TestRenderSpendStrip_NoFigureCarriesAMinusSign(t *testing.T) {
	m := &model{}
	m.spend.snap = &usage.Snapshot{
		Window: "1h",
		Totals: usage.Counts{Requests: 10, CostMicros: -5_000_000,
			PricedRequests: 10, PriceableRequests: 10},
		Priced: true,
	}
	m.spend.todaySnap = &usage.Snapshot{
		Window: usage.WindowToday,
		Totals: usage.Counts{Requests: 400, CostMicros: -5_000_000,
			PricedRequests: 400, PriceableRequests: 400},
		Priced: true,
	}

	got := renderSpendStrip(m.spendSummary(), 200)
	if strings.Contains(got, "$-") {
		t.Errorf("strip %q renders a negative amount — a refund nobody issued", got)
	}
	// And it is not silence either: the row is reserved on height alone, so a blank line
	// above the footer is the one outcome worse than saying "unavailable".
	if !strings.Contains(got, "cost unavailable") {
		t.Errorf("strip %q neither showed a figure nor declined one", got)
	}
}

// TestApplyTodayFigure_CarriesTheLedgersDamageDisclosure.
//
// The data half of the fix. usage.Snapshot.Degraded had ZERO non-test consumers in
// cmd/abctl: the server populated it, logged a warning, and nothing downstream read it — so
// a day that lost rows produced a figure indistinguishable from a clean one.
//
// The strip's today poll is the only ledger-backed request the TUI's chrome makes, which
// makes this the one field on the path that can be populated at all.
func TestApplyTodayFigure_CarriesTheLedgersDamageDisclosure(t *testing.T) {
	m := &model{}
	m.spend.todaySnap = &usage.Snapshot{
		Window: usage.WindowToday,
		Totals: usage.Counts{Requests: 400, CostMicros: 4_170_000,
			PricedRequests: 400, PriceableRequests: 400},
		Priced:   true,
		Degraded: &usage.Degraded{SkippedLines: 3, TruncatedDays: 1},
	}

	var out spendSummary
	m.applyTodayFigure(&out)
	if out.TodayDegraded == nil {
		t.Fatal("TodayDegraded is nil; the ledger's damage disclosure reaches no renderer")
	}
	if out.TodayDegraded.SkippedLines != 3 || out.TodayDegraded.TruncatedDays != 1 {
		t.Errorf("TodayDegraded = %+v, want 3 lines and 1 day", *out.TodayDegraded)
	}
	// And the figure is still published: it is short, not unknown.
	if !out.HasToday || out.TodayUSD != 4.17 {
		t.Errorf("HasToday=%v TodayUSD=%v; a short figure was withheld rather than qualified",
			out.HasToday, out.TodayUSD)
	}
}

// TestApplyTodayFigure_ACleanReadLeavesNoDisclosure.
//
// The pointer's whole point: absence means the read was clean, so nothing may be rendered
// for it. Zeros in an always-present object would read as "checked, fine" from a producer
// that never checked.
func TestApplyTodayFigure_ACleanReadLeavesNoDisclosure(t *testing.T) {
	m := &model{}
	m.spend.todaySnap = &usage.Snapshot{
		Window: usage.WindowToday,
		Totals: usage.Counts{Requests: 400, CostMicros: 4_170_000,
			PricedRequests: 400, PriceableRequests: 400},
		Priced: true,
	}

	var out spendSummary
	m.applyTodayFigure(&out)
	if out.TodayDegraded != nil {
		t.Errorf("TodayDegraded = %+v for a clean read", *out.TodayDegraded)
	}
}

// TestSpendSummary_CarriesTheClampDisclosureOnBothSpans.
//
// The data half. usage.Counts.Saturated had ZERO non-test consumers in cmd/abctl, so a clamped
// aggregate produced a figure indistinguishable from a settled one on every money surface at
// once. Both spans are asserted because the flag is on usage.Counts rather than on a ledger
// read: the ring's Add clamps too, so carrying it for the day alone would leave the strip's
// rolling reading able to publish a clamped figure bare.
func TestSpendSummary_CarriesTheClampDisclosureOnBothSpans(t *testing.T) {
	clamped := usage.Counts{Requests: 400, CostMicros: 4_170_000,
		PricedRequests: 400, PriceableRequests: 400, Saturated: true}

	m := &model{}
	m.spend.snap = &usage.Snapshot{Window: "1h", Totals: clamped, Priced: true,
		Buckets: []usage.Bucket{{Counts: clamped}}}
	m.spend.todaySnap = &usage.Snapshot{Window: usage.WindowToday, Totals: clamped, Priced: true}

	out := m.spendSummary()
	if !out.Clamped {
		t.Error("Clamped is false; a clamped rolling window reaches no renderer")
	}
	if !out.TodayClamped {
		t.Error("TodayClamped is false; a clamped day reaches no renderer")
	}
	// And the figures are still published: they are floors, not unknowns, and withholding them
	// would report measured spend as unavailable.
	if !out.HasToday || out.WindowUSD == 0 {
		t.Errorf("HasToday=%v WindowUSD=%v; clamped figures were withheld rather than qualified",
			out.HasToday, out.WindowUSD)
	}
}

// TestSpendSummary_ACleanAggregateLeavesTheClampUnset is the mirror: false must mean "the
// arithmetic held", so nothing may be rendered for it.
func TestSpendSummary_ACleanAggregateLeavesTheClampUnset(t *testing.T) {
	clean := usage.Counts{Requests: 400, CostMicros: 4_170_000,
		PricedRequests: 400, PriceableRequests: 400}

	m := &model{}
	m.spend.snap = &usage.Snapshot{Window: "1h", Totals: clean, Priced: true,
		Buckets: []usage.Bucket{{Counts: clean}}}
	m.spend.todaySnap = &usage.Snapshot{Window: usage.WindowToday, Totals: clean, Priced: true}

	out := m.spendSummary()
	if out.Clamped || out.TodayClamped {
		t.Errorf("Clamped=%v TodayClamped=%v for an aggregate that never clamped",
			out.Clamped, out.TodayClamped)
	}
}

// TestRenderSpendStrip_ADamagedDayWearsItsOwnMarker.
//
// The rendered half. The marker rides on the FIGURE, so the fitter can drop the words and
// never the fact — the discipline partialMarker's own doc sets out. And it is a THIRD glyph:
// usage.Snapshot.Degraded's doc forbids showing it under the same marker as
// IncompleteRequests, because one says a figure in the sum is a floor and the other says
// rows are missing from the sum.
func TestRenderSpendStrip_ADamagedDayWearsItsOwnMarker(t *testing.T) {
	s := spendSummary{
		TodayUSD: 4.17, HasToday: true, TodayPriceable: 400,
		TodayDegraded: &usage.Degraded{SkippedLines: 3},
		WindowUSD:     1.12, WindowLabel: "1h", Priced: true, HasSnapshot: true, Priceable: 10,
	}
	got := renderSpendStrip(s, 200)

	if !strings.Contains(got, damagedMarker+"$4.1700 today") {
		t.Errorf("strip %q publishes a short day figure with no marker on it", got)
	}
	// The words too, while there is room for them.
	if !strings.Contains(got, "3 lines lost") {
		t.Errorf("strip %q does not say what the day lost", got)
	}
	// The rolling window figure is ring-backed and cannot be damaged, so it must NOT wear
	// the marker: a caveat on the wrong figure is a misattribution, which is the defect
	// moneyFigure was built to end.
	if strings.Contains(got, damagedMarker+"$1.1200") {
		t.Errorf("strip %q marks the ring-backed window figure as damaged", got)
	}
}

// TestRenderSpendStrip_TheDamageMarkerSurvivesNarrowing.
//
// The words are droppable, the marker is not. fitStripFigures gives up every figure's
// explanation before it gives up a reading, so a narrow terminal loses "3 lines lost" — and
// if it also lost the "!" the strip would publish a short total as a complete one, the worst
// outcome available on this line.
func TestRenderSpendStrip_TheDamageMarkerSurvivesNarrowing(t *testing.T) {
	s := spendSummary{
		TodayUSD: 4.17, HasToday: true, TodayPriceable: 400, TodayIncomplete: 7,
		TodayUnpriced: 100,
		TodayDegraded: &usage.Degraded{SkippedLines: 3, TruncatedDays: 1},
		HasSnapshot:   true,
	}
	// Every width from the point one whole figure fits. Below that the strip renders ""
	// rather than clip, which is its documented contract.
	for w := 1; w <= 200; w++ {
		got := renderSpendStrip(s, w)
		if got == "" {
			continue
		}
		if !strings.Contains(got, damagedMarker) {
			t.Errorf("width %d: %q dropped the damage marker: a short total now reads as complete", w, got)
		}
		// All three claims, all one cell each, none crowding out another.
		if !strings.Contains(got, damagedMarker+inexactMarker+"$4.1700"+partialMarker) {
			t.Errorf("width %d: %q lost one of the three markers", w, got)
		}
	}
}

// The width matrix again, with the damage caveat live — the newest thing that can break the
// strip's one-line, within-budget, never-clipped contract. The CJK label is two display
// columns per rune, so any len()- or rune-based arithmetic in the caveat path renders wider
// than it claims.
func TestRenderSpendStrip_TheDamageCaveatObeysTheWidthContract(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    spendSummary
	}{
		{"damaged day beside a partial hour", spendSummary{
			TodayUSD: 0.0031, HasToday: true, TodayUnpriced: 399, TodayPriceable: 400,
			TodayDegraded: &usage.Degraded{SkippedLines: 3},
			WindowUSD:     1.12, WindowLabel: "1h", Priced: true,
			HasSnapshot: true, Unpriced: 12, Priceable: 318,
			SavedUSD: 0.24, HasSaved: true,
		}},
		{"every caveat the day can carry, plus a stale age", spendSummary{
			TodayUSD: 0.0031, HasToday: true, TodayUnpriced: 399, TodayPriceable: 400,
			TodayIncomplete: 7,
			TodayDegraded:   &usage.Degraded{SkippedLines: 1_234_567, TruncatedDays: 89},
			WindowUSD:       1.12, WindowLabel: "1h", Priced: true,
			HasSnapshot: true, Unpriced: 12, Priceable: 318, Incomplete: 3,
			SavedUSD: 0.24, HasSaved: true,
			Age: 3 * time.Minute, Stale: true,
		}},
		{"damaged day under a cjk window label", spendSummary{
			TodayUSD: 0.0031, HasToday: true, TodayUnpriced: 399, TodayPriceable: 400,
			TodayDegraded: &usage.Degraded{SkippedLines: 3, TruncatedDays: 1},
			WindowUSD:     1.12, WindowLabel: "過去一時間", Priced: true,
			HasSnapshot: true, Unpriced: 12, Priceable: 318,
		}},
		{"a disclosure carrying no counters", spendSummary{
			TodayUSD: 4.17, HasToday: true, TodayPriceable: 400,
			TodayDegraded: &usage.Degraded{},
			HasSnapshot:   true,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, w := range []int{40, 60, 80, 96, 120, 200} {
				assertStripFits(t, tc.s, w)
			}
			// Every width from 1 up, because the interesting failures are at the seams where a
			// form stops fitting.
			for w := 1; w <= 200; w++ {
				assertStripFits(t, tc.s, w)
			}
		})
	}
}

// TestRenderSpendStrip_TheDamageCaveatIsNotWhatDropsAFigure.
//
// The words go in the figure's FULL form only, and fitStripFigures tries every figure's
// compact form before it drops a reading — so a longer caveat can cost an explanation and
// never a number. Pinned against the same summary with a clean read, which is the only way
// to tell "the caveat took a figure" from "the terminal was always too narrow".
func TestRenderSpendStrip_TheDamageCaveatIsNotWhatDropsAFigure(t *testing.T) {
	clean := spendSummary{
		TodayUSD: 4.17, HasToday: true, TodayPriceable: 400,
		WindowUSD: 1.12, WindowLabel: "1h", Priced: true,
		HasSnapshot: true, Priceable: 318,
	}
	damaged := clean
	damaged.TodayDegraded = &usage.Degraded{SkippedLines: 1_234_567, TruncatedDays: 89}

	for w := 1; w <= 200; w++ {
		gotClean, gotDamaged := renderSpendStrip(clean, w), renderSpendStrip(damaged, w)
		// Count the readings, not the characters: a figure is a "$" on this line.
		nClean, nDamaged := strings.Count(gotClean, "$"), strings.Count(gotDamaged, "$")
		// The marker costs the day figure one column, so the damaged line may hold one fewer
		// reading at the seams — that is the marker, which is undroppable by design, not the
		// caveat's words. More than one behind means the words are costing numbers.
		if nDamaged < nClean-1 {
			t.Errorf("width %d: damaged line holds %d figures where clean holds %d\n  clean:   %q\n  damaged: %q",
				w, nDamaged, nClean, gotClean, gotDamaged)
		}
	}
}

// TestMoneyMarkers_AreThreeDistinctOneColumnClaims.
//
// usage.Snapshot.Degraded's doc is explicit that its claim must not be merged with
// usage.Counts.IncompleteRequests', nor shown under one marker; partialMarker is a third
// claim again. Spelling any two of them the same character would collapse two facts into one
// glyph WITHOUT A SINGLE RENDER TEST NOTICING — the figure would still carry "a marker", and
// every assertion phrased in terms of the constants would still hold. That is exactly what
// happened when this was mutated, which is why the distinctness is pinned directly.
//
// One display column each, which is what makes them survivable at every width the strip's
// fitter and fitTableColumns can produce. Not a rune count and not len(): both lie about a
// glyph, and the strip's whole budget is expressed in display cells.
func TestMoneyMarkers_AreThreeDistinctOneColumnClaims(t *testing.T) {
	for _, m := range []struct{ name, glyph string }{
		{"damagedMarker", damagedMarker},
		{"inexactMarker", inexactMarker},
		{"partialMarker", partialMarker},
	} {
		if w := lipgloss.Width(m.glyph); w != 1 {
			t.Errorf("%s = %q is %d display columns, want 1", m.name, m.glyph, w)
		}
	}
	seen := map[string]string{}
	for _, m := range []struct{ name, glyph string }{
		{"damagedMarker", damagedMarker},
		{"inexactMarker", inexactMarker},
		{"partialMarker", partialMarker},
	} {
		if other, dup := seen[m.glyph]; dup {
			t.Errorf("%s and %s are both %q — two claims under one marker", m.name, other, m.glyph)
		}
		seen[m.glyph] = m.name
	}
}

// TestRenderSpendStrip_ADisclosureWithNoCountersStillMarksTheFigure.
//
// PRESENCE is the claim, not the counters. usage.Snapshot.Degraded is a pointer precisely so
// a clean read serialises nothing, so a producer that sent the object is saying it found
// damage — and reading its zeros as "checked, fine" is the same class of false reassurance
// as $0.00 over unpriced traffic. Tightening snapshotDamaged to require a non-zero counter
// survived every other test in this file.
func TestRenderSpendStrip_ADisclosureWithNoCountersStillMarksTheFigure(t *testing.T) {
	s := spendSummary{
		TodayUSD: 4.17, HasToday: true, TodayPriceable: 400,
		TodayDegraded: &usage.Degraded{},
		HasSnapshot:   true,
	}
	got := renderSpendStrip(s, 200)
	if !strings.Contains(got, damagedMarker+"$4.1700") {
		t.Errorf("strip %q reads a counterless disclosure as a clean read", got)
	}
	if !strings.Contains(got, "rows lost") {
		t.Errorf("strip %q says nothing about a disclosure it was sent", got)
	}
}

// TestRenderSpendStrip_AClampedFigureWearsTheShortMarkerOnEitherReading.
//
// usage.Counts.Saturated says an addition into a window's totals reached the int64 ceiling and
// was CLAMPED rather than allowed to wrap, so the figure is a floor by an amount nothing in the
// response can state. The server aggregated it and no client in cmd/abctl read it, which is the
// same defect usage.Snapshot.Degraded shipped with — and here it is worse than a missing caveat,
// because a clamped figure is not merely low but absurd, and an absurd number with no marker
// reads as a real one.
//
// BOTH READINGS, which is the asymmetry with damage. Degraded is a property of a LEDGER READ, so
// only the day figure can carry one. Saturated is on usage.Counts, and the ring's Add clamps
// exactly like the ledger's fold does — so a rolling hour can overflow with no ledger anywhere
// near it, and a strip that marked only the day would publish the other figure bare.
func TestRenderSpendStrip_AClampedFigureWearsTheShortMarkerOnEitherReading(t *testing.T) {
	t.Run("the day", func(t *testing.T) {
		s := spendSummary{
			TodayUSD: 4.17, HasToday: true, TodayPriceable: 400, TodayClamped: true,
			WindowUSD: 1.12, WindowLabel: "1h", Priced: true, HasSnapshot: true, Priceable: 10,
		}
		got := renderSpendStrip(s, 200)
		if !strings.Contains(got, damagedMarker+"$4.1700 today") {
			t.Errorf("strip %q publishes a clamped day figure with no marker on it", got)
		}
		if !strings.Contains(got, saturatedNote) {
			t.Errorf("strip %q does not say the day's figures are floors", got)
		}
		// The clean rolling figure must NOT be marked: a caveat on the wrong figure is the
		// misattribution moneyFigure was built to end.
		if strings.Contains(got, damagedMarker+"$1.1200") {
			t.Errorf("strip %q marks an unclamped window figure as short", got)
		}
	})
	t.Run("the rolling window", func(t *testing.T) {
		s := spendSummary{
			TodayUSD: 4.17, HasToday: true, TodayPriceable: 400,
			WindowUSD: 1.12, WindowLabel: "1h", Priced: true, HasSnapshot: true, Priceable: 10,
			Clamped: true,
		}
		got := renderSpendStrip(s, 200)
		// The group's span sits OUTSIDE the markers — "1h: !$1.1200". The markers keep their own
		// order among themselves (damaged outside inexact, so the leftmost cell is the most serious
		// claim); the span is not a claim about the figure's accuracy but a statement of what it
		// covers, so it reads first without displacing anything.
		if !strings.Contains(got, "1h: "+damagedMarker+"$1.1200") {
			t.Errorf("strip %q publishes a clamped rolling figure with no marker on it", got)
		}
		if strings.Contains(got, damagedMarker+"$4.1700") {
			t.Errorf("strip %q marks an unclamped day figure as short", got)
		}
	})
}

// TestRenderSpendStrip_ACleanAggregateCarriesNoClampCaveat is the mirror. A permanent "figures
// are floors" note over an aggregate that never clamped is the same false signal as a coverage
// warning that never clears — and it would appear on every strip there is.
func TestRenderSpendStrip_ACleanAggregateCarriesNoClampCaveat(t *testing.T) {
	s := spendSummary{
		TodayUSD: 4.17, HasToday: true, TodayPriceable: 400,
		WindowUSD: 1.12, WindowLabel: "1h", Priced: true, HasSnapshot: true, Priceable: 10,
	}
	got := renderSpendStrip(s, 200)
	if strings.Contains(got, saturatedNote) || strings.Contains(got, damagedMarker) {
		t.Errorf("strip %q qualifies a clean aggregate", got)
	}
}

// TestRenderSpendStrip_TheClampMarkerSurvivesNarrowing.
//
// The words are droppable, the marker is not — fitStripFigures gives up every figure's
// explanation before it gives up a reading. A narrow terminal losing "clamped, figures are
// floors" is a cost; losing the "!" would publish a clamped total as a settled one, which is the
// worst outcome available on this line.
func TestRenderSpendStrip_TheClampMarkerSurvivesNarrowing(t *testing.T) {
	s := spendSummary{
		TodayUSD: 4.17, HasToday: true, TodayPriceable: 400, TodayIncomplete: 7,
		TodayUnpriced: 100, TodayClamped: true,
		HasSnapshot: true,
	}
	for w := 1; w <= 200; w++ {
		got := renderSpendStrip(s, w)
		if got == "" {
			continue
		}
		if !strings.Contains(got, damagedMarker+inexactMarker+"$4.1700"+partialMarker) {
			t.Errorf("width %d: %q lost one of the three markers", w, got)
		}
	}
}

// TestMoneyFigure_OneMarkerForBothWaysAFigureCanBeShort.
//
// damagedMarker's two causes take ONE cell between them, and each keeps its own words. "!!" on a
// figure that is short twice over reads as emphasis rather than as two facts, and a fourth glyph
// would deepen the strip's vocabulary to draw a distinction that changes nothing about how the
// number must be read — where the WORDS do change what an operator goes and looks at: a day file
// for one, whatever produced 9.2e18 micros of traffic for the other.
func TestMoneyFigure_OneMarkerForBothWaysAFigureCanBeShort(t *testing.T) {
	fig := moneyFigure(4.17, "today", 0, 400, 0, &usage.Degraded{SkippedLines: 3}, true)
	if n := strings.Count(fig.compact, damagedMarker); n != 1 {
		t.Errorf("compact form %q carries %d %q cells, want exactly 1", fig.compact, n, damagedMarker)
	}
	for _, want := range []string{saturatedNote, "3 lines lost"} {
		if !strings.Contains(fig.full, want) {
			t.Errorf("full form %q is missing %q — one marker must not collapse two causes into "+
				"one explanation", fig.full, want)
		}
	}
	// The clamp leads: it is short in every column of the aggregate, where a damaged read is
	// short in the dollars. Same order as the Cost pane and `abctl cost`.
	if clamp, damaged := strings.Index(fig.full, saturatedNote), strings.Index(fig.full, "3 lines lost"); clamp > damaged {
		t.Errorf("the damaged-read note outranks the clamp in %q", fig.full)
	}
}

// The width matrix again, with the clamp caveat live. Same contract as ever: one line, never
// wider than the budget, never a clipped figure. The CJK label is two display columns per rune,
// so any len()- or rune-based arithmetic in the new caveat path renders wider than it claims.
func TestRenderSpendStrip_TheClampCaveatObeysTheWidthContract(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    spendSummary
	}{
		{"a clamped day beside a clamped hour", spendSummary{
			TodayUSD: 4.17, HasToday: true, TodayPriceable: 400, TodayClamped: true,
			WindowUSD: 1.12, WindowLabel: "1h", Priced: true,
			HasSnapshot: true, Priceable: 318, Clamped: true,
		}},
		{"every caveat a day can carry at once", spendSummary{
			TodayUSD: 0.0031, HasToday: true, TodayUnpriced: 399, TodayPriceable: 400,
			TodayIncomplete: 7, TodayClamped: true,
			TodayDegraded: &usage.Degraded{SkippedLines: 1_234_567, TruncatedDays: 89},
			WindowUSD:     1.12, WindowLabel: "過去一時間", Priced: true,
			HasSnapshot: true, Unpriced: 12, Priceable: 318, Incomplete: 3, Clamped: true,
			SavedUSD: 0.24, HasSaved: true,
			Age: 3 * time.Minute, Stale: true,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for w := 1; w <= 200; w++ {
				assertStripFits(t, tc.s, w)
			}
		})
	}
}

// An unpriced window must still render every reading it DOES have.
//
// This is the boundary no test crossed, which is how four comments in spend.go came to
// describe behaviour the renderer defeated. spendSummary reads tokens, errors, the cache ratio
// and the saving OUTSIDE its Priced guard, on the stated grounds that "suppressing them
// alongside the money would blank the only readings a deployment with no rate table has" and
// that "a window that priced nothing and pruned something reports 'cost unavailable' beside a
// real saved figure, and both are true". The renderer returned before reaching any of them.
//
// Every unpriced case in this file either set HasToday (which escaped the early return) or
// left the volume fields zero, so the data-layer tests passed while the renderer threw the
// values away. This one populates both halves.
//
// REACHABLE, not exotic: any endpoint absent from the rate card sits at Priced == false
// permanently, and usage/pricing_test.go pins a saving on an unpriced request as a supported
// state.
func TestRenderSpendStrip_AnUnpricedWindowStillShowsWhatItKnows(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    spendSummary
	}{
		{
			// Priceable traffic nothing could price: the coverage note explains the absence.
			name: "priceable but unpriced",
			s: spendSummary{
				WindowLabel: "1h", Priced: false, HasSnapshot: true,
				Unpriced: 318, Priceable: 318,
				SavedUSD: 0.1804, HasSaved: true,
				CacheHitPct: 81, HasCacheHit: true, Tokens: 9_890_000, Errors: 2,
			},
		},
		{
			// Nothing priceable at all — and a saving is STILL possible, because a pruned
			// request whose response could not be parsed carries no model and so is not
			// priceable, while the prompt was pruned all the same.
			name: "nothing priceable",
			s: spendSummary{
				WindowLabel: "1h", Priced: false, HasSnapshot: true,
				SavedUSD: 0.1804, HasSaved: true,
				CacheHitPct: 81, HasCacheHit: true, Tokens: 9_890_000, Errors: 2,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := renderSpendStrip(tc.s, 200)

			// The money reading is still refused, which is the part that already worked.
			if !strings.Contains(got, "unavailable") && !strings.Contains(got, "no priceable") {
				t.Errorf("strip %q no longer says the cost is unknown", got)
			}
			if strings.Contains(got, "$0.00") {
				t.Errorf("strip %q renders $0.00 for an unknown cost", got)
			}
			// And every reading that IS known survives to the screen.
			for _, want := range []string{"~$0.1804", "saved", "cache 81%", "9.9M", "2 err"} {
				if !strings.Contains(got, want) {
					t.Errorf("strip %q dropped %q: the money is unknown, this reading is not",
						got, want)
				}
			}
		})
	}
}

// The pane pointer is a place to look, not a reading, so it must not outrank one. At a width
// that cannot hold everything, the token count survives and the advice does not.
//
// BOTH HALVES ASSERTED AT A WIDTH THAT PROVES IT. The first version probed 44 columns, where the
// fitter emits "SPEND  cost unavailable" and nothing else: the hint was absent, the `&&`
// short-circuited, and the test asserted nothing at all — it could not tell "the advice yielded"
// from "everything yielded", which is the whole claim. 64 columns is where the token count
// survives and the hint does not, so requiring BOTH cannot pass vacuously.
//
// The ORDER is asserted too, at a width that holds everything, because that is the invariant
// independent of any particular column count — the ladder drops from the right, so position IS
// priority.
func TestRenderSpendStrip_TheUsageHintYieldsToRealReadings(t *testing.T) {
	s := spendSummary{
		WindowLabel: "1h", Priced: false, HasSnapshot: true, Unpriced: 318, Priceable: 318,
		HasCacheHit: true, CacheHitPct: 81, Tokens: 9_890_000,
	}

	wide := renderSpendStrip(s, 200)
	hintAt, tokensAt := strings.Index(wide, "[u] usage"), strings.Index(wide, "9.9M")
	if hintAt < 0 {
		t.Fatalf("a 200-column strip %q dropped the pane pointer entirely", wide)
	}
	if tokensAt < 0 {
		t.Fatalf("a 200-column strip %q dropped the token count", wide)
	}
	if hintAt < tokensAt {
		t.Errorf("strip %q puts the advice ahead of the reading: the ladder drops from the right, "+
			"so that is the order in which they would be given up", wide)
	}

	// 64 columns: room for the reading, not for the advice.
	narrow := renderSpendStrip(s, 64)
	if !strings.Contains(narrow, "9.9M") {
		t.Fatalf("strip %q at 64 columns dropped the token count, so this width cannot "+
			"distinguish yielding advice from yielding everything", narrow)
	}
	if strings.Contains(narrow, "[u] usage") {
		t.Errorf("strip %q kept the advice at a width that had to give something up", narrow)
	}
}

// The coverage gap must be stated ONCE.
//
// Two figures can state it: the bare note beside "cost unavailable" when nothing is priced
// anywhere, and the window-labelled note for a today figure sitting beside a rolling window
// that priced nothing. Letting the unpriced branch fall through to the figures list put both on
// the same line — the same gap twice, once bare and once labelled — which is the kind of thing
// a reader takes as two different findings.
func TestRenderSpendStrip_TheCoverageGapIsStatedOnce(t *testing.T) {
	base := spendSummary{
		WindowLabel: "1h", Priced: false, HasSnapshot: true, Unpriced: 318, Priceable: 318,
		Tokens: 9_890_000,
	}
	for _, tc := range []struct {
		name  string
		s     spendSummary
		label bool // does the surviving note wear the window label?
	}{
		// No money reading at all: the note sits beside "cost unavailable" and needs no label,
		// because adjacency carries it.
		{name: "nothing priced", s: base},
		// A today figure separates them, so the note must wear the window's label or it reads
		// as qualifying the day.
		{name: "today beside an unpriced window", label: true, s: func() spendSummary {
			d := base
			d.HasToday, d.TodayUSD, d.TodayPriceable = true, 30.935, 100
			return d
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := renderSpendStrip(tc.s, 200)
			if n := strings.Count(got, "318 of 318 unpriced"); n != 1 {
				t.Errorf("strip %q states the same gap %d times, want once", got, n)
			}
			// The label is the GROUP's prefix now, so it appears on the note only when the note
			// LEADS the window group. Which is exactly the distinction this table draws: beside
			// "cost unavailable" the note is second and adjacency carries it, while a today figure
			// pushes it to the front of the group and the span comes with the position.
			labelled := strings.Contains(got, "1h: 318 of 318 unpriced")
			if labelled != tc.label {
				t.Errorf("strip %q: window-labelled = %v, want %v — an unlabelled note is only "+
					"safe immediately after the reading it qualifies", got, labelled, tc.label)
			}
		})
	}
}

// A FAILED WINDOW POLL MUST NOT BLANK A GOOD DAY FIGURE.
//
// spendState.todaySnap gives this as the reason the two chains are split at all: "the two can
// fail independently — an older proxy answers the window fine and 400s on window=today — and one
// broken figure must not blank the other." It held in one direction only. Today failing left the
// window alone; the window failing discarded today, because the renderer returned on Failed
// before reading any figure.
//
// Both directions asserted, since a fix that blanked the window instead would satisfy either
// half alone.
func TestRenderSpendStrip_EitherChainCanFailWithoutBlankingTheOther(t *testing.T) {
	t.Run("window failed, day answered", func(t *testing.T) {
		got := renderSpendStrip(spendSummary{
			Failed: true, WindowLabel: "1h",
			TodayUSD: 30.935, HasToday: true, TodayPriceable: 100,
			SavedUSD: 0.1804, HasSaved: true,
		}, 200)
		if !strings.Contains(got, "$30.9350 today") {
			t.Errorf("strip %q lost the day figure to a failed WINDOW poll — the two chains are "+
				"split precisely so that cannot happen", got)
		}
		if !strings.Contains(got, "~$0.1804") {
			t.Errorf("strip %q lost the saving as well", got)
		}
		// And it says which reading is missing, with the window's label so it cannot be read
		// as qualifying the day.
		if !strings.Contains(got, "1h: poll failed") {
			t.Errorf("strip %q does not say the window poll failed, or says it unlabelled beside "+
				"a day figure", got)
		}
	})

	t.Run("day failed, window answered", func(t *testing.T) {
		// HasToday false is how a failed or absent today chain arrives; the window is fine.
		got := renderSpendStrip(spendSummary{
			WindowLabel: "1h", Priced: true, WindowUSD: 2.91, Priceable: 10, HasSnapshot: true,
			Tokens: 9_890_000,
		}, 200)
		if !strings.Contains(got, "1h: $2.9100") {
			t.Errorf("strip %q lost the window figure to an absent day figure", got)
		}
		if strings.Contains(got, "poll failed") {
			t.Errorf("strip %q reports a failure for a window poll that answered", got)
		}
	})
}

// A wedged poll must be visible on the figure it belongs to, and the day figure is the one that
// could go hours stale unnoticed: it outranks the window, polls twelve times more slowly, and is
// the last thing the fitter drops.
//
// The age is ONE reading for the line, taken from the OLDER chain — so a fresh window poll
// cannot vouch for a wedged day poll.
func TestSpendSummary_TheAgeComesFromTheOlderChain(t *testing.T) {
	now := time.Now()
	m := &model{}
	m.spend.snap = &usage.Snapshot{
		Window: "1h", Priced: true,
		Totals: usage.Counts{Requests: 1, CostMicros: 1, PricedRequests: 1, PriceableRequests: 1},
	}
	// The window answered a moment ago; the day chain has been wedged for an hour.
	m.spend.lastFetch = now
	m.spend.todayLastFetch = now.Add(-time.Hour)

	got := m.spendSummary()
	if !got.Stale {
		t.Fatal("a day poll wedged for an hour reports no staleness, because a window poll from " +
			"one second ago was the only clock consulted — which puts the gap on the most " +
			"prominent figure on the line")
	}
	if got.Age < 59*time.Minute {
		t.Errorf("Age = %v, want ~1h: the age must be the OLDER chain's, not the fresher one's",
			got.Age)
	}

	// And a chain that has NEVER answered is not infinitely stale: today is unavailable on a
	// proxy with no ledger, and reporting that as staleness would mark every Kubernetes
	// deployment's strip permanently old.
	m.spend.todayLastFetch = time.Time{}
	if fresh := m.spendSummary(); fresh.Stale {
		t.Errorf("Stale = true with a today chain that never answered (age %v); an absent ledger "+
			"is not a wedged poll", fresh.Age)
	}
}

// The saving's label is its IDENTITY, not an explanation, so it can never be dropped while the
// figure is shown.
//
// ~ is inexactMarker, and on a money figure that means "lower bound" — an inexact spend figure
// renders "~$4.1700 today" and keeps its label. So a bare "~$0.1804" sitting between two
// labelled figures reads as spend whose label the ladder happened to drop, which is a different
// claim rather than a terser one. The ladder's rule is that it gives up explanations before
// figures; this is not an explanation.
func TestRenderSpendStrip_TheSavingNeverLosesItsLabel(t *testing.T) {
	s := spendSummary{
		TodayUSD: 30.935, HasToday: true, TodayPriceable: 100,
		SavedUSD: 0.1804, HasSaved: true,
		WindowUSD: 2.91, WindowLabel: "1h", Priced: true, HasSnapshot: true,
		CacheHitPct: 81, HasCacheHit: true, Tokens: 9_890_000, Errors: 2,
	}
	amount := inexactMarker + formatUSDCell(0.1804)
	shown := 0
	for _, w := range []int{200, 120, 100, 90, 80, 72, 64, 56, 48, 40, 32, 24} {
		got := renderSpendStrip(s, w)
		if !strings.Contains(got, amount) {
			continue // the ladder dropped the whole figure, which is the allowed outcome
		}
		shown++
		if !strings.Contains(got, "saved "+amount) {
			t.Errorf("width %d: strip %q shows the amount without its label — between two "+
				"labelled money figures that reads as a spend lower bound", w, got)
		}
	}
	if shown == 0 {
		t.Fatal("the saving never appeared at any width, so nothing above was asserted")
	}
}
