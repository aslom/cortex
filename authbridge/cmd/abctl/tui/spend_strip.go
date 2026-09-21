package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/rossoctl/cortex/authbridge/authlib/usage"
)

// stripLabel prefixes the line. Kept short: it is spent on every width.
const stripLabel = "SPEND"

// stripGap separates whole figures. Three spaces rather than a glyph separator so
// the figures read as independent readings rather than as one expression — the
// same spacing the footer's status line uses between its own readings.
const stripGap = "   "

// inexactMarker precedes a dollar figure that is NOT EXACT: at least one of the priced
// requests behind it carries a figure the aggregator could not settle exactly
// (usage.Counts.IncompleteRequests) — a stream that died before its output count, so the
// amount is a LOWER BOUND, or a gateway that reported only a total. It reads
// "approximately", and the real number is at least this much.
//
// ONE SPELLING, EVERYWHERE. This is the marker the strip puts on the today and window
// figures, the sessions table puts on its SAVED cell (see sessionMoneyCell) and the Usage
// pane puts on its cost cell (see renderCostSummary). A branch that carries a commit
// titled "Stop publishing a truncated stream's floor as an exact total" had three money
// surfaces republishing that floor with no annotation at all, and three different
// annotations would have been barely better: a marker a reader has to learn twice is a
// marker they learn once and misread thereafter.
//
// One display column, so it survives every width the strip's fitter can produce and
// every width fitTableColumns can leave the COST column at. The burn rate has always
// worn the same "~" for the same reason — a derived rate is not an exact figure either.
const inexactMarker = "~"

// partialMarker follows a dollar figure that covers only PART of the traffic it
// appears to be about: something in the window was priceable and carries no figure, so
// the real total is LARGER than the number shown.
//
// One display column, and it rides on the figure itself rather than in the words beside
// it. That is the whole point: fitStripFigures may shorten a caveat to nothing and may
// drop a whole figure, but while a figure is on screen its marker is on screen with it,
// so a partial total can never be published as a complete one. It reads as "and more",
// which is what a coverage gap means for a total.
const partialMarker = "+"

// damagedMarker precedes a dollar figure that is SHORT of real spend by an amount nothing in
// the response can state. It has TWO CAUSES and one meaning:
//
//   - THE LEDGER READ WAS INCOMPLETE: rows the answer needed could not be read at all — a day
//     file that lost lines, or one whose scan was abandoned part-way. See
//     usage.Snapshot.Degraded and snapshotDamaged.
//   - THE AGGREGATE ARITHMETIC WAS CLAMPED: an addition into the totals hit the int64 ceiling
//     and was capped rather than allowed to wrap, so every figure in that window is a floor.
//     See usage.Counts.Saturated, whose own doc says the disclosure "travels on the same
//     response as the number it qualifies, which is where an operator reading that number will
//     see it" — this is that place.
//
// ONE GLYPH FOR BOTH, because the claim a reader acts on is identical: the number on screen is
// less than the money that was spent, and by how much is unstatable. The words beside it name
// which cause, exactly as inexactMarker carries one glyph over the two directions
// usage.Snapshot.IncompleteBy distinguishes. A second glyph would make the strip's vocabulary
// four marks deep to draw a distinction that changes nothing about how the figure must be
// read. figureIsShort is the one spelling of the test.
//
// A THIRD GLYPH, and deliberately not one of the other two. Degraded's own doc is explicit
// that this is a different claim from usage.Counts.IncompleteRequests and that the two must
// not be merged or shown under one marker: inexactMarker says a figure the total CARRIES is
// a floor, this says spend is MISSING FROM THE SUM. partialMarker is closer — the real
// total is larger either way — but a coverage gap is COUNTED and nameable ("412 requests on
// api.openai.com gpt-5"), while this shortfall is unstatable, and collapsing them would
// tell an operator to add a pricing entry for a corrupt file.
//
// Prefixed OUTSIDE inexactMarker, so the leftmost cell carries the most serious claim and
// a figure that is both reads "!~$4.1700+" — three one-column claims, each with its own
// meaning, rather than one marker doing two jobs badly.
//
// One display column, so it survives every width fitStripFigures can produce. It rides on
// the FIGURE rather than in the words beside it, for the reason partialMarker records: the
// fitter may shorten a caveat to nothing, but while a figure is on screen its markers are
// too, so a total that is short can never be published as a complete one. The cost is that
// each marker widens the figure by a column, which moves the width at which a LATER reading
// is dropped — accepted, and the same trade the other two already make, because the marker
// is the fact and the words are the explanation.
const damagedMarker = "!"

