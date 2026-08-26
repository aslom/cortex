package inferenceparser

import (
	"context"
	"testing"

	"github.com/rossoctl/cortex/authbridge/authlib/pipeline"
)

func TestInferenceParser_AnthropicMessages_Request(t *testing.T) {
	p := NewInferenceParser()
	pctx := &pipeline.Context{
		Path: "/v1/messages",
		Body: []byte(`{
			"model": "claude-opus-4-8",
			"system": "You are a helpful assistant.",
			"messages": [
				{"role": "user", "content": "What is the weather in NYC?"}
			],
			"max_tokens": 1024,
			"temperature": 0.7,
			"stream": false,
			"tools": [
				{"name": "get_weather", "description": "Get weather", "input_schema": {"type": "object"}}
			]
		}`),
	}

	if action := p.OnRequest(context.Background(), pctx); action.Type != pipeline.Continue {
		t.Fatalf("expected Continue, got %v", action.Type)
	}
	ext := pctx.Extensions.Inference
	if ext == nil {
		t.Fatal("Extensions.Inference is nil — /v1/messages not parsed")
	}
	if ext.Model != "claude-opus-4-8" {
		t.Errorf("Model = %q, want claude-opus-4-8", ext.Model)
	}
	if !ext.IsAction {
		t.Error("IsAction should be true for an inference request")
	}
	// system (top-level) is surfaced as a leading system message, then the user turn.
	if len(ext.Messages) != 2 || ext.Messages[0].Role != "system" || ext.Messages[1].Role != "user" {
		t.Fatalf("Messages = %+v, want [system, user]", ext.Messages)
	}
	if ext.Messages[0].Content != "You are a helpful assistant." {
		t.Errorf("system content = %q", ext.Messages[0].Content)
	}
	if ext.MaxTokens == nil || *ext.MaxTokens != 1024 {
		t.Errorf("MaxTokens = %v, want 1024", ext.MaxTokens)
	}
	if len(ext.Tools) != 1 || ext.Tools[0].Name != "get_weather" {
		t.Fatalf("Tools = %+v, want [get_weather]", ext.Tools)
	}
}

func TestInferenceParser_AnthropicMessages_ContentBlockArray(t *testing.T) {
	// Anthropic content can be a block array; flatten text blocks like OpenAI.
	p := NewInferenceParser()
	pctx := &pipeline.Context{
		Path: "/v1/messages",
		Body: []byte(`{
			"model": "claude-opus-4-8",
			"max_tokens": 64,
			"messages": [
				{"role": "user", "content": [
					{"type": "text", "text": "part one"},
					{"type": "text", "text": "part two"}
				]}
			]
		}`),
	}
	p.OnRequest(context.Background(), pctx)
	ext := pctx.Extensions.Inference
	if ext == nil || len(ext.Messages) != 1 {
		t.Fatalf("ext/messages = %+v", ext)
	}
	if ext.Messages[0].Content != "part one\npart two" {
		t.Errorf("flattened content = %q, want \"part one\\npart two\"", ext.Messages[0].Content)
	}
}

func TestInferenceParser_AnthropicMessages_NonStreamingResponse(t *testing.T) {
	p := NewInferenceParser()
	pctx := &pipeline.Context{Path: "/v1/messages"}
	// non-streaming: ext.Stream == false → one-shot last=true frame is parsed as JSON.
	pctx.Extensions.Inference = &pipeline.InferenceExtension{Model: "claude-opus-4-8", IsAction: true}

	body := []byte(`{
		"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-opus-4-8",
		"content": [
			{"type": "text", "text": "It is sunny."},
			{"type": "tool_use", "id": "toolu_1", "name": "get_weather", "input": {"city": "NYC"}}
		],
		"stop_reason": "tool_use",
		"usage": {"input_tokens": 25, "output_tokens": 8, "cache_read_input_tokens": 2}
	}`)
	p.OnResponseFrame(context.Background(), pctx, body, true)

	ext := pctx.Extensions.Inference
	if ext.Completion != "It is sunny." {
		t.Errorf("Completion = %q", ext.Completion)
	}
	if ext.FinishReason != "tool_use" {
		t.Errorf("FinishReason = %q, want tool_use", ext.FinishReason)
	}
	if len(ext.ToolCalls) != 1 || ext.ToolCalls[0].Name != "get_weather" {
		t.Fatalf("ToolCalls = %+v", ext.ToolCalls)
	}
	// PromptTokens = input_tokens + cache_read (25 + 2); CompletionTokens = output_tokens.
	if ext.PromptTokens != 27 || ext.CompletionTokens != 8 || ext.TotalTokens != 35 {
		t.Errorf("tokens = prompt %d / completion %d / total %d, want 27/8/35",
			ext.PromptTokens, ext.CompletionTokens, ext.TotalTokens)
	}
}

