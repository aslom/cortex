package sessionapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rossoctl/cortex/authbridge/authlib/pipeline"
)

// The claim writeSessionView makes: the same bytes json.Encoder produced.
//
// Worth a table rather than one case, because the difference between the two encoders is
// entirely in the envelope — the field order, the omitempty on totalEvents, nil vs empty
// events, the trailing newline — and every one of those is a place a hand-rolled writer
// can be plausibly wrong while looking right on a happy-path session.
func TestWriteSessionView_MatchesTheBufferedEncoding(t *testing.T) {
	at := time.Date(2026, 9, 16, 10, 30, 0, 0, time.UTC)

	cases := []struct {
		name string
		view *pipeline.SessionView
	}{
		{"nil events encode as null, not []", &pipeline.SessionView{ID: "s1"}},
		{"empty events", &pipeline.SessionView{ID: "s1", Events: []pipeline.SessionEvent{}}},
		{"one event", &pipeline.SessionView{ID: "s1", Events: []pipeline.SessionEvent{
			{At: at, Direction: pipeline.Inbound, Phase: pipeline.SessionRequest},
		}}},
		{"several events", &pipeline.SessionView{ID: "s1", Events: []pipeline.SessionEvent{
			{At: at, Phase: pipeline.SessionRequest},
			{At: at.Add(time.Second), Phase: pipeline.SessionResponse, StatusCode: 200},
			{At: at.Add(2 * time.Second), Phase: pipeline.SessionDenied, StatusCode: 401},
		}}},
		{"totalEvents present when the view is a tail", &pipeline.SessionView{
			ID: "s1", TotalEvents: 5071,
			Events: []pipeline.SessionEvent{{At: at}},
		}},
		{"an id needing escapes", &pipeline.SessionView{
			ID:     `a/b "c" <d>&e` + "\né世",
			Events: []pipeline.SessionEvent{{At: at}},
		}},
		{"content needing HTML escapes", &pipeline.SessionView{ID: "s1", Events: []pipeline.SessionEvent{{
			At: at,
			Inference: &pipeline.InferenceExtension{
				Model:      "m",
				Messages:   []pipeline.InferenceMessage{{Role: "user", Content: `<script>a && b</script>`}},
				Completion: "   \x00 ünïcødé 世界",
			},
		}}}},
		{"every extension populated", &pipeline.SessionView{ID: "s1", TotalEvents: 3, Events: []pipeline.SessionEvent{{
			At:         at,
			Direction:  pipeline.Outbound,
			Phase:      pipeline.SessionResponse,
			Host:       "api.example",
			HTTPMethod: "POST",
			HTTPPath:   "/v1/messages",
			StatusCode: 200,
			Duration:   12 * time.Millisecond,
			Identity:   &pipeline.EventIdentity{Subject: "alice", ClientID: "agent", Scopes: []string{"openid"}},
			Error:      &pipeline.EventError{Kind: "upstream", Code: "503", Message: "unavailable"},
			A2A:        &pipeline.A2AExtension{Method: "message/send", Parts: []pipeline.A2APart{{Content: "hi"}}},
			Inference: &pipeline.InferenceExtension{
				Model:     "claude",
				Messages:  []pipeline.InferenceMessage{{Role: "user", Content: "q"}},
				Tools:     []pipeline.InferenceTool{{Name: "t", Description: "d", Parameters: map[string]any{"type": "object"}}},
				ToolCalls: []pipeline.InferenceToolCall{{ID: "1", Name: "t", Arguments: `{"a":1}`}},
			},
			Invocations: &pipeline.Invocations{Outbound: []pipeline.Invocation{{
				Plugin: "token-exchange", Action: pipeline.ActionModify, Reason: "exchanged",
				Details: map[string]string{"target_audience": "aud"},
			}}},
			Plugins: map[string]json.RawMessage{"cost": json.RawMessage(`{"usd":0.0123}`)},
		}}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var want bytes.Buffer
			if err := json.NewEncoder(&want).Encode(tc.view); err != nil {
				t.Fatal(err)
			}
			var got bytes.Buffer
			if err := writeSessionView(&got, tc.view); err != nil {
				t.Fatal(err)
			}
			if got.String() != want.String() {
				t.Errorf("streamed output differs from json.Encoder\n got: %s\nwant: %s", got.String(), want.String())
			}
			// And it is still valid JSON that round-trips, so a mismatch the string
			// compare somehow tolerated cannot pass here either.
			var back pipeline.SessionView
			if err := json.Unmarshal(got.Bytes(), &back); err != nil {
				t.Fatalf("output does not parse: %v", err)
			}
			if back.ID != tc.view.ID || len(back.Events) != len(tc.view.Events) || back.TotalEvents != tc.view.TotalEvents {
				t.Errorf("round-trip lost fields: id=%q events=%d total=%d",
					back.ID, len(back.Events), back.TotalEvents)
			}
		})
	}
}

