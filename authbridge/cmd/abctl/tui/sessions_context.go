package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/rossoctl/cortex/authbridge/authlib/pipeline"
)

// sessionContext is how full the session's context is, in prompt tokens: the CONVERSATION's
// latest request, ignoring the one-shot calls interleaved with it.
//
// Zero when nothing can be said, which the gauge renders as an em dash rather than as an empty
// track — "not known" and "barely used" are different answers and the column has to keep them
// apart.
//
// THE LATEST REQUEST WAS THE WRONG ANSWER, and measuring said so. One session's own events, in
// order, prompt tokens per response:
//
//	msgs     1491     3     3  1494     3     3  1497     2  1500  1503  1506     2  1509
//	context  830k  282k  282k  835k  282k  282k  837k    6k  840k  844k  848k    7k  851k
//
// Claude Code interleaves one-shot completions — title generation, quota and summary calls — with
// the conversation, under the SAME X-Claude-Code-Session-Id, so a session is not one thread. The
// gauge followed whichever spoke last and swung 83% to 0.7% between adjacent turns.
//
// THE DISCRIMINATOR IS THE TOOL MANIFEST, not the size. Across 115 responses in three live
// sessions the split was total, with no overlap:
//
//	carries tools    73 responses   63-1572 messages   94k-897k context
//	no tools         42 responses     2-3   messages  6.5k-295k context
//
// A request with no tools is a one-shot completion, not an agentic conversation. Note the second
// row's ceiling: one of those calls carried 295k, so "the biggest context wins" would have picked
// it — the tool manifest is what separates the two populations, and the token count is not.
//
// AMONG CANDIDATES, THE MOST MESSAGES WINS, and there is deliberately no window. The main thread
// goes SILENT while a subagent runs — that is structural, not evidence it is gone — so any
// last-N-responses window can fill with subagent traffic and hand the column to it. Unbounded,
// a subagent's conversation can never out-message a long main thread however long it runs.
//
// WHAT THAT COSTS: a compaction restarts the conversation at a low message count while the
// pre-compaction request stays retained with 1500 of them, so the gauge keeps showing the old
// context — for the rest of the session, if that request is never evicted. Chosen knowingly: a
// stale figure beats one that flips to a subagent's. The exact fix is a conversation id from the
// proxy, which would make both the filter and this tie-break unnecessary; a window would not fix
// it and would reintroduce the silence problem.
//
// THE PROMPT SIDE ONLY. promptTokens is input + cache-read + cache-write; output is left out.
// Measured on the same sessions it is 0.003%-2.2% of the prompt, and 0.2% on the conversations
// near the limit — a fifth of one eighth-block at 1M, so counting it would change no pixel.
//
// Read off the RESPONSE because the provider is the only party that tokenizes — the same reason
// tokensCell looks forward from a request row to its pair.
//
// WHY THIS COMES FROM abctl'S OWN CACHE and not from the session summary: the summary carries no
// per-request field, so there is nothing to read. abctl subscribes to /v1/events unfiltered and
// appends every event under its session id, so any session with traffic since it attached has a
// conversation here.
//
// AND ONLY THE STREAM CAN ESTABLISH IT — the timeline cannot, which is not what an earlier version
// of this comment claimed. abctl asks for `view=summary` on every timeline fetch, tail and page
// alike (apiclient/snapshot.go), and sessionapi.summarizeEvent drops exactly the two fields this
// rule reads: Inference.Messages and Inference.Tools. Measured on one live session, the same
// 200-event window both ways:
//
//	unprojected      62 inference responses   41 carry a manifest   up to 1755 msgs, 997k context
//	view=summary     62 inference responses    0 carry a manifest    no msgs, no manifest
//
// The stream is unprojected (handleStream marshals the whole event), so live traffic answers this
// question and a timeline fetch never can. A session idle since before abctl attached therefore
// shows the dash until either a turn arrives on the stream or the operator opens one of its
// events — the detail pane's fetch is the only path that puts a full event back into the slice.
//
// WHICH IS WHY A SNAPSHOT MUST NOT ERASE WHAT THE STREAM ESTABLISHED. Dropping the running answer
// on a wholesale replacement — the obvious invalidation, and what this did first — blanked the
// column to a dash the moment an operator opened a session, because the projected events that
// replaced the streamed ones carry no candidate at all. contextRun rebases instead: see
// rebaseSessionContext. The figure deliberately outlives the events it was read from.
// A whole-slice fold, for callers with no running state to keep — the tests, and any future
// one-shot reader. The sessions pane goes through sessionContextFor instead.
func sessionContext(events []pipeline.SessionEvent) int {
	return foldSessionContext(events, contextRun{}).tokens
}

