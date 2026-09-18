package sessionapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rossoctl/cortex/authbridge/authlib/pipeline"
)

// fullEvent is an event carrying every content-bearing field the projection is
// meant to drop, alongside every field the timeline renders.
func fullEvent() pipeline.SessionEvent {
	temp := 0.7
	return pipeline.SessionEvent{
		SessionID:  "s1",
		Seq:        42,
		At:         time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC),
		Direction:  pipeline.Outbound,
		Phase:      pipeline.SessionResponse,
		RequestID:  "req-1",
		Host:       "api.example.com",
		StatusCode: 200,
		Duration:   1200 * time.Millisecond,
		Identity:   &pipeline.EventIdentity{},
		Invocations: &pipeline.Invocations{
			Outbound: []pipeline.Invocation{{Plugin: "token-exchange", Action: "modify", Reason: "exchanged"}},
		},
		Plugins: map[string]json.RawMessage{
			"cost": json.RawMessage(`{"cost_usd":0.1}`),
		},
		Inference: &pipeline.InferenceExtension{
			Model:       "opus",
			Temperature: &temp,
			Stream:      true,
			Messages: []pipeline.InferenceMessage{
				{Role: "user", Content: strings.Repeat("x", 4096)},
			},
			Tools:            []pipeline.InferenceTool{{Name: "search"}},
			Completion:       strings.Repeat("y", 2048),
			ToolCalls:        []pipeline.InferenceToolCall{{Name: "search"}},
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
			InputTokens:      90,
			CacheReadTokens:  10,
			OutputTokens:     50,
			PresentKinds:     9,
			FinishReason:     "stop",
		},
		A2A: &pipeline.A2AExtension{
			Method:       "message/send",
			Role:         "user",
			IsAction:     true,
			Parts:        []pipeline.A2APart{{Kind: "text", Content: strings.Repeat("z", 1024)}},
			Artifact:     strings.Repeat("a", 512),
			FinalStatus:  "completed",
			ErrorMessage: "none",
		},
		MCP: &pipeline.MCPExtension{
			Method:   "tools/call",
			IsAction: true,
			Params:   map[string]any{"big": strings.Repeat("p", 1024)},
			Result:   map[string]any{"big": strings.Repeat("r", 1024)},
			Err:      &pipeline.MCPError{Code: -1, Message: "boom"},
		},
	}
}

// The projection has one job: drop the payloads the timeline never renders while
// keeping every field it does. Asserted field by field, because "it got smaller"
// would also be true of a projection that dropped something the table needs.
func TestSummarizeEvent_DropsPayloadsKeepsTimelineFields(t *testing.T) {
	full := fullEvent()
	got := summarizeEvent(&full)

	// Dropped: the content-bearing fields, which live only in the detail pane.
	if got.Inference.Messages != nil {
		t.Error("Inference.Messages survived")
	}
	if got.Inference.Tools != nil {
		t.Error("Inference.Tools survived")
	}
	if got.Inference.Completion != "" {
		t.Error("Inference.Completion survived")
	}
	if got.Inference.ToolCalls != nil {
		t.Error("Inference.ToolCalls survived")
	}
	if got.A2A.Parts != nil {
		t.Error("A2A.Parts survived")
	}
	if got.A2A.Artifact != "" {
		t.Error("A2A.Artifact survived")
	}
	if got.MCP.Params != nil {
		t.Error("MCP.Params survived")
	}
	if got.MCP.Result != nil {
		t.Error("MCP.Result survived")
	}
	if got.Plugins != nil {
		t.Error("Plugins survived — unbounded per-plugin JSON, rendered only in the detail pane")
	}

	// Kept: everything the events table and its sort keys read.
	if got.Seq != full.Seq || got.At != full.At || got.Host != full.Host {
		t.Error("identity/time/host fields did not survive")
	}
	if got.Direction != full.Direction || got.Phase != full.Phase {
		t.Error("direction/phase did not survive")
	}
	if got.StatusCode != 200 || got.Duration != 1200*time.Millisecond {
		t.Error("status/duration did not survive — STATUS and DURATION columns")
	}
	// Token counts drive the TOKENS and COST columns, and cost is derived from the
	// cache-read/write split rather than the total — so each one is checked.
	for _, tc := range []struct {
		name      string
		got, want int
	}{
		{"promptTokens", got.Inference.PromptTokens, full.Inference.PromptTokens},
		{"completionTokens", got.Inference.CompletionTokens, full.Inference.CompletionTokens},
		{"totalTokens", got.Inference.TotalTokens, full.Inference.TotalTokens},
		{"inputTokens", got.Inference.InputTokens, full.Inference.InputTokens},
		{"cacheReadTokens", got.Inference.CacheReadTokens, full.Inference.CacheReadTokens},
		{"outputTokens", got.Inference.OutputTokens, full.Inference.OutputTokens},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d — TOKENS/COST columns read it", tc.name, tc.got, tc.want)
		}
	}
	if got.Inference.PresentKinds != full.Inference.PresentKinds {
		t.Error("presentKinds did not survive — it says which counts are real")
	}
	if got.Inference.Model != "opus" || got.Inference.FinishReason != "stop" {
		t.Error("model/finishReason did not survive")
	}
	// Invocations is timeline data, not detail data: abctl renders one row per
	// invocation, and the ACTION column comes from it.
	if got.Invocations == nil || len(got.Invocations.Outbound) != 1 {
		t.Fatal("Invocations did not survive — the timeline renders a row per invocation")
	}
	if got.Invocations.Outbound[0].Plugin != "token-exchange" {
		t.Error("invocation contents did not survive")
	}
	// Protocol identity and the action/bypass classification drive what the row
	// says it is; errors are the thing you scan a timeline for.
	if got.A2A.Method != "message/send" || !got.A2A.IsAction || got.A2A.FinalStatus != "completed" {
		t.Error("A2A timeline fields did not survive")
	}
	if got.A2A.ErrorMessage != "none" {
		t.Error("A2A.ErrorMessage did not survive")
	}
	if got.MCP.Method != "tools/call" || !got.MCP.IsAction {
		t.Error("MCP timeline fields did not survive")
	}
	if got.MCP.Err == nil || got.MCP.Err.Message != "boom" {
		t.Error("MCP error did not survive")
	}
}