// The point of the change: the document reaches the writer in per-event pieces, so the
// peak buffer is one event and not the response.
//
// Asserted on write sizes rather than on heap, because that is deterministic. bufio hands
// through any write larger than its buffer, so the largest write this makes is one event's
// encoding — where json.Encoder.Encode writes the entire document in a single call, which
// is the allocation that put 244MB on the heap for a 105MB response. The buffered
// encoder's own largest write is measured here rather than assumed, so this fails if
// writeSessionView ever starts accumulating instead of streaming.
func TestWriteSessionView_NeverBuffersTheWholeDocument(t *testing.T) {
	const events = 40
	view := &pipeline.SessionView{ID: "s1"}
	for i := 0; i < events; i++ {
		view.Events = append(view.Events, pipeline.SessionEvent{
			At: time.Now(),
			Inference: &pipeline.InferenceExtension{
				Model: "m",
				Messages: []pipeline.InferenceMessage{{
					Role:    "user",
					Content: fmt.Sprintf("event %02d: %s", i, strings.Repeat("prompt text ", 6000)),
				}},
			},
		})
	}

	oneEvent, err := json.Marshal(&view.Events[0])
	if err != nil {
		t.Fatal(err)
	}

	var streamed, buffered maxWriter
	if err := writeSessionView(&streamed, view); err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(&buffered).Encode(view); err != nil {
		t.Fatal(err)
	}

	if streamed.total != buffered.total {
		t.Fatalf("streamed %d bytes, buffered %d — the two must emit the same document",
			streamed.total, buffered.total)
	}
	t.Logf("document %d bytes; largest single write: streamed %d, buffered %d",
		streamed.total, streamed.max, buffered.max)

	// The bound: one event plus whatever the buffer had already accumulated.
	if limit := len(oneEvent) + snapshotWriteBuffer; streamed.max > limit {
		t.Errorf("largest streamed write is %d bytes, over the one-event bound of %d — "+
			"the document is being accumulated, not streamed", streamed.max, limit)
	}
	// And the contrast is real, not an artifact of a document too small to tell apart.
	if buffered.max < streamed.total {
		t.Errorf("buffered encoder's largest write is %d of %d bytes; this test needs a "+
			"document the buffered path emits in one write to be measuring anything",
			buffered.max, streamed.total)
	}
}

// maxWriter records how much the writer was ever handed at once.
type maxWriter struct {
	max   int
	total int
}

func (m *maxWriter) Write(p []byte) (int, error) {
	if len(p) > m.max {
		m.max = len(p)
	}
	m.total += len(p)
	return len(p), nil
}

// A write error must surface, not be swallowed by the buffer.
func TestWriteSessionView_ReportsWriteErrors(t *testing.T) {
	view := &pipeline.SessionView{ID: "s1", Events: []pipeline.SessionEvent{
		{Inference: &pipeline.InferenceExtension{Messages: []pipeline.InferenceMessage{
			{Role: "user", Content: strings.Repeat("x", 128<<10)},
		}}},
	}}
	if err := writeSessionView(failWriter{}, view); err == nil {
		t.Error("a failing writer produced no error")
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, fmt.Errorf("connection reset") }