// figureIsShort reports that a figure wears damagedMarker: it is short of real spend by an
// amount nothing in the response can state, from either of the two causes that produce that
// claim. See damagedMarker.
//
// A function rather than the disjunction written at each site, for the reason negativeCost is
// one: the same test is made by the strip's today figure, the Cost pane's TOTAL, the pane's
// caveat block and `abctl cost`, and a predicate written four times is a predicate that drifts.
// usage.Counts.Saturated arrived after usage.Snapshot.Degraded and only one surface picked it
// up; the next disclosure of this class should have one place to be added.
func figureIsShort(degraded *usage.Degraded, saturated bool) bool {
	return snapshotDamaged(degraded) || saturated
}

// stripFigure is one reading in the strip, in the two forms it can take.
//
// full spells the caveat out; compact keeps the figure and its marker and drops only
// the words. Both forms of a qualified figure carry the marker — see partialMarker —
// so degradation costs an explanation and never the fact.
//
// Two forms rather than a general shortener because the strip has exactly one thing to
// give up under width pressure, and the fitter's job is to choose between whole
// readings, not to edit them.
type stripFigure struct {
	full    string
	compact string
}

// plainFigure is a reading with nothing to qualify: both forms are the same string.
func plainFigure(s string) stripFigure { return stripFigure{full: s, compact: s} }

// coverageNote is the one spelling of a coverage gap, shared by every branch so the
// wording cannot drift between them.
func coverageNote(unpriced, priceable int64) string {
	return fmt.Sprintf("%d of %d unpriced", unpriced, priceable)
}

// damagedNote is the strip's spelling of a damaged ledger read: the shortest form that
// still says WHAT was lost, because "incomplete" on its own gives a reader nothing to act
// on where "1 day file lost" names something to go and look at.
//
// One fact at three verbosities, the same relationship coverageNote has with the Cost
// pane's "covers N of M priceable requests" and cmd_cost.go's line: costDamagedNote spells
// it out where there is room for a sentence, this is what a strip can afford.
//
// It rides in the figure's FULL form only. damagedMarker is what survives into the compact
// form, so width pressure costs the explanation and never the fact.
//
// "lost", not "skipped": the ledger's own verbs describe what IT did, and the reader of a
// spend line cares what the number is missing.
func damagedNote(d *usage.Degraded) string {
	switch {
	case d.SkippedLines > 0 && d.TruncatedDays > 0:
		return fmt.Sprintf("%d lines, %d day file%s lost",
			d.SkippedLines, d.TruncatedDays, plural(int(d.TruncatedDays)))
	case d.SkippedLines > 0:
		return fmt.Sprintf("%d line%s lost", d.SkippedLines, plural(int(d.SkippedLines)))
	case d.TruncatedDays > 0:
		return fmt.Sprintf("%d day file%s lost", d.TruncatedDays, plural(int(d.TruncatedDays)))
	default:
		// A disclosure carrying no counters. Presence is still the claim — see
		// snapshotDamaged — and this is the least it can say without inventing a number.
		return "rows lost"
	}
}

// saturatedNote is the strip's spelling of a clamped aggregate: the shortest form that still
// says which way the figure is wrong.
//
// One fact at three verbosities, the same relationship damagedNote has with
// costSaturatedNote's sentence and cmd_cost.go's line. "clamped" alone would leave a reader
// guessing whether the number shown is too big or too small, and "floors" is the half they
// act on — the real spend is LARGER than the figure beside it.
//
// A constant, because usage.Counts.Saturated is a bool: there is no count of clamped
// additions to interpolate, and its doc explains why there deliberately is not one.
//
// It rides in the figure's FULL form only. damagedMarker is what survives into the compact
// form, exactly as with damagedNote, so width pressure costs the explanation and never the fact.
const saturatedNote = "clamped, figures are floors"

// moneyAmount is the marked figure alone, without a label or a caveat clause.
//
// Extracted from moneyFigure so the KPI band and the strip cannot disagree about which
// markers a reading earns. THE MARKERS ARE THE COMPACT DISCLOSURE: the band has no room for
// the parenthesised prose moneyFigure adds, and neither does the strip at a narrow width —
// fitStripFigures already falls back to this same marked form there. So a surface showing
// markers only is the established degradation rather than a new loss.
//
// Marker order is load-bearing and unchanged: damaged outermost so the leftmost glyph is the
// most serious claim, inexact next to the amount, partial trailing.
//
// THE PRECISION IS THE CALLER'S, and splitting it out is what keeps the rule beside
// formatUSDTotal true. This function used to format as well as mark, so every surface reaching
// it got one precision — and its callers are on both sides of the boundary: the band's TODAY and
// LAST are span totals, while moneyFigure below feeds the drawer's PER-MODEL rows, which are
// per-item. Formatting here in cents silently rounded the model column too, which the rule says
// it must not be.
//
// moneyFigure's OTHER two callers are on the wrong side of that rule and are dead: renderSpendStrip
// passes it s.TodayUSD and s.WindowUSD, span totals that come out at four decimals, and nothing in
// production calls renderSpendStrip any more — app.go mentions it only in comments, and
// renderSpendBand is the live renderer. Said here because this comment is where a reviver of the
// strip would look for the rule: reviving it means routing those two through moneyTotal.
func moneyAmount(usd float64, unpriced, priceable, incomplete int64,
	degraded *usage.Degraded, saturated bool) string {
	return markMoney(formatUSDCell(usd), unpriced, priceable, incomplete, degraded, saturated)
}

