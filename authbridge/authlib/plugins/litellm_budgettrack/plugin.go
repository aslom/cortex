// Package litellm_budgettrack provides a pipeline plugin that tracks
// per-request cost and enforces a daily spending budget, rejecting requests
// with HTTP 429 when the budget is exceeded.
//
// Cost is resolved in two ways:
//
//   - Non-streaming responses carry the cost in a response header
//     (x-litellm-response-cost, or the pre-discount -original variant), read
//     on the terminal frame.
//   - Streaming responses (text/event-stream — what Claude Code's
//     /v1/messages uses) report cost 0 in the header because the total is not
//     known when the headers are sent. For these, the plugin parses the token
//     usage out of the terminal SSE events (Anthropic message_delta /
//     message_stop, or OpenAI's final chunk usage) and prices it from the
//     configured per-token rates. Streaming cost tracking is therefore active
//     only when input_cost_per_token / output_cost_per_token are configured.
package litellm_budgettrack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/rossoctl/cortex/authbridge/authlib/pipeline"
	"github.com/rossoctl/cortex/authbridge/authlib/plugins"
)

// Response cost headers emitted by LiteLLM.
//
// responseCostHeader is the effective (post-discount) cost and is present on
// OpenAI-style /v1/chat/completions responses. Newer LiteLLM releases — and the
// Anthropic /v1/messages endpoint that Claude Code uses — do not emit it, only
// the pre-discount "-original" variant, so we fall back to that when the bare
// header is absent. Without the fallback, budget tracking silently records $0
// for Anthropic-format traffic.
const (
	responseCostHeader         = "X-Litellm-Response-Cost"
	responseCostOriginalHeader = "X-Litellm-Response-Cost-Original"
)

type budgetTrackConfig struct {
	SpendFile string  `json:"spend_file" required:"true" description:"Path to the JSON spend ledger file."`
	MaxBudget float64 `json:"max_budget" required:"true" description:"Daily budget in USD."`
	// InputCostPerToken / OutputCostPerToken price streamed responses whose
	// header cost is 0 (the total is unknown when streaming headers are sent).
	// USD per token; optional. When both are zero, streamed responses cannot be
	// priced and contribute 0 to the ledger.
	InputCostPerToken  float64 `json:"input_cost_per_token" description:"USD per input/prompt token, for pricing streamed responses."`
	OutputCostPerToken float64 `json:"output_cost_per_token" description:"USD per output/completion token, for pricing streamed responses."`
}

// stateKey names the per-request scratch holding token usage accumulated across
// streaming frames until the terminal frame prices it.
const stateKey = "litellm-budget-track"

// usageState accumulates the largest token counts seen across a stream's
// frames. Anthropic reports input_tokens in message_start and the cumulative
// output_tokens in the final message_delta, so taking the max of each yields
// the final totals; OpenAI reports both together in its terminal usage chunk.
type usageState struct {
	inputTokens  int
	outputTokens int
}

type spendLedger struct {
	Date       string  `json:"date"`
	TotalSpend float64 `json:"total_spend"`
	TotalCalls int     `json:"total_calls"`
}

// BudgetTrack enforces a daily spending budget based on x-litellm-response-cost.
type BudgetTrack struct {
	cfg    budgetTrackConfig
	mu     sync.Mutex
	ledger spendLedger
}

// New creates an unconfigured BudgetTrack plugin instance.
func New() *BudgetTrack { return &BudgetTrack{} }

func init() {
	plugins.RegisterPlugin("litellm-budget-track", func() pipeline.Plugin { return New() })
}

func (p *BudgetTrack) Name() string { return "litellm-budget-track" }

func (p *BudgetTrack) Capabilities() pipeline.PluginCapabilities {
	return pipeline.PluginCapabilities{
		Description: "Track LLM cost (response header or streamed usage) and enforce a daily budget.",
	}
}

func (p *BudgetTrack) Configure(raw json.RawMessage) error {
	if err := json.Unmarshal(raw, &p.cfg); err != nil {
		return fmt.Errorf("litellm-budget-track config: %w", err)
	}
	if p.cfg.SpendFile == "" {
		return fmt.Errorf("litellm-budget-track: spend_file is required")
	}
	if p.cfg.MaxBudget <= 0 {
		return fmt.Errorf("litellm-budget-track: max_budget must be > 0")
	}
	p.loadLedger()
	return nil
}

// OnRequest checks if the daily budget has been exceeded before allowing the request.
func (p *BudgetTrack) OnRequest(_ context.Context, pctx *pipeline.Context) pipeline.Action {
	p.mu.Lock()
	p.resetIfNewDay()
	spend := p.ledger.TotalSpend
	p.mu.Unlock()

	if spend >= p.cfg.MaxBudget {
		return pipeline.DenyStatus(429, "budget.exceeded",
			fmt.Sprintf("Cortex ExceededTokenBudget: daily spend $%.4f exceeds budget $%.2f. Reset at midnight UTC.", spend, p.cfg.MaxBudget))
	}
	return pipeline.Action{Type: pipeline.Continue}
}

// OnResponse handles the buffered path on listeners that do not route through
// OnResponseFrame. On the proxy listeners this plugin is a StreamingResponder,
// so pipeline.RunResponse skips it and OnResponseFrame drives accumulation
// instead; this remains for listeners that only call OnResponse.
func (p *BudgetTrack) OnResponse(_ context.Context, pctx *pipeline.Context) pipeline.Action {
	if cost := headerCost(pctx); cost > 0 {
		p.accumulate(cost)
	}
	return pipeline.Action{Type: pipeline.Continue}
}

