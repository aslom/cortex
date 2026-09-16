package session

import "github.com/rossoctl/cortex/authbridge/authlib/pipeline"

// Repeated message content is stored once per session, not once per event.
//
// WHY: an LLM request carries the whole conversation, so every turn re-sends every
// earlier message. The store keeps one event per request, each holding its own copy of
// that array, which makes retention quadratic in turns while the conversation itself
// grows linearly. Measured on a real 96-turn session: 27.7MB of message text held, 7.0MB
// distinct — 3.9x, and the factor rises with turn count because the duplicated prefix
// gets longer. The proxy reached 3.37GB resident on a laptop in 18 hours, against
// ~833MB for every Claude Code transcript on the same disk. The transcripts are the same
// conversations, appended once each; the difference was all duplication.
//
// The fix costs nothing at read time and changes nothing observable: Go strings are
// immutable, so two events sharing one backing array cannot tell, and a consumer sees
// the same bytes it always did.
//
// SCOPE: string fields only — inference messages and completions, A2A part content.
// MCP Params/Result are map[string]any, which needs a recursive walk of arbitrary JSON;
// deferred until measurement says it matters, rather than guessed at now.
const (
	// internMinLen is the shortest string worth a map lookup.
	//
	// A role ("user"), a finish reason ("end_turn") and an empty completion all repeat
	// constantly and all cost less to duplicate than to hash. The savings live in
	// message bodies, which are orders of magnitude past this.
	internMinLen = 64
)

// interner maps a string to the one copy the session keeps of it.
//
// It holds only the strings of the LAST event interned, which is enough to collapse the
// whole history and is the reason it needs no eviction policy of its own. Turn N's array
// is turn N-1's array plus a message or two, so interning N against N-1 makes them share;
// N-1 already shares with N-2, and so on back to the first turn that carried the string.
// One rolling event's worth of keys, rather than a table that grows with the session and
// would then have to be pruned in step with event eviction and body shedding.
//
// The tradeoff is deliberate: content that disappears from the conversation and comes
// back later (a compaction that rewrites history, say) misses the table and gets a second
// copy. Best-effort dedup with a bounded table beats exact dedup with a table that
// outlives what it describes.
type interner struct {
	prev map[string]string
}

// intern returns the session's copy of s, recording s as canonical if it is new.
func (in *interner) intern(s string, next map[string]string) string {
	if len(s) < internMinLen {
		return s
	}
	if canon, ok := in.prev[s]; ok {
		next[s] = canon
		return canon
	}
	next[s] = s
	return s
}

// internEvent rewrites the event's large string fields to reference the session's
// existing copies, then rolls the table forward to this event's strings.
//
// It mutates through the event's extension POINTERS, which the caller may still hold —
// safe because every replacement is a string equal to the one it replaces. There is no
// observable change to make; that is the whole point.
func (in *interner) internEvent(e *pipeline.SessionEvent) {
	next := make(map[string]string, len(in.prev))

	if e.Inference != nil {
		for i := range e.Inference.Messages {
			e.Inference.Messages[i].Content = in.intern(e.Inference.Messages[i].Content, next)
		}
		e.Inference.Completion = in.intern(e.Inference.Completion, next)
	}
	if e.A2A != nil {
		for i := range e.A2A.Parts {
			e.A2A.Parts[i].Content = in.intern(e.A2A.Parts[i].Content, next)
		}
	}

	in.prev = next
}
