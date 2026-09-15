package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rossoctl/cortex/authbridge/authlib/pipeline"
	"github.com/rossoctl/cortex/authbridge/cmd/abctl/apiclient"
)

func TestShowPluginDetailRendersConfig(t *testing.T) {
	m := newPickerModel(context.Background(), nil, nil)
	// The viewport defaults to 0×0 (sized by layout() on WindowSizeMsg);
	// in unit tests we set it manually so View() returns content.
	m.detailVp.Width = 80
	m.detailVp.Height = 20
	plugin := &apiclient.PipelinePlugin{
		Name:      "jwt-validation",
		Direction: "inbound",
		Position:  1,
		Config:    json.RawMessage(`{"issuer":"http://idp"}`),
	}
	m.showPluginDetail(plugin, true)
	view := m.detailVp.View()
	if !strings.Contains(view, "Config:") {
		t.Fatalf("rendered view missing Config section:\n%s", view)
	}
	if !strings.Contains(view, "issuer") {
		t.Fatalf("rendered view missing config key:\n%s", view)
	}
	if !strings.Contains(view, "http://idp") {
		t.Fatalf("rendered view missing config value:\n%s", view)
	}
}

func TestShowPluginDetailRendersNoneForEmptyConfig(t *testing.T) {
	m := newPickerModel(context.Background(), nil, nil)
	m.detailVp.Width = 80
	m.detailVp.Height = 20
	plugin := &apiclient.PipelinePlugin{
		Name:      "non-configurable",
		Direction: "inbound",
		Position:  1,
		Config:    nil,
	}
	m.showPluginDetail(plugin, true)
	view := m.detailVp.View()
	if !strings.Contains(view, "Config:") {
		t.Fatalf("rendered view missing Config section:\n%s", view)
	}
	if !strings.Contains(view, "(none)") {
		t.Fatalf("rendered view should say (none) for empty Config:\n%s", view)
	}
}

// TestShowPluginDetailHandlesMalformedConfig verifies the TUI degrades
// gracefully when Config bytes are not valid JSON. The server should
// never produce malformed bytes (Configure() validates), but corruption
// in transit isn't impossible — we lock the contract that the renderer
// writes *something* without panicking.
func TestShowPluginDetailHandlesMalformedConfig(t *testing.T) {
	m := newPickerModel(context.Background(), nil, nil)
	m.detailVp.Width = 80
	m.detailVp.Height = 20
	plugin := &apiclient.PipelinePlugin{
		Name:      "broken",
		Direction: "inbound",
		Position:  1,
		Config:    json.RawMessage(`{not valid`),
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("showPluginDetail panicked on malformed JSON: %v", r)
		}
	}()
	m.showPluginDetail(plugin, true)
	view := m.detailVp.View()
	if !strings.Contains(view, "Config:") {
		t.Fatalf("rendered view missing Config section:\n%s", view)
	}
	// ColorizeJSONBytes' fallback is to render the raw bytes as a muted
	// string. We don't assert exact escape-code output (style-dependent),
	// but the literal "{not" should appear somewhere.
	if !strings.Contains(view, "{not") {
		t.Fatalf("rendered view missing raw config fallback:\n%s", view)
	}
}

func TestShowPluginDetailRendersDescription(t *testing.T) {
	m := newPickerModel(context.Background(), nil, nil)
	m.detailVp.Width = 80
	m.detailVp.Height = 30
	plugin := &apiclient.PipelinePlugin{
		Name:        "test",
		Direction:   "inbound",
		Position:    1,
		Description: "Operator-facing description",
	}
	m.showPluginDetail(plugin, true)
	view := m.detailVp.View()
	if !strings.Contains(view, "Operator-facing description") {
		t.Fatalf("Description not rendered:\n%s", view)
	}
}

// TestShowPluginDetailRendersUnmetRequires verifies the ✗ branch
// triggers when a Required upstream is absent.
func TestShowPluginDetailRendersUnmetRequires(t *testing.T) {
	m := newPickerModel(context.Background(), nil, nil)
	m.detailVp.Width = 80
	m.detailVp.Height = 30
	m.pipeline = &apiclient.PipelineView{
		Outbound: []apiclient.PipelinePlugin{
			{Name: "needy", Direction: "outbound", Position: 1, Requires: []string{"missing-dep"}},
		},
	}
	plugin := &m.pipeline.Outbound[0]
	m.showPluginDetail(plugin, true)
	view := m.detailVp.View()
	if !strings.Contains(view, "Requires:") {
		t.Fatalf("Requires section missing:\n%s", view)
	}
	if !strings.Contains(view, "missing-dep") {
		t.Fatalf("Requires should name missing-dep:\n%s", view)
	}
	if !strings.Contains(view, "NOT in this chain") {
		t.Fatalf("Requires should call out the missing dep:\n%s", view)
	}
}