func TestInferenceParser_AnthropicMessages_StreamFoldsEvents(t *testing.T) {
	p := NewInferenceParser()
	pctx := &pipeline.Context{Path: "/v1/messages"}
	pctx.Extensions.Inference = &pipeline.InferenceExtension{Model: "claude-opus-4-8", Stream: true, IsAction: true}

	frames := [][]byte{
		[]byte(`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","usage":{"input_tokens":25,"output_tokens":1}}}`),
		[]byte(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		[]byte(`{"type":"ping"}`),
		[]byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`),
		[]byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}`),
		[]byte(`{"type":"content_block_stop","index":0}`),
		[]byte(`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":15}}`),
		[]byte(`{"type":"message_stop"}`),
	}
	for _, f := range frames {
		if action := p.OnResponseFrame(context.Background(), pctx, f, false); action.Type != pipeline.Continue {
			t.Fatalf("frame action = %v, want Continue", action.Type)
		}
	}
	// Mid-stream: not finalized yet.
	if pctx.Extensions.Inference.Completion != "" {
		t.Errorf("Completion populated mid-stream = %q", pctx.Extensions.Inference.Completion)
	}
	// Finalize.
	p.OnResponseFrame(context.Background(), pctx, nil, true)

	ext := pctx.Extensions.Inference
	if ext.Completion != "Hello world" {
		t.Errorf("Completion = %q, want \"Hello world\"", ext.Completion)
	}
	if ext.FinishReason != "end_turn" {
		t.Errorf("FinishReason = %q, want end_turn", ext.FinishReason)
	}
	// input_tokens from message_start; cumulative output_tokens from message_delta.
	if ext.PromptTokens != 25 || ext.CompletionTokens != 15 || ext.TotalTokens != 40 {
		t.Errorf("tokens = prompt %d / completion %d / total %d, want 25/15/40",
			ext.PromptTokens, ext.CompletionTokens, ext.TotalTokens)
	}
}