// contextRun is the running answer over the events folded so far: the winning figure and the
// message count that won it, plus how many events have been folded in.
//
// A RUNNING answer rather than a rescan, because the caller's shape demands it. The sessions row
// loop asks for every session, rebuildSessionsTable runs on every streamed event, and retention
// is unbounded — so a full scan per call is O(events) per session per event on the DEFAULT pane.
// Measured on this branch before the fold: 6.1ms and 3.49MB per call at 100k events, against
// ~14ns and no allocation for the tail scan it replaced. Ten sessions of that size is 60ms and
// 35MB for one arriving event.
//
// A REMEMBERED MAXIMUM, not a cache of a pure function over m.events — and the difference is
// load-bearing, not a convenience. The events abctl holds for a session can stop carrying the
// evidence the figure was read from: a `view=summary` timeline fetch replaces streamed events with
// projected copies that carry no manifest and no message count. A cache would be invalidated by
// that and come back empty; a remembered maximum survives it. What it costs is stated with the
// compaction trade-off above — a figure this holds is the largest conversation abctl has SEEN for
// the session, which after a server-side eviction may be larger than anything it still holds.
type contextRun struct {
	n      int // events folded so far
	tokens int
	msgs   int
	// at is WHEN the winning response arrived, and it exists because of the rebase. Ties go to
	// the latest, which a single forward fold expresses as arrival order — but a rebase folds
	// new events on top of a winner that is no longer in the slice, so arrival order says
	// nothing about which of the two came first. Without this, opening an OLDER turn of equal
	// length replaced a newer remembered figure (reproduced at 445k against a true 500k).
	at time.Time
}

// foldSessionContext folds events into run and returns the new running answer.
//
// Forward, and ties keep the LATEST — by TIMESTAMP, not by arrival order. A plain `>=` expressed
// that correctly while folding was the only way events entered the run, since arrival order and
// time order agreed. Rebasing broke the equivalence: the remembered winner is not in the slice
// being folded, so "later in this fold" no longer means "later in the session", and an older turn
// of equal length would take the tie. Comparing At keeps the rule the tests name.
//
// `!Before` rather than `After`, so two candidates sharing a timestamp still resolve by arrival
// order the way the pure fold did.
//
// Returns only the new run. An earlier version also reported whether anything was folded, "so a
// caller can tell no-candidates-yet from zero" — a distinction no caller made: all three sites
// discarded it and sessionContext collapses both cases to 0 regardless.
func foldSessionContext(events []pipeline.SessionEvent, run contextRun) contextRun {
	for i := range events {
		e := &events[i]
		// RESPONSES ONLY, stated rather than relied on. The token counts arrive on the response
		// pass, so a request snapshot carries zeroes and would be dropped by the promptTokens
		// check below anyway — but that is an accident of when SnapshotInference copies, not
		// something this loop said. Checking the phase makes the doc above load-bearing and
		// halves the candidates.
		if e.Phase != pipeline.SessionResponse || e.Inference == nil {
			continue
		}
		// The tool manifest is the filter: no tools means a one-shot completion.
		//
		// The REQUEST side is not checked, and the reason is stronger than the 117 paired
		// samples that showed no divergence. SnapshotInference is `c := *ext`, a shallow copy,
		// and Tools is only ever appended while parsing the REQUEST — so both snapshots carry
		// the same slice header off the same extension and cannot disagree by construction. A
		// paired check would be dead code, and the map it needed is what made this function
		// allocate per call.
		if len(e.Inference.Tools) == 0 {
			continue
		}
		n := promptTokens(e.Inference)
		if n <= 0 {
			continue
		}
		if msgs := len(e.Inference.Messages); msgs > run.msgs ||
			(msgs == run.msgs && !e.At.Before(run.at)) {
			run.tokens, run.msgs, run.at = n, msgs, e.At
		}
	}
	run.n += len(events)
	return run
}