// moneyTotal is moneyAmount for a SPAN TOTAL — the day's spend, the window's — so it reads in
// cents. See the precision rule beside formatUSDTotal.
func moneyTotal(usd float64, unpriced, priceable, incomplete int64,
	degraded *usage.Degraded, saturated bool) string {
	return markMoney(formatUSDTotal(usd), unpriced, priceable, incomplete, degraded, saturated)
}

// markMoney puts the disclosure markers on an already-formatted amount.
func markMoney(amount string, unpriced, priceable, incomplete int64,
	degraded *usage.Degraded, saturated bool) string {
	if incomplete > 0 {
		amount = inexactMarker + amount
	}
	if figureIsShort(degraded, saturated) {
		amount = damagedMarker + amount
	}
	if unpriced > 0 && priceable > 0 {
		amount += partialMarker
	}
	return amount
}

// moneyFigure builds one dollar reading together with the caveats that belong to IT.
//
// label is the figure's own suffix — "today", "/1h" — and it is why this takes one at
// all: the coverage note used to ride at the END of the line, describing the rolling
// window, while the today figure sat at the front as the headline. "SPEND $4.1700
// today  $1.1200 /1h  40 of 40 unpriced" reads as qualifying the day and describes the
// hour. A caveat attached to its own figure cannot be misread that way, and a figure
// with no caveat of its own now says so by carrying no marker.
//
// Exactness and coverage are separate claims about the same number — whether the figure
// is the real one, and how much of the traffic it covers — and both can be true at once.
// They are stated in that order, matching cmd_cost.go, whose comment records why: the
// exactness caveat qualifies the dollar figure itself, where coverage qualifies how much
// of the traffic the figure is about.
//
// degraded is a THIRD claim and it goes first, because it is the only one of the three that
// says the SUM is incomplete rather than qualifying a figure the sum contains. nil means
// the read was clean and nothing is rendered for it — see snapshotDamaged, which is where
// the pointer semantics are argued. Only a ledger-backed figure can carry one, so the
// window reading passes nil.
//
// saturated is a FOURTH claim that SHARES the third one's glyph, because it makes the same
// demand of a reader: this figure is short of real spend by an amount nothing can state. It is
// also the only claim on this line that BOTH readings can carry — usage.Counts.Saturated lives
// on Counts, so the ring's window totals and the ledger's day totals can each clamp, where a
// damaged read is ledger-only. See figureIsShort and damagedMarker.
func moneyFigure(usd float64, label string, unpriced, priceable, incomplete int64,
	degraded *usage.Degraded, saturated bool) stripFigure {
	amount := moneyAmount(usd, unpriced, priceable, incomplete, degraded, saturated)
	// A gap is only readable with a denominator, and a denominator of zero is not a
	// gap at all — it is a window with nothing to price, which the caller handles.
	// Recomputed here for the caveat list; moneyAmount owns the MARKER.
	partial := unpriced > 0 && priceable > 0
	// AN EMPTY LABEL ADDS NO SEPARATOR. Unconditional concatenation left a trailing space on
	// every unlabelled figure, which the joiner then compounded into a four-space gap — visible
	// in the drawer, whose rows have always passed "" here ("claude-opus-5   $35.5797    234
	// req"), and one column of a width budget this file measures to the cell. Now that the window
	// reading passes "" too — its span is on the group, see labelSpan — the stray column would be
	// on the strip's own money figure.
	//
	// THE PARTIAL MARKER IS NOT APPLIED HERE. It was, on this change's own base; moneyAmount
	// owns all three markers now, so re-applying it rendered the figure twice-marked ("$1.12++").
	// `partial` above survives because the CAVEAT LIST below still needs it.
	reading := amount
	if label != "" {
		reading += " " + label
	}
	fig := plainFigure(reading)
	var caveats []string
	// The clamp leads even the damaged read: it is short in every column of the aggregate, not
	// only in the dollars, and it is the reason a figure this line renders can be absurd rather
	// than merely low. Both can be true of one reading, and each keeps its own words — one sends
	// an operator to a day file, the other to whatever produced 9.2e18 micros of traffic.
	if saturated {
		caveats = append(caveats, saturatedNote)
	}
	if snapshotDamaged(degraded) {
		caveats = append(caveats, damagedNote(degraded))
	}
	if incomplete > 0 {
		// No denominator: spendSummary carries the day's and the window's priced counts
		// nowhere, and the count alone is what a strip has room for. The Cost pane states
		// the full "N of M priced figures are lower bounds" for a reader who wants it.
		caveats = append(caveats, fmt.Sprintf("%d inexact", incomplete))
	}
	if partial {
		caveats = append(caveats, coverageNote(unpriced, priceable))
	}
	if len(caveats) > 0 {
		fig.full = fig.compact + " (" + strings.Join(caveats, ", ") + ")"
	}
	return fig
}

