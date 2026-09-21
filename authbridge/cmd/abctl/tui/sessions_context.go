package tui

import (
	"strings"

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
// conversation here. One idle since before that shows the dash until an operator drills in and
// the snapshot fills the cache.
// A whole-slice fold, for callers with no running state to keep — the tests, and any future
// one-shot reader. The sessions pane goes through sessionContextFor instead.
func sessionContext(events []pipeline.SessionEvent) int {
	run, _ := foldSessionContext(events, contextRun{})
	return run.tokens
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
type contextRun struct {
	n      int // events folded so far
	tokens int
	msgs   int
}

// foldSessionContext folds events into run and returns the new running answer.
//
// Forward, and ties keep the LATEST: `>=` rather than `>`, because folding walks in arrival order
// where the previous backward scan walked in reverse. The two must agree — a tie between two turns
// of the same length should report the more recent — and the direction is what decides which
// comparison expresses that.
//
// Reports whether anything was folded so a caller can tell "no candidates yet" from "zero".
func foldSessionContext(events []pipeline.SessionEvent, run contextRun) (contextRun, bool) {
	changed := false
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
		if msgs := len(e.Inference.Messages); msgs >= run.msgs {
			run.tokens, run.msgs, changed = n, msgs, true
		}
	}
	run.n += len(events)
	return run, changed
}

// sessionContextFor is the gauge's figure for one session, folded rather than rescanned.
//
// Appending is the only growth path that preserves the prefix, so a longer slice folds just its
// tail. Both wholesale replacements — the snapshot load in app.go and the older-page merge in
// paging.go — drop the entry, so a replacement of the SAME length cannot return a stale figure
// through the length check below.
func (m *model) sessionContextFor(id string) int {
	events := m.events[id]
	run, ok := m.contextRun[id]
	switch {
	case ok && run.n == len(events):
		return run.tokens
	case ok && run.n < len(events):
		run, _ = foldSessionContext(events[run.n:], run)
	default:
		run, _ = foldSessionContext(events, contextRun{})
	}
	if m.contextRun == nil {
		m.contextRun = map[string]contextRun{}
	}
	m.contextRun[id] = run
	return run.tokens
}

// forgetSessionContext drops a session's running answer, for the paths that replace its events
// rather than appending to them.
func (m *model) forgetSessionContext(id string) {
	delete(m.contextRun, id)
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