// sessionContextFor is the gauge's figure for one session, folded rather than rescanned.
//
// Appending is the only growth path that preserves the prefix, so a longer slice folds just its
// tail. Every path that does something else to m.events owes this function an action — see the
// inventory on model.events — because a replacement of the SAME length is invisible to the length
// check below.
func (m *model) sessionContextFor(id string) int {
	events := m.events[id]
	run, ok := m.contextRun[id]
	switch {
	case ok && run.n == len(events):
		return run.tokens
	case ok && run.n < len(events):
		run = foldSessionContext(events[run.n:], run)
		m.contextRun[id] = run
	default:
		// FEWER EVENTS THAN THE RUN FOLDED, or no run at all: the prefix cannot be trusted,
		// so the slice is re-folded — but from the figure already established, not from zero.
		// The picker's release drops a live session's events to reclaim memory
		// (keys.go), which is a decision about storage and not new information about the
		// session, so zeroing here would turn the gauge into a dash for a session still
		// sending traffic.
		m.rebaseSessionContext(id, events)
		return m.contextRun[id].tokens
	}
	return run.tokens
}

// rebaseSessionContext re-folds a session whose events were REPLACED rather than appended to,
// keeping the figure it had already established.
//
// Called wherever the length check cannot interpret what happened: the snapshot load (a projected
// copy of the same window, often the same length), the older-page merge (older events land before
// the folded ones, and the page cap can drop newer ones off the end), the detail pane's write-back
// (one event swapped in place for its full self, length unchanged), and sessionContextFor's own
// fallback for a slice shorter than the run.
//
// KEEPS tokens, msgs AND at, and re-folds the whole new slice on top of them. Keeping the figure is
// what makes the column survive a `view=summary` snapshot — see sessionContext on why the timeline
// cannot answer this. Re-folding rather than just re-basing n is what lets the new slice WIN:
// nothing here assumes the replacement is poorer, so a detail fetch that puts a longer
// conversation back in place beats the remembered one on message count exactly as a streamed turn
// would.
//
// Costs one whole-slice fold per replacement, which is a keystroke rather than an arriving event:
// 0.51ms at 100k events, no allocation.
//
// THERE IS NO forgetSessionContext, and its absence is deliberate. One existed and dropped the
// entry on every path that did not append — that is what blanked the column after a projected
// snapshot. The only thing that genuinely voids a figure is a DIFFERENT WORKLOAD: a pod or
// endpoint switch, where the same session id means someone else's conversation. backToPodsPane
// handles that by nilling the whole map beside m.events.
func (m *model) rebaseSessionContext(id string, events []pipeline.SessionEvent) {
	prev := m.contextRun[id]
	run := foldSessionContext(events,
		contextRun{tokens: prev.tokens, msgs: prev.msgs, at: prev.at})
	if m.contextRun == nil {
		m.contextRun = map[string]contextRun{}
	}
	m.contextRun[id] = run
}

// contextGauge draws prompt tokens against contextWindowTokens, in exactly width columns.
//
// A BRACKETED TRACK, because the brackets are what make the scale visible on every row. The
// three designs considered were this, a dotted remainder ("███▎····") and a single eighth-block
// cell; the brackets earn their two columns twice over. A session at 0.8% draws "▕▏       ▏",
// which reads as nearly-empty-out-of-something, where a bare "▏" floating in whitespace reads as
// a rendering fault — and the em dash for an unknown context stays unmistakably different from
// both, which it cannot be when an almost-empty bar is itself almost nothing.
//
// No ░ for the remainder. usage_glyphs.go rejected shaded blocks after measuring them: "█
// against ▓ is nearly indistinguishable at a glance in most terminal fonts", and a track a
// reader cannot separate from the fill is worse than no track.
//
// Fill arithmetic is tierBar's, reused rather than restated, which brings its rule with it: a
// non-zero value never renders as an empty bar. That rule is the whole reason a 8,200-token
// context still shows a sliver.
//
// Exactly width columns, so every row's gauge starts and ends in the same place and the fills can
// be compared down the column by eye. That is also what makes the column's alignment moot — see
// sessionsRightAligned, which lists it for the em dash's sake.
func contextGauge(promptTokens, width int) string {
	// Three is brackets plus one cell of track. Below that there is nothing to draw, and a
	// bracket pair alone would claim a scale it cannot show.
	if width < 3 {
		return ""
	}
	if promptTokens <= 0 {
		return emptyCell
	}
	budget := width - 2
	bar := tierBar(int64(promptTokens), contextWindowTokens, budget)
	return "▕" + bar + strings.Repeat(" ", budget-lipgloss.Width(bar)) + "▏"
}

// cachedMarker names a row whose events abctl holds and the server no longer lists (#870).
//
// A word rather than a glyph: it has to survive rendering under a colour profile, and bubbles
// truncates each cell with runewidth.Truncate BEFORE styling — see
// TestSessionsPicker_CachedMarkerRendersIntact, which exists because a styled cell had its
// escape bytes measured against the column width and came out mangled.
const cachedMarker = "cached"