// renderSpendStrip draws the always-on spend line.
//
// A pure function of its arguments so it can be table-tested at many widths,
// which is where its entire risk lives. Chrome that overflows wraps, and a
// wrapped chrome line costs a row of the table below it.
//
// Degradation drops WHOLE FIGURES from the right and never clips a number.
// #953 asks for "no truncated numbers", and a half-rendered dollar amount is
// worse than a missing one: "$1.1" reads as a real, smaller figure rather than as
// an incomplete one. This is the same discipline as fitHintLine, mirrored —
// helpView orders its hints so the essential ones come last and fitHintLine cuts
// the front; the strip's essential figure is first, so it cuts the back.
//
// Returns "" in exactly two cases, and they are deliberately the only two:
//
//  1. No poll has answered yet (!HasSnapshot). "We have not looked" is honest and
//     self-corrects within one poll interval.
//  2. The terminal is too narrow for even one WHOLE figure. That threshold is the
//     width of the FIRST figure's most compact form and nothing more, because
//     fitStripFigures drops the LABEL, and then the caveat's words, before it drops a
//     number: "1h: $1.1200" is 11 columns and renders bare from width 11 up, so ""
//     appears only at 10 or below. (That arithmetic survived the span moving from a suffix to
//     a group prefix untouched — "$1.1200 /1h" was 11 columns too, which is why labelSpan costs
//     this threshold nothing.) Each marker a figure carries makes it one column wider,
//     so a first figure wearing all three (damaged, inexact, partial) moves that threshold
//     by three — the markers are never what gets dropped. (This note said "about 18" — label plus figure
//     — which was right before the label-drop fallback below existed and has been wrong
//     by 7 since.) Accepted rather than
//     fixed: at that width there is no honest short form, and clipping a number is
//     forbidden. The row stays reserved, because making the reservation depend on
//     the rendered result would mean re-running layout() outside WindowSizeMsg and
//     resizing every table as data came and went, which is worse than a blank line
//     in a terminal too narrow to show a dollar figure at all.
//
// Every OTHER state says something: a failed poll says "cost unavailable", an
// unpriced window says so with its coverage, and a window with no priceable
// traffic says that. An always-on strip that renders nothing has failed at its
// only job.
//
// "Today" exists — the durable cost ledger supplies it and spend.go's
// applyTodayFigure sets HasToday only when the server really served that window and
// priced it. So does "saved", from usage.Counts.AvoidedMicros, and it comes in two spans:
// the DAY's when the ledger answered, the window's as a fallback. Neither is inferred from a
// figure being non-zero — a deployment that does not prune says nothing there rather than
// "saved $0.00", which would assert that pruning saved nothing.
//
// FIGURES ARE GROUPED BY SPAN and each group names its own once. That is what makes the line
// readable rather than merely correct: see the comment on the three lists inside.
func renderSpendStrip(s spendSummary, width int) string {
	if width <= 0 {
		return ""
	}

	// No poll has answered yet. THIS silence is honest: it says "we have not looked", which
	// is true, brief, and self-correcting within one poll interval. It is the only case where
	// "" is the right answer, and it is checked before anything else so the branches below can
	// assume there is a snapshot to report figures from.
	// NOT when the poll FAILED, which is a thing to report rather than an absence: silence for a
	// persistently failing endpoint buys a permanent blank line above the footer — the row is
	// reserved on height alone — and no diagnostic anywhere on screen.
	if !s.Priced && !s.HasToday && !s.HasSnapshot && !s.Failed {
		return ""
	}

	// Ordered most to least important; the tail is what a narrow terminal loses.
	//
	// EVERY FIGURE CARRIES ITS OWN CAVEAT. A caveat built from one window's counters
	// and rendered beside another window's figure is not a warning, it is a
	// misattribution — and the figure it silently vouched for was "today", the headline
	// of this branch and the one the ledger is most likely to leave partial.
	//
	// THREE LISTS, ONE PER SPAN, because the caveats were not the only thing this line could
	// misattribute: the SPAN was, by the same mechanism. The strip carries readings from two polls
	// — the ledger's day and the ring's hour — and it labelled only the two money figures, which
	// left "saved", the cache ratio and the token count unlabelled among them. A reader then has
	// no rule to apply, because position does not encode the span either: `saved` was the HOUR's
	// and stood second, between the day's cost and the hour's.
	//
	// Measured on a local proxy: an hour holding 84.6M tokens rendered "85M tokens" beside
	// "$61.7655 today", above a sessions table whose own TOKENS column summed to 123.8M. Three
	// spans, one of them stated — and the day itself held 122.5M, so the unlabelled figure
	// matched neither of its neighbours. Every number was right and the line was unreadable.
	//
	// So each span's figures are collected together and the span is named ONCE, on the group.
	// tail is NEITHER span: it holds the readings that qualify the whole answer — the pane
	// pointer, and the staleness note applyAges builds from the OLDER OF BOTH CHAINS, which
	// inside either group would claim to be about one of them.
	var today, window, tail []stripFigure
	// NOTHING PRICED ANYWHERE: say so rather than assert a zero — usage_render.go establishes
	// that a zero cost and an unknown cost are different answers, and only one of them means
	// the traffic was free.
	//
	// A FIGURE, NOT A TERMINAL ANSWER, and that distinction is the bug this replaced. These two
	// readings used to `return` from here, which threw away every figure below: the token
	// count, the cache ratio, the error count and the SAVING. spendSummary reads all four
	// outside its own Priced guard, on the stated grounds that "suppressing them alongside the
	// money would blank the only readings a deployment with no rate table has" and that "a
	// window that priced nothing and pruned something reports 'cost unavailable' beside a real
	// saved figure, and both are true" — and the renderer returned before either could happen,
	// so four comments described behaviour the next twelve lines defeated.
	//
	// Reachable rather than exotic: any endpoint absent from the rate card sits at
	// Priced == false permanently, and usage/pricing_test.go pins a saving on an unpriced
	// request as a supported state.
	if !s.Priced && !s.HasToday {
		switch {
		case s.Failed:
			// The poll did not answer, so there are no counters to qualify anything with —
			// which is a DIFFERENT claim from "we looked and nothing was priceable", and
			// rendering the latter for a broken endpoint sends a reader after a pricing table
			// when the fix is a proxy.
			window = append(window, plainFigure("cost unavailable"), plainFigure("poll failed"))
		case s.Priceable > 0:
			// The note carries no span of its own: it is in the WINDOW group, whose label the
			// group prefix supplies, and it sits immediately after the reading it qualifies.
			// Adjacency used to be all it had — which broke precisely when a today figure came
			// between them, and the fix then was a second, hand-labelled copy of the same note
			// further down. The grouping removes the case that needed two spellings.
			window = append(window, plainFigure("cost unavailable"),
				plainFigure(coverageNote(s.Unpriced, s.Priceable)))
		default:
			// Priceable == 0 WITH a snapshot in hand is a finding, not an absence: we looked,
			// and there was no inference traffic to price. Say so. An always-on strip that
			// renders nothing has failed at its only job.
			window = append(window, plainFigure("no priceable traffic yet"))
		}
	}
	if s.HasToday {
		// Today outranks the rolling window when it exists: it is the figure an
		// operator is accountable for, and the window is context for it.
		//
		// TodayDegraded is the ledger's own damage disclosure, and this is the only figure on
		// the line that can carry one: applyTodayFigure sets it from the window=today reply,
		// which is the strip's single ledger-backed poll. Without it a day that lost lines
		// rendered a figure byte-identical to a clean one — the exact failure
		// usage.Snapshot.Degraded exists to end, on the strip's headline reading.
		today = append(today, moneyFigure(s.TodayUSD, "today",
			s.TodayUnpriced, s.TodayPriceable, s.TodayIncomplete, s.TodayDegraded, s.TodayClamped))
		// SECOND, directly after the spend it belongs to, because the pair is the reading: what
		// it cost and what it would have cost.
		//
		// THE DAY's SAVING, from the day's own poll — and it took a span twin on spendSummary to
		// make that sentence true. This slot used to hold the HOUR's saving, so "the pair" was a
		// day beside an hour: $64.1765 today against a saved figure of $1.0291 for a day that had
		// really avoided $2.1891. See spendSummary.TodaySavedUSD.
		if s.HasTodaySaved {
			today = append(today, savedFigure(s.TodaySavedUSD))
		}
	}
	// Guarded on Priced independently of the branch above, which lets !Priced
	// through whenever HasToday is set. Without this guard that combination — a
	// ledger-backed today figure over a rolling window that priced nothing —
	// rendered "$0.0000 /1h", stating a settled zero for a cost nobody knows.
	//
	// It was unreachable while nothing set HasToday, which is why it survived two
	// review rounds. It is REACHABLE now: the today poll lands on its own 5-minute
	// chain, so a fresh session can hold a priced day total beside a rolling hour that
	// has priced nothing yet. The guard is what makes that state render honestly.
	if s.Priced {
		// nil degraded, and not because nobody looked: this reading comes from the in-memory
		// ring, which has no lines to fail to decode and no files to abandon, so a duration
		// window leaves usage.Snapshot.Degraded nil and that absence is the truth. See
		// snapshotDamaged.
		//
		// The CLAMP is not nil-by-construction the same way, and passing false here would be a
		// second bug of the shape this whole exercise is about: usage.Counts.Saturated is on
		// Counts, and the ring's Add clamps exactly like the ledger's fold does, so a rolling
		// window can overflow with no ledger anywhere near it.
		// NO SPAN SUFFIX. The span is the group's now, applied once below to whichever figure
		// leads the window group — which is usually this one, and must not be assumed to be:
		// every branch here is a different occupant of the same slot.
		window = append(window, moneyFigure(s.WindowUSD, "",
			s.Unpriced, s.Priceable, s.Incomplete, nil, s.Clamped))
	} else if s.Failed && s.HasToday {
		// A FAILED WINDOW BESIDE A GOOD DAY. The day stands on its own chain, so the only thing
		// missing is the window reading, and this occupies the slot that reading would have —
		// AFTER the today figure, because today outranks the window and is the reading a reader
		// came for. It needs the window's label for the reason the coverage note below needed
		// one, and gets it from the group rather than by appending its own.
		window = append(window, plainFigure("poll failed"))
	} else if s.HasToday && s.Unpriced > 0 && s.Priceable > 0 {
		// The window figure is suppressed because nothing in the window was priced, so its
		// coverage gap has no figure to ride on. It still has to be stated — this is the
		// reachable state where a ledger-backed day sits beside a rolling hour that priced
		// nothing — and being in the window group is what stops it reading as a qualification
		// of the today figure to its left. That misreading is what the unlabelled tail note
		// used to produce, and the hand-appended "/1h" here was the narrower fix for it.
		//
		// GATED ON HasToday, which is the only state it is for. Without that gate it fired
		// alongside the note the no-money branch above already emits, and the same gap was
		// stated twice in one group.
		window = append(window, plainFigure(coverageNote(s.Unpriced, s.Priceable)))
	}
	// THE WINDOW's SAVING IS THE FALLBACK, shown only when the day has none of its own — two
	// saved figures on one line is how a reader learns to read neither.
	//
	// It lands here, after the money reading rather than before it, and that is a demotion with a
	// reason. The saving used to outrank the window total outright ("a saving is a headline number
	// for anyone who turned tool-prune on"), which it earned by being the partner of the figure
	// BEFORE it. In this group its partner is the window's own cost, so following that cost is
	// what keeps the pair adjacent — and leading the group would put the span prefix on the
	// saving, labelling the one figure on the line that is not spend.
	//
	// Reachable wherever there is no durable cost ledger, which is Kubernetes by design: see
	// applyTodayFigure, where a ring-served day leaves HasToday unset.
	if !s.HasTodaySaved && s.HasSaved {
		window = append(window, savedFigure(s.SavedUSD))
	}
	// Volume LAST IN THE GROUP, after the money: what the window cost, what it avoided, then the
	// readings a reader checks those against. (The line as a whole now reads day-spend, day-saving,
	// window-spend, window-saving, volume — the money of each span before the volume of either,
	// which is what grouping by span buys.)
	//
	// The cache ratio leads the volume figures because it is the LEADING indicator — a
	// cache read bills at roughly 0.1x uncached input, so for agent traffic the hit rate
	// moves before the dollar figure does. No marker: it is a ratio of two counters the
	// provider reported, exact as reported, and the counters' own caveats ride on the
	// money figures they qualify.
	if s.HasCacheHit {
		window = append(window, stripFigure{
			full:    fmt.Sprintf("cache %.0f%%", s.CacheHitPct),
			compact: fmt.Sprintf("%.0f%%", s.CacheHitPct),
		})
	}
	// humanizeCount, not a local formatter: it is the one this package already uses for
	// token counts and it is width-BOUNDED — its own doc records a version that rendered a
	// billion tokens as "1000.0M" and silently broke the column. A strip that measures with
	// lipgloss.Width and drops whole figures cannot afford one of unbounded width.
	//
	// IN THE WINDOW GROUP, which is the figure this whole restructuring is for: this count is the
	// ring's, and unlabelled beside a day's cost it invited exactly the comparison it cannot
	// answer — against the day, and against the lifetime sum of the sessions table below it.
	if s.Tokens > 0 {
		window = append(window, stripFigure{
			full:    humanizeCount(s.Tokens) + " tokens",
			compact: humanizeCount(s.Tokens),
		})
	}
	// Errors only when there are some, on this line's standing rule: a permanent "0 err"
	// is the figure that teaches a reader to stop looking at the strip.
	if s.Errors > 0 {
		window = append(window, stripFigure{
			full:    fmt.Sprintf("%d err", s.Errors),
			compact: fmt.Sprintf("%de", s.Errors),
		})
	}
	// The pane pointer, only when there is no money figure to explain and only after the
	// readings that ARE known. It is a place to look rather than a reading, so it outranks
	// nothing: at a narrow width a reader is better served by the token count than by advice.
	// The Usage pane is what distinguishes an old proxy from a transport error.
	//
	// IN THE TAIL rather than in the window group, because it is not a reading of anything. A
	// window group whose only member were this would render "1h: [u] usage", offering the span of
	// a figure that is not there.
	if s.Failed || (!s.Priced && !s.HasToday) {
		tail = append(tail, plainFigure("[u] usage"))
	}
	// The age rides last, so it is the first thing a narrow terminal gives up. It
	// qualifies every figure on the line rather than one of them, and unlike a partiality
	// marker it is recoverable — the next poll either answers or the age keeps growing —
	// so it is the one caveat that may be dropped outright.
	//
	// "EVERY FIGURE ON THE LINE" IS WHY IT IS IN THE TAIL. applyAges takes the older of the two
	// poll chains precisely so one age describes the whole answer, so this is the one reading that
	// belongs to neither span — and it can reach the front of the window group, in the state where
	// a priced day sits over an hour with nothing to report, where the prefix would hand a
	// both-chains figure the hour's label.
	if s.Stale {
		age := formatSpendAge(s.Age)
		tail = append(tail, stripFigure{full: "polled " + age + " ago", compact: age + " ago"})
	}
	// THE SPAN, ONCE, ON THE GROUP. Applied to the first window figure rather than to a chosen one,
	// because the slot has several possible occupants — the money reading, a "poll failed", a bare
	// coverage note — and the label belongs to whichever is actually there.
	//
	// SAFE AGAINST THE FITTER because it drops whole figures from the RIGHT: the first window
	// figure outlives every later one, so a prefix baked in here cannot be dropped while the
	// figures it covers survive. The reverse — labelling the last — would be.
	//
	// WIDTH-NEUTRAL against the suffix it replaces: "1h: $1.1200" and "$1.1200 /1h" are both 11
	// columns, so the documented width at which this function falls silent does not move.
	//
	// An EMPTY label means the server's window is unreadable or no snapshot answered (the Failed
	// path builds a summary with no WindowLabel at all), and an unlabelled group is the honest
	// answer there — "unknown: $1.12" would be worse than a bare figure.
	if len(window) > 0 && s.WindowLabel != "" {
		window[0] = labelSpan(s.WindowLabel, window[0])
	}
	figures := make([]stripFigure, 0, len(today)+len(window)+len(tail))
	figures = append(figures, today...)
	figures = append(figures, window...)
	figures = append(figures, tail...)
	return fitStripFigures(stripLabel, figures, width)
}