// The store hands out pointers to events it keeps. A projection that cleared
// fields in place would delete the operator's conversation from the store —
// permanently, and only for sessions someone happened to open.
func TestSummarizeEvent_DoesNotMutateTheStoredEvent(t *testing.T) {
	full := fullEvent()
	_ = summarizeEvent(&full)

	if len(full.Inference.Messages) != 1 {
		t.Error("Inference.Messages was cleared on the source event")
	}
	if full.Inference.Completion == "" {
		t.Error("Inference.Completion was cleared on the source event")
	}
	if len(full.A2A.Parts) != 1 || full.A2A.Artifact == "" {
		t.Error("A2A payload was cleared on the source event")
	}
	if full.MCP.Params == nil || full.MCP.Result == nil {
		t.Error("MCP payload was cleared on the source event")
	}
	if full.Plugins == nil {
		t.Error("Plugins was cleared on the source event")
	}
}

// A nil extension must stay nil rather than become an empty object, or every row
// would claim to carry a protocol it does not.
func TestSummarizeEvent_LeavesAbsentExtensionsAbsent(t *testing.T) {
	bare := pipeline.SessionEvent{Seq: 1, Host: "h"}
	got := summarizeEvent(&bare)
	if got.Inference != nil || got.A2A != nil || got.MCP != nil {
		t.Error("an absent extension was materialised by the projection")
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, absent := range []string{`"inference"`, `"a2a"`, `"mcp"`} {
		if strings.Contains(string(b), absent) {
			t.Errorf("%s appears in the encoding of an event that had none: %s", absent, b)
		}
	}
}

// The size claim this whole change rests on. Measured at 203x on real traffic;
// asserted well under that so the test pins the order of magnitude rather than
// one machine's sample.
func TestSummarizeEvent_IsOrdersOfMagnitudeSmaller(t *testing.T) {
	full := fullEvent()
	fb, err := json.Marshal(&full)
	if err != nil {
		t.Fatalf("marshal full: %v", err)
	}
	sb, err := json.Marshal(summarizeEvent(&full))
	if err != nil {
		t.Fatalf("marshal summary: %v", err)
	}
	ratio := float64(len(fb)) / float64(len(sb))
	t.Logf("full=%d summary=%d ratio=%.1fx", len(fb), len(sb), ratio)
	if ratio < 10 {
		t.Errorf("summary is only %.1fx smaller (full=%d summary=%d); the payloads are the point",
			ratio, len(fb), len(sb))
	}
}