// OnResponseFrame observes each response frame. It parses token usage out of
// streamed SSE frames and, on the terminal frame, prices the request: the
// response-header cost when present (non-streaming), otherwise the parsed
// usage times the configured per-token rates (streaming).
func (p *BudgetTrack) OnResponseFrame(_ context.Context, pctx *pipeline.Context, frame []byte, last bool) pipeline.Action {
	if in, out, ok := parseFrameUsage(frame); ok {
		st := pipeline.GetState[usageState](pctx, stateKey)
		if st == nil {
			st = &usageState{}
			pipeline.SetState(pctx, stateKey, st)
		}
		if in > st.inputTokens {
			st.inputTokens = in
		}
		if out > st.outputTokens {
			st.outputTokens = out
		}
	}
	if !last {
		return pipeline.Action{Type: pipeline.Continue}
	}

	// Terminal frame: settle the cost exactly once.
	cost := headerCost(pctx)
	if cost <= 0 {
		if st := pipeline.GetState[usageState](pctx, stateKey); st != nil {
			cost = float64(st.inputTokens)*p.cfg.InputCostPerToken +
				float64(st.outputTokens)*p.cfg.OutputCostPerToken
		}
	}
	if cost > 0 {
		p.accumulate(cost)
	}
	return pipeline.Action{Type: pipeline.Continue}
}

// accumulate adds one priced call to today's ledger and persists it.
func (p *BudgetTrack) accumulate(cost float64) {
	p.mu.Lock()
	p.resetIfNewDay()
	p.ledger.TotalSpend += cost
	p.ledger.TotalCalls++
	p.saveLedger()
	p.mu.Unlock()
}

// headerCost returns the cost reported in the response headers, or 0 when
// absent/zero/unparseable. Streamed responses report 0 here.
func headerCost(pctx *pipeline.Context) float64 {
	costStr := pctx.ResponseHeaders.Get(responseCostHeader)
	if costStr == "" {
		// Anthropic /v1/messages (and newer LiteLLM) omit the bare header.
		costStr = pctx.ResponseHeaders.Get(responseCostOriginalHeader)
	}
	if costStr == "" {
		return 0
	}
	cost, err := strconv.ParseFloat(costStr, 64)
	if err != nil || cost <= 0 {
		return 0
	}
	return cost
}

// parseFrameUsage extracts token usage from a response frame, covering
// Anthropic (usage, or message.usage in message_start) and OpenAI
// (usage.prompt_tokens / completion_tokens). Returns the largest input/output
// token counts found.
//
// The listener's sseframe reader strips the "data:" prefix and returns the
// bare payload, so a streamed frame arrives as raw JSON. The buffered
// application/json path also delivers the whole body as one raw-JSON frame.
// We therefore try the frame as JSON directly, and also scan any "data:"
// lines for the case a frame still carries SSE framing.
func parseFrameUsage(frame []byte) (in, out int, found bool) {
	consider := func(b []byte) {
		b = bytes.TrimSpace(b)
		if len(b) == 0 || b[0] != '{' {
			return
		}
		var ev struct {
			Usage   *usageJSON `json:"usage"`
			Message *struct {
				Usage *usageJSON `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(b, &ev) != nil {
			return
		}
		u := ev.Usage
		if u == nil && ev.Message != nil {
			u = ev.Message.Usage // Anthropic message_start nests usage
		}
		if u == nil {
			return
		}
		if i := u.inputTotal(); i > in {
			in, found = i, true
		}
		if o := u.outputTotal(); o > out {
			out, found = o, true
		}
	}

	consider(frame) // bare-JSON frame (sseframe payload, or buffered body)
	for _, line := range bytes.Split(frame, []byte("\n")) {
		if line = bytes.TrimSpace(line); bytes.HasPrefix(line, []byte("data:")) {
			consider(bytes.TrimPrefix(line, []byte("data:")))
		}
	}
	return in, out, found
}

// usageJSON accepts both Anthropic and OpenAI usage shapes.
type usageJSON struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	PromptTokens             int `json:"prompt_tokens"`
	CompletionTokens         int `json:"completion_tokens"`
}

func (u usageJSON) inputTotal() int {
	return u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens + u.PromptTokens
}

func (u usageJSON) outputTotal() int { return u.OutputTokens + u.CompletionTokens }

func (p *BudgetTrack) todayUTC() string {
	return time.Now().UTC().Format("2006-01-02")
}

func (p *BudgetTrack) resetIfNewDay() {
	today := p.todayUTC()
	if p.ledger.Date != today {
		p.ledger = spendLedger{Date: today}
	}
}

func (p *BudgetTrack) loadLedger() {
	data, err := os.ReadFile(p.cfg.SpendFile)
	if err != nil {
		p.ledger = spendLedger{Date: p.todayUTC()}
		return
	}
	var l spendLedger
	if json.Unmarshal(data, &l) != nil || l.Date != p.todayUTC() {
		p.ledger = spendLedger{Date: p.todayUTC()}
		return
	}
	p.ledger = l
}

func (p *BudgetTrack) saveLedger() {
	data, _ := json.MarshalIndent(p.ledger, "", "  ")
	_ = os.WriteFile(p.cfg.SpendFile, data, 0644)
}

var (
	_ pipeline.Plugin             = (*BudgetTrack)(nil)
	_ pipeline.Configurable       = (*BudgetTrack)(nil)
	_ pipeline.StreamingResponder = (*BudgetTrack)(nil)
)