// savedFigure is the avoided-spend reading, in the ONE spelling both spans use.
//
// A function because there are now two call sites — the day's saving and the window's fallback —
// and the argument below is about how the figure must be spelled, not about which span it covers.
// Two copies of that argument would drift.
//
// inexactMarker unconditionally, which is the whole reason it is spelled "saved ~$0.18" and not
// "saved $0.18". The figure comes from a bytes-to-tokens ratio rather than a tokenizer and is
// GROSS of the prompt-cache re-warm, so it is never exact — see usage.Counts.AvoidedMicros, which
// spells out both caveats and says a client must present this as approximate unconditionally
// rather than inferring exactness from the absence of a per-request flag.
//
// NOT a moneyFigure, and that is the point: moneyFigure attaches coverage, exactness, damage and
// clamp caveats built from a span's counters, and none of them are about this number. A saving is
// not spend, so a caveat about how much of the SPEND was priced would be a misattribution of
// exactly the kind moneyFigure exists to prevent.
//
// NO COMPACT FORM: this figure's label is not an explanation, it is the figure's IDENTITY.
// The compact form was a bare "~$0.1804", and ~ is inexactMarker — which on a money figure means
// "this is a lower bound". An inexact SPEND figure renders "~$4.1700 today" and keeps its label,
// so a bare marked figure between two labelled ones reads as spend whose label the ladder happened
// to drop. The ladder's rule is that it gives up explanations before figures, and "saved" is not
// an explanation of $0.1804 — without it the number is a different claim, not a terser one. Equal
// forms mean the ladder drops the whole figure instead, which is the same rule it applies to a
// number it cannot render in full: if it cannot be said correctly, it is not said.
func savedFigure(usd float64) stripFigure {
	return plainFigure("saved " + inexactMarker + formatUSDCell(usd))
}

