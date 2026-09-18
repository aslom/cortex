package sessionapi

import "github.com/rossoctl/cortex/authbridge/authlib/pipeline"

// summarizeEvent returns a copy of e with the content-bearing payloads removed,
// keeping every field the events timeline renders.
//
// WHY THIS EXISTS: an inference request carries the whole conversation, so a
// single event measures ~209KB and 99.5% of that is the `inference` field alone.
// The timeline shows time, direction, host, method, status, duration, tokens and
// cost — none of which needs a message body. Measured on real traffic, dropping
// the payloads is a 203x reduction: a 1000-event session goes from 199 MiB to
// 0.98 MiB, which is the difference between waiting seconds to open a session
// and fetching the whole thing.
//
// WHAT STAYS, and why each one is not obvious:
//   - Invocations. This is timeline data, not detail data — abctl renders one row
//     PER INVOCATION and the ACTION column comes from it. Dropping it would empty
//     the timeline it is meant to speed up.
//   - Token counts, all of them separately. Cost is derived from the
//     cache-read/write split rather than the total, so a partial set silently
//     changes the COST column.
//   - Error fields (MCP.Err, A2A.ErrorMessage, Error). Small, and a failure is
//     the thing an operator scans a timeline for.
//   - IsAction on each parser extension. It is what says whether a row is a
//     user-meaningful action or protocol mechanics.
//
// WHAT GOES: Inference.Messages / Tools / Completion / ToolCalls, A2A.Parts /
// Artifact, MCP.Params / Result, and Plugins. All are rendered only in the detail
// pane, which fetches the full event for the row under the cursor. Plugins is in
// that list for a different reason than size (it measured 0.3%): it is an
// escape hatch any plugin can put anything into, so it is unbounded, and the
// timeline never reads it.
//
// COPIES, NEVER CLEARS. The store hands out pointers to the events it keeps, so
// clearing fields in place would delete the operator's conversation from the
// store — permanently, and only for the sessions somebody happened to open. One
// shallow copy of the event plus one per present extension; the payload slices
// and maps are dropped by reference, not walked.
func summarizeEvent(e *pipeline.SessionEvent) *pipeline.SessionEvent {
	s := *e

	// An unbounded per-plugin JSON blob the timeline never reads.
	s.Plugins = nil

	if e.Inference != nil {
		inf := *e.Inference
		inf.Messages = nil
		inf.Tools = nil
		inf.Completion = ""
		inf.ToolCalls = nil
		s.Inference = &inf
	}
	if e.A2A != nil {
		a := *e.A2A
		a.Parts = nil
		a.Artifact = ""
		s.A2A = &a
	}
	if e.MCP != nil {
		m := *e.MCP
		m.Params = nil
		m.Result = nil
		s.MCP = &m
	}
	return &s
}