// prunePlugin is a plugin whose detail pane is taller than any terminal used in
// these tests, so there is something to scroll. tool-prune is the plugin the
// report named; the long config is what makes the pane scrollable.
func prunePlugin() *apiclient.PipelinePlugin { return prunePluginWithTools(40) }

// prunePluginWithTools varies how many config lines the detail pane renders, so a
// refresh can make the content shorter than the offset a reader was parked at.
func prunePluginWithTools(n int) *apiclient.PipelinePlugin {
	cfg := map[string]any{"mode": "aggressive"}
	for i := 0; i < n; i++ {
		cfg[fmt.Sprintf("allow_tool_%02d", i)] = fmt.Sprintf("some-fairly-long-tool-name-%02d", i)
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		panic(err)
	}
	return &apiclient.PipelinePlugin{
		Name: "tool-prune", Direction: "outbound", Position: 2,
		Description: "Prunes unused tools from the request", Config: raw,
	}
}

// pluginDetailModel opens the plugin detail pane the way an operator does:
// pipeline pane, cursor on the plugin, Enter.
func pluginDetailModel(t *testing.T) *model {
	t.Helper()
	m := newPickerModel(context.Background(), nil, nil)
	m.detailVp.Width, m.detailVp.Height = 80, 10
	m.width, m.height = 80, 13
	m.pipeline = &apiclient.PipelineView{Outbound: []apiclient.PipelinePlugin{*prunePlugin()}}
	m.pipelineTbl = newPipelineTable()
	m.rebuildPipelineTable()
	m.pane = panePipeline
	// Row 0 is the inbound/outbound divider when the inbound chain is empty, and
	// Enter on the divider selects no plugin at all — park on the plugin's row.
	rows := m.pipelineTbl.Rows()
	plugRow := -1
	for i := range rows {
		if !isDividerRow(rows, i) {
			plugRow = i
			break
		}
	}
	if plugRow < 0 {
		t.Fatal("fixture built no plugin row")
	}
	setCursorVisible(&m.pipelineTbl, plugRow)

	if cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter}); cmd == nil {
		t.Fatal("Enter on a pipeline plugin should fetch the pipeline")
	}
	if m.pane != panePluginDetail {
		t.Fatalf("Enter left pane = %v, want panePluginDetail", m.pane)
	}
	if m.detailVp.YOffset != 0 {
		t.Fatalf("a freshly opened plugin detail pane should start at the top, YOffset = %d", m.detailVp.YOffset)
	}
	return m
}

// refreshPipeline delivers the reply to a loadPipelineCmd, which is what both the
// Enter-time fetch and every two-second refresh tick produce.
func refreshPipeline(t *testing.T, m *model, p *apiclient.PipelinePlugin) {
	t.Helper()
	if p == nil {
		p = prunePlugin()
	}
	view := apiclient.PipelineView{Outbound: []apiclient.PipelinePlugin{*p}}
	next, _ := m.Update(pipelineLoadedMsg(&view))
	mm, ok := next.(*model)
	if !ok {
		t.Fatalf("Update returned %T, want *model", next)
	}
	*m = *mm
}

// TestPluginDetail_RefreshKeepsScrollPosition is the reported bug: pipeline →
// tool-prune → Enter, scroll down, and about a second later the pane is back at the
// top.
//
// The second is not a coincidence. Entering the pane fires an immediate
// loadPipelineCmd (so the counters are current rather than a tick stale), and its
// reply re-renders the pane through showPluginDetail — which ended with an
// unconditional detailVp.GotoTop(). The two-second refresh tick then repeats it for
// as long as the pane is open, so the pane cannot be read past its first screenful.
//
// A refresh of the plugin already on screen must leave the reader where they are.
func TestPluginDetail_RefreshKeepsScrollPosition(t *testing.T) {
	m := pluginDetailModel(t)

	for i := 0; i < 4; i++ {
		m.detailVp.ScrollDown(1)
	}
	want := m.detailVp.YOffset
	if want == 0 {
		t.Fatal("fixture is not scrollable: YOffset still 0 after scrolling down")
	}

	// Two refreshes: the Enter-time fetch, then a tick.
	for refresh := 1; refresh <= 2; refresh++ {
		refreshPipeline(t, m, nil)
		if got := m.detailVp.YOffset; got != want {
			t.Errorf("refresh %d scrolled the pane to YOffset %d, want %d", refresh, got, want)
		}
	}
}