// labelSpan names the span a group of figures covers, on the figure that leads it.
//
// A PREFIX, not a suffix, and that is what makes one label cover several figures: "1h: $36.5723
// cache 99%  84.6M tokens" reads as three readings of one hour, where "$36.5723 /1h  cache 99%
// 84.6M tokens" reads as one labelled figure followed by two of unstated span — which is what the
// line did, and what let an hour's token count be compared against a day's total.
//
// ON BOTH FORMS, for the reason the partiality markers ride on both: fitStripFigures may reduce
// every figure to its compact form, and a span that survives only at full width is a span that
// disappears exactly when the line is hardest to read.
func labelSpan(label string, f stripFigure) stripFigure {
	return stripFigure{full: label + ": " + f.full, compact: label + ": " + f.compact}
}

// formatSpendAge renders an age the way a strip has room for: "3m", not "3m12.4s".
//
// Rounded DOWN to the coarsest unit that still says something, because the number's job
// is to distinguish "a poll or two behind" from "this chain stopped answering", and no
// reader needs the seconds of a five-minute-old figure to tell those apart.
func formatSpendAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}

// fitStripFigures joins as many LEADING figures as fit, dropping whole ones from
// the right. Returns "" when even the first figure cannot fit without clipping,
// because a clipped figure is a wrong figure.
//
// Two degradation steps per prefix length, in this order: every figure spelled out,
// then every figure compact. So the line gives up its EXPLANATIONS before it gives up a
// READING, and it gives up a reading before it clips anything. The compact form still
// carries each figure's marker, so no step of this can turn a partial total into one
// that looks complete — the worst outcome available on this line, and the one the old
// tail-mounted coverage note produced whenever it was the thing that got dropped.
//
// Width arithmetic is lipgloss.Width throughout, never len() and never a rune
// count. footer.go:88-92 records the bug that costs: a budget computed in display
// columns and then sliced by rune index overflowed on any wide character,
// rendering 55 columns for a 40-column budget. The package's trunc and truncStr
// helpers are both display-cell-unaware for the same reason, so neither is usable
// here.
//
// Linear rather than a binary search over n: the list is at most five figures
// long, and the loop is the specification — "the widest prefix that fits".
func fitStripFigures(label string, figures []stripFigure, width int) string {
	if len(figures) == 0 {
		return ""
	}
	join := func(n int, compact bool) string {
		parts := make([]string, 0, n)
		for _, f := range figures[:n] {
			if compact {
				parts = append(parts, f.compact)
			} else {
				parts = append(parts, f.full)
			}
		}
		return label + "  " + strings.Join(parts, stripGap)
	}
	for n := len(figures); n >= 1; n-- {
		for _, compact := range []bool{false, true} {
			if candidate := join(n, compact); lipgloss.Width(candidate) <= width {
				return candidate
			}
		}
	}
	// The label does not fit alongside even one figure. Drop the LABEL before
	// dropping the number: the figure is the information, the label is decoration,
	// and a dollar amount on its own is still unambiguous in the chrome.
	for _, only := range []string{figures[0].full, figures[0].compact} {
		if lipgloss.Width(only) <= width {
			return only
		}
	}
	return ""
}