// TestInferenceParser_AnthropicMessages_StreamBetaPathUsage covers the ?beta=true
// Messages path, where the prompt-cache counts arrive in message_delta instead of
// message_start. The frames below are the usage blocks captured verbatim from a
// real Claude Code turn (anthropic-beta: claude-code-20250219) against an
// Anthropic-compatible gateway: message_start carried only input_tokens, and the
// 33,763 cached tokens appeared two events later. Reading the prompt size from
// message_start alone recorded that turn as 9 tokens instead of 33,772.
func TestInferenceParser_AnthropicMessages_StreamBetaPathUsage(t *testing.T) {
	p := NewInferenceParser()
	// Query-free Path: the HTTP listeners (forwardproxy, reverseproxy) populate
	// Context.Path from r.URL.Path, so /v1/messages?beta=true arrives here as
	// /v1/messages. extproc does NOT — it uses the :path pseudo-header, query
	// included; TestInferenceParser_AnthropicMessages_QueryStringPath covers that.
	pctx := &pipeline.Context{Path: "/v1/messages"}
	pctx.Extensions.Inference = &pipeline.InferenceExtension{Model: "claude-haiku-4-5", Stream: true, IsAction: true}

	frames := [][]byte{
		[]byte(`{"type":"message_start","message":{"id":"msg_bdrk_1","type":"message","role":"assistant","usage":{"input_tokens":9,"output_tokens":0}}}`),
		[]byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Done"}}`),
		[]byte(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":9,"output_tokens":399,"cache_creation_input_tokens":3755,"cache_read_input_tokens":30008}}`),
		[]byte(`{"type":"message_stop"}`),
	}
	for _, f := range frames {
		p.OnResponseFrame(context.Background(), pctx, f, false)
	}
	p.OnResponseFrame(context.Background(), pctx, nil, true)

	ext := pctx.Extensions.Inference
	// 9 + 3755 + 30008 — the cached context is still billed input.
	if ext.PromptTokens != 33772 {
		t.Errorf("PromptTokens = %d, want 33772 (message_delta usage ignored?)", ext.PromptTokens)
	}
	if ext.CompletionTokens != 399 || ext.TotalTokens != 34171 {
		t.Errorf("tokens = completion %d / total %d, want 399/34171",
			ext.CompletionTokens, ext.TotalTokens)
	}
	if ext.FinishReason != "end_turn" {
		t.Errorf("FinishReason = %q, want end_turn", ext.FinishReason)
	}
}

// TestInferenceParser_AnthropicMessages_QueryStringPath pins dialect dispatch
// when Context.Path carries a query string. extproc populates Path from the
// HTTP/2 :path pseudo-header, which includes the query, so Claude Code's
// POST /v1/messages?beta=true arrives here as "/v1/messages?beta=true" — while
// the HTTP listeners strip it via r.URL.Path.
//
// Two distinct failure modes are covered, both previously silent:
//
//   - OnRequest's exact-match switch fell to default, leaving
//     Extensions.Inference nil so the whole exchange went unrecorded;
//   - had dispatch matched but the four dialect-selection sites not been
//     normalised, an Anthropic stream would have been folded by the OpenAI
//     handler, which does not understand message_delta and would report zero
//     tokens rather than fail.
//
// Asserting the token counts therefore checks the routing, not just the match.
func TestInferenceParser_AnthropicMessages_QueryStringPath(t *testing.T) {
	p := NewInferenceParser()
	pctx := &pipeline.Context{
		Path: "/v1/messages?beta=true",
		Body: []byte(`{"model":"claude-haiku-4-5","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}

	if action := p.OnRequest(context.Background(), pctx); action.Type != pipeline.Continue {
		t.Fatalf("expected Continue, got %v", action.Type)
	}
	ext := pctx.Extensions.Inference
	if ext == nil {
		t.Fatal("Extensions.Inference is nil — query string defeated the dispatch switch")
	}
	if ext.Model != "claude-haiku-4-5" {
		t.Errorf("Model = %q, want claude-haiku-4-5", ext.Model)
	}

	frames := [][]byte{
		[]byte(`{"type":"message_start","message":{"id":"msg_q1","type":"message","role":"assistant","usage":{"input_tokens":11,"output_tokens":0}}}`),
		[]byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`),
		[]byte(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":11,"output_tokens":7,"cache_read_input_tokens":500}}`),
	}
	for _, f := range frames {
		p.OnResponseFrame(context.Background(), pctx, f, false)
	}
	p.OnResponseFrame(context.Background(), pctx, nil, true)

	// 11 + 500 cached input; the OpenAI folder would leave these at 0.
	if ext.PromptTokens != 511 || ext.CompletionTokens != 7 || ext.TotalTokens != 518 {
		t.Errorf("tokens = prompt %d / completion %d / total %d, want 511/7/518 (wrong dialect?)",
			ext.PromptTokens, ext.CompletionTokens, ext.TotalTokens)
	}
	if ext.Completion != "ok" {
		t.Errorf("Completion = %q, want \"ok\"", ext.Completion)
	}
	if ext.FinishReason != "end_turn" {
		t.Errorf("FinishReason = %q, want end_turn", ext.FinishReason)
	}
}