// The other half of the contract, driven through the keys rather than the flag:
// leaving the pane and coming back must start at the top. Otherwise a fix for the
// refresh would have turned into "the pane remembers a scroll position forever",
// which is just as wrong and much harder to notice.
func TestPluginDetail_ReopeningStartsAtTop(t *testing.T) {
	m := pluginDetailModel(t)
	for i := 0; i < 4; i++ {
		m.detailVp.ScrollDown(1)
	}
	if m.detailVp.YOffset == 0 {
		t.Fatal("fixture is not scrollable")
	}

	m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.pane != panePipeline {
		t.Fatalf("esc left pane = %v, want panePipeline", m.pane)
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.pane != panePluginDetail {
		t.Fatalf("Enter left pane = %v, want panePluginDetail", m.pane)
	}
	if got := m.detailVp.YOffset; got != 0 {
		t.Errorf("re-opened the pane at YOffset %d, want 0", got)
	}
}

// fatInferenceEvent is an event whose detail-pane JSON is longer than any viewport
// these tests build. The plain cursorRowsFixture events render a handful of lines, so
// a scroll assertion against them is vacuous — GotoBottom on content that already
// fits leaves the offset at 0 and every "did it move?" check passes for free.
func fatInferenceEvent() pipeline.SessionEvent {
	e := cursorRowsFixture(1)[0]
	for i := 0; i < 20; i++ {
		e.Inference.Messages = append(e.Inference.Messages, pipeline.InferenceMessage{
			Role:    "user",
			Content: fmt.Sprintf("message %02d: %s", i, strings.Repeat("some prompt text ", 4)),
		})
	}
	return e
}

// A refresh whose content SHRANK is the case viewport.SetContent does not fully
// handle: it pulls the offset back only when the offset is past the last LINE, while
// the offset that can actually be rendered stops a screenful earlier. So a plugin
// that loses a few config or Metrics lines under a reader parked at the bottom left
// the pane rendering its tail high with dead space below it.
func TestPluginDetail_ShrinkingRefreshDoesNotStrandTheOffset(t *testing.T) {
	m := pluginDetailModel(t)
	m.detailVp.GotoBottom()
	if m.detailVp.YOffset == 0 {
		t.Fatal("fixture is not scrollable")
	}

	// A handful of lines shorter — not so much shorter that SetContent's own clamp
	// catches it, which is exactly the gap.
	refreshPipeline(t, m, prunePluginWithTools(35))
	if m.detailVp.PastBottom() {
		t.Errorf("a shrinking refresh left the viewport past the bottom: YOffset %d, height %d",
			m.detailVp.YOffset, m.detailVp.Height)
	}
}

// The events detail pane takes the same flag, and its false case is reached by a
// terminal resize or by opening the filter — both of which route through layout(),
// which re-renders the pane to re-wrap the JSON for the new width. Neither is a
// reason to move the reader.
func TestEventDetail_ReRenderKeepsScrollPosition(t *testing.T) {
	m := fitModel(t, paneEvents, 100, 30, []pipeline.SessionEvent{fatInferenceEvent()})
	m.rebuildEventsTable()
	er, ok := m.selectedEventRow()
	if !ok {
		t.Fatal("fixture has no selectable event row")
	}
	m.showDetail(er, true)
	m.pane = paneDetail
	for i := 0; i < 5; i++ {
		m.detailVp.ScrollDown(1)
	}
	want := m.detailVp.YOffset
	if want == 0 {
		t.Fatal("fixture is not scrollable: YOffset still 0 after scrolling down")
	}

	// Opening the filter re-lays-out the body, which re-renders this pane.
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if got := m.detailVp.YOffset; got != want {
		t.Errorf("opening the filter scrolled the pane to %d, want %d", got, want)
	}

	// A resize re-wraps the content; the reader stays put (clamped if the re-wrap
	// made the content shorter, never reset to the top).
	m.width, m.height = 70, 24
	m.layout()
	if got := m.detailVp.YOffset; got == 0 && want != 0 {
		t.Errorf("a resize reset the pane to the top, want it near %d", want)
	}
}
