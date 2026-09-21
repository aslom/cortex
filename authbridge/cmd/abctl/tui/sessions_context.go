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
func sessionContext(events []pipeline.SessionEvent) int {
	// Which exchanges had a tool manifest on the REQUEST side. Requests and responses have never
	// been observed to disagree — 117 pairs, no divergence — so this is belt and braces rather
	// than a second filter, and it is cheap: one pass and a set of ids.
	toolRequests := make(map[string]bool)
	for i := range events {
		e := &events[i]
		if e.Phase == pipeline.SessionRequest && e.RequestID != "" &&
			e.Inference != nil && len(e.Inference.Tools) > 0 {
			toolRequests[e.RequestID] = true
		}
	}

	best, bestMsgs := 0, -1
	for i := len(events) - 1; i >= 0; i-- {
		e := &events[i]
		if e.Inference == nil || len(e.Inference.Tools) == 0 {
			continue
		}
		// The paired request must have carried tools too — unless there is no RequestID to pair
		// on, which is an older proxy. Excluding those would blank the column for it, so the
		// response's own manifest stands in.
		if e.RequestID != "" && !toolRequests[e.RequestID] {
			continue
		}
		n := promptTokens(e.Inference)
		if n <= 0 {
			continue
		}
		// Strictly greater, walking backwards, so equal message counts keep the MOST RECENT.
		if len(e.Inference.Messages) > bestMsgs {
			best, bestMsgs = n, len(e.Inference.Messages)
		}
	}
	return best
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
