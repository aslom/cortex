package session

import (
	"log/slog"
	"net/http"
)

// ClaudeCodeSessionHeader is the request header the Claude Code CLI sets on
// every inference request, carrying a UUID that is stable for the life of one
// session and reused when a session is resumed (`claude --resume <id>`). It is
// the same id the CLI names its transcript after
// (~/.claude/projects/<slug>/<uuid>.jsonl), so a bucket keyed on it lines up
// with what the user sees in their own client.
//
// Verified against claude-cli/2.1.266. The value is also duplicated inside the
// request body as metadata.user_id, which is why losing the header is not
// silently fatal — see IDFromHeaders for why we read the header only.
const ClaudeCodeSessionHeader = "X-Claude-Code-Session-Id"

// IDFromHeaders returns the first usable session id found in h among names,
// in order, or "" when none is present. Callers treat "" as "fall back to
// whatever bucketing you did before" — never as an error, so a client that
// stops sending the header degrades to the old shared bucket instead of
// dropping telemetry.
//
// The header is read rather than the equivalent body field (Claude Code
// duplicates the id in metadata.user_id) because a header costs nothing to
// reach: bucketing must work for every request, including ones no parser
// matched and ones whose body was never buffered.
func IDFromHeaders(h http.Header, names []string) string {
	for _, name := range names {
		id := h.Get(name)
		if id == "" {
			continue
		}
		if !validHeaderSessionID(id) {
			// Debug, not Warn: on a shared proxy any client can send anything,
			// and a rejected id already degrades visibly to the old bucket.
			// Logging the value itself is what the check exists to prevent, so
			// report only its shape.
			slog.Debug("session: ignoring unusable session id header",
				"header", name, "len", len(id))
			continue
		}
		return id
	}
	return ""
}

// validHeaderSessionID reports whether a client-supplied id is safe to use as
// a bucket key.
//
// Two rules, each for a concrete reason:
//
//   - No control characters. The id is rendered in abctl's TUI, interpolated
//     into structured log lines, and returned in /v1/sessions JSON. A newline
//     forges a log line and an ESC sequence rewrites the operator's terminal.
//     Printable bytes above ASCII are left alone so a non-Anthropic client with
//     its own id scheme still works.
//   - No longer than MaxSessionIDLen. Store.Append truncates past that, and two
//     overlong ids sharing a prefix would then silently merge into one bucket —
//     precisely the confusion per-session bucketing exists to remove. Refusing
//     falls back to the old shared bucket, which is wrong in a way the operator
//     can see, rather than wrong in a way they cannot.
func validHeaderSessionID(id string) bool {
	if len(id) > MaxSessionIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] < 0x20 || id[i] == 0x7f {
			return false
		}
	}
	return true
}