// spendStripMinHeight is the terminal height at which the strip earns its row.
//
// Below it the row is worth more to the table than to the chrome, so the strip
// yields it entirely rather than shrink the body further. 20 rows is roughly
// where an events table stops being able to show a turn's request and its
// response together, which is the smallest useful unit of that pane.
const spendStripMinHeight = 20

// spendStripVisible reports whether the strip DRAWS a row on the current pane.
//
// False on paneNamespaces and panePods: those run before a connection exists, so
// there is no spend to report, and they return early from paneView with their own
// JoinVertical anyway.
//
// Note the asymmetry with layout(), which reserves the row on HEIGHT alone and
// ignores the pane. That is deliberate and is explained where the reservation is
// made: layout() runs on WindowSizeMsg, so a pane-aware reservation would have to
// be re-run on every pane transition. The consequence is that the two picker
// panes render one row shorter than they strictly need — invisible, next to an
// events table that is wrong by a row and pushes the footer off-screen.
func (m *model) spendStripVisible() bool {
	if !m.spendStripReservesRow() {
		return false
	}
	switch m.pane {
	case paneNamespaces, panePods:
		return false
	}
	return true
}

// spendStripReservesRow reports whether layout() must hold a row back for the
// strip. Height ONLY — deliberately blind to the pane.
//
// layout() is called from exactly one place, the WindowSizeMsg handler; no pane
// transition re-runs it. So a reservation that read m.pane would go stale the
// moment the user moved between a picker and a data pane, and bodyHeight would
// stay wrong until the next terminal resize — which for a pane the strip DOES
// draw on means a body one row too tall and a footer pushed off the bottom.
//
// Being blind to the pane costs the two picker panes one row they could have
// used. That is the accepted trade: a picker one row short is invisible, an
// events table one row long is not.
func (m *model) spendStripReservesRow() bool { return m.height >= spendStripMinHeight }
