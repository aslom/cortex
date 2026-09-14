package session

import (
	"net/http"
	"strings"
	"testing"
)

// TestIDFromHeaders_FirstConfiguredHeaderWins pins the ordering contract of
// session.id_headers: the list is a precedence order, not a set. An operator
// running two different agents through one proxy relies on this to say which
// client's notion of a session wins when a request somehow carries both.
func TestIDFromHeaders_FirstConfiguredHeaderWins(t *testing.T) {
	h := http.Header{
		ClaudeCodeSessionHeader: []string{"claude-session"},
		"X-Other-Agent-Session": []string{"other-session"},
	}

	if got := IDFromHeaders(h, []string{ClaudeCodeSessionHeader, "X-Other-Agent-Session"}); got != "claude-session" {
		t.Errorf("IDFromHeaders() = %q, want %q", got, "claude-session")
	}
	// Reversing the configured order reverses the winner.
	if got := IDFromHeaders(h, []string{"X-Other-Agent-Session", ClaudeCodeSessionHeader}); got != "other-session" {
		t.Errorf("IDFromHeaders() reversed = %q, want %q", got, "other-session")
	}
	// A configured header that is absent is skipped, not fatal.
	if got := IDFromHeaders(h, []string{"X-Absent", ClaudeCodeSessionHeader}); got != "claude-session" {
		t.Errorf("IDFromHeaders() with absent first = %q, want %q", got, "claude-session")
	}
}

// TestIDFromHeaders_InvalidWinnerFallsThroughToNextHeader pins the interaction
// between validation and precedence — the one branch where the two rules meet.
// A present-but-unusable value in the higher-precedence header must not veto the
// runner-up: it is not a claim about which session this is, so attribution falls
// through rather than being abandoned. The alternative reading (a bad winner
// hard-stops the lookup) is deliberately NOT the behavior; see IDFromHeaders.
func TestIDFromHeaders_InvalidWinnerFallsThroughToNextHeader(t *testing.T) {
	names := []string{ClaudeCodeSessionHeader, "X-Other-Agent-Session"}

	t.Run("control characters in the winner", func(t *testing.T) {
		h := http.Header{
			ClaudeCodeSessionHeader: []string{"poisoned\nvalue"},
			"X-Other-Agent-Session": []string{"good-session"},
		}
		if got := IDFromHeaders(h, names); got != "good-session" {
			t.Errorf("IDFromHeaders() = %q, want %q", got, "good-session")
		}
	})

	t.Run("over-length winner", func(t *testing.T) {
		h := http.Header{
			ClaudeCodeSessionHeader: []string{strings.Repeat("a", MaxSessionIDLen+1)},
			"X-Other-Agent-Session": []string{"good-session"},
		}
		if got := IDFromHeaders(h, names); got != "good-session" {
			t.Errorf("IDFromHeaders() = %q, want %q", got, "good-session")
		}
	})

	t.Run("both unusable yields empty so the caller falls back", func(t *testing.T) {
		h := http.Header{
			ClaudeCodeSessionHeader: []string{"bad\nwinner"},
			"X-Other-Agent-Session": []string{"bad\rrunner-up"},
		}
		if got := IDFromHeaders(h, names); got != "" {
			t.Errorf("IDFromHeaders() = %q, want \"\"", got)
		}
	})
}

// TestIDFromHeaders_NoUsableIDReturnsEmpty covers every way the lookup comes up
// empty. All of them must return "" so the caller falls back to its previous
// bucketing rather than inventing a bucket.
func TestIDFromHeaders_NoUsableIDReturnsEmpty(t *testing.T) {
	cases := []struct {
		name    string
		headers http.Header
		names   []string
	}{
		{"no headers configured", http.Header{ClaudeCodeSessionHeader: []string{"x"}}, nil},
		{"configured header absent", http.Header{}, []string{ClaudeCodeSessionHeader}},
		{"nil header map", nil, []string{ClaudeCodeSessionHeader}},
		{"empty value", http.Header{ClaudeCodeSessionHeader: []string{""}}, []string{ClaudeCodeSessionHeader}},
		{"only an unusable value", http.Header{ClaudeCodeSessionHeader: []string{"bad\nvalue"}}, []string{ClaudeCodeSessionHeader}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IDFromHeaders(tc.headers, tc.names); got != "" {
				t.Errorf("IDFromHeaders() = %q, want \"\"", got)
			}
		})
	}
}

// TestIDFromHeaders_AcceptsIDAtMaxLength is the boundary companion to the
// over-length rejection: exactly MaxSessionIDLen is stored intact by
// Store.Append, so it must be accepted. An off-by-one here would silently push
// legitimate ids into the shared bucket.
func TestIDFromHeaders_AcceptsIDAtMaxLength(t *testing.T) {
	atLimit := strings.Repeat("a", MaxSessionIDLen)
	h := http.Header{ClaudeCodeSessionHeader: []string{atLimit}}
	if got := IDFromHeaders(h, []string{ClaudeCodeSessionHeader}); got != atLimit {
		t.Errorf("IDFromHeaders() rejected an id of exactly MaxSessionIDLen (%d)", MaxSessionIDLen)
	}
}
