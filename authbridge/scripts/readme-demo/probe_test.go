package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func demoFixture() Fixture {
	return Fixture{Sessions: []FixtureSession{
		{ID: "api-7f3c", Title: "fix the retry handler", Model: "claude-opus-5", Turns: []Turn{
			{Messages: 4, Tools: 15, Input: 1800, CacheRead: 46000, CacheWrite: 9000, Output: 620, PromptUSD: 0.098, OutputUSD: 0.047,
				ToolCall: "github-tool-mcp", ToolHost: "github-tool-mcp"},
			{Messages: 10, Tools: 15, Input: 900, CacheRead: 93000, CacheWrite: 2400, Output: 810, PromptUSD: 0.121, OutputUSD: 0.061},
			{Messages: 16, Tools: 15, Input: 1100, CacheRead: 141000, CacheWrite: 3100, Output: 940, PromptUSD: 0.174, OutputUSD: 0.071},
		}},
		{ID: "web-2a91", Title: "add dark mode toggle", Model: "claude-opus-5", Turns: []Turn{
			{Messages: 4, Tools: 15, Input: 1200, CacheRead: 21000, CacheWrite: 6400, Output: 380, PromptUSD: 0.061, OutputUSD: 0.029},
			{Messages: 8, Tools: 15, Input: 700, CacheRead: 44000, CacheWrite: 1800, Output: 520, PromptUSD: 0.074, OutputUSD: 0.039},
		}},
		{ID: "infra-55de", Title: "debug the helm chart", Model: "claude-sonnet-5", Turns: []Turn{
			{Messages: 4, Tools: 15, Input: 800, CacheRead: 12000, CacheWrite: 3200, Output: 240, PromptUSD: 0.012, OutputUSD: 0.004},
		}},
	}}
}

func newProbe(t *testing.T) *Capturer {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	f := demoFixture()
	if err := WriteTitles(home, f); err != nil {
		t.Fatal(err)
	}
	c, err := NewCapturer(f, 120, 34, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestCapture_SessionsTableShowsMoneyAndTitles(t *testing.T) {
	c := newProbe(t)
	got := ansi.Strip(c.Screen())
	fmt.Printf("\n########## SESSIONS ##########\n%s\n", got)

	for _, want := range []string{
		"SESSION", "TITLE", "TOKENS", "COST", "SAVED~", "CONTEXT(1M)",
		"fix the retry handler", "add dark mode toggle", "debug the helm chart",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("sessions table missing %q", want)
		}
	}
	if strings.Count(got, "$") < 2 {
		t.Errorf("expected COST figures in the table, got none")
	}
}

func TestCapture_SpendTiersAndUsageAndDrilldown(t *testing.T) {
	c := newProbe(t)

	c.Press("$")
	tiers := ansi.Strip(c.Screen())
	fmt.Printf("\n########## $ SPEND TIERS ##########\n%s\n", tiers)

	c.Press("esc", "u")
	usage := ansi.Strip(c.Screen())
	fmt.Printf("\n########## u USAGE ##########\n%s\n", usage)

	c.Press("esc", "G", "enter")
	events := ansi.Strip(c.Screen())
	fmt.Printf("\n########## EVENTS ##########\n%s\n", events)

	c.Press("enter")
	detail := ansi.Strip(c.Screen())
	fmt.Printf("\n########## DETAIL ##########\n%s\n", detail)
}
