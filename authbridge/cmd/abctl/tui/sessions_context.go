package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/rossoctl/cortex/authbridge/authlib/pipeline"
)

// sessionContext is how full the session's context was on its LATEST request, in prompt tokens.
//
// Zero when nothing can be said, which the gauge renders as an em dash rather than as an empty
// track — "not known" and "barely used" are different answers and the column has to keep them
// apart.
//
// THE LATEST REQUEST, not the largest and not the sum. A conversation's context is whatever it
// currently carries, so the figure a reader needs is the most recent one; TOKENS beside it
// already answers "how much has this session spent in total", and the two are different
// questions about the same session.
//
// Walked backwards, and the prompt side is read off the RESPONSE because the provider is the
// only party that tokenizes — the same reason tokensCell looks forward from a request row to its
// pair. promptTokens is input + cache-read + cache-write, which is the whole of what was sent.
//
// WHY THIS COMES FROM abctl'S OWN CACHE and not from the session summary: the summary carries no
// per-request field, so there is nothing to read. abctl subscribes to /v1/events unfiltered and
// appends every event under its session id, so any session with traffic since it attached has a
// latest request here. One idle since before that shows the dash until an operator drills in and
// the snapshot fills the cache. Publishing the figure server-side would populate every row on the
// first frame and is the better answer; it is a change to authlib/session and the session API,
// and is left out of this one deliberately.
func sessionContext(events []pipeline.SessionEvent) int {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Inference == nil {
			continue
		}
		if n := promptTokens(events[i].Inference); n > 0 {
			return n
		}
	}
	return 0
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
