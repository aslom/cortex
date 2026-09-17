package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rossoctl/cortex/authbridge/authlib/config"
)

// writeBuiltinConfig must produce a config file in cortexDir that loads, presets,
// and validates cleanly and describes a forward-only TLS-bridge observe
// pipeline pointed at caDir — otherwise --local would fail at boot instead of
// giving users a working, hot-reloadable local setup.
func TestDemoConfig_WriteLoadsAndValidates(t *testing.T) {
	cortexDir := t.TempDir()
	caDir := filepath.Join(cortexDir, "ca")

	p, err := writeBuiltinConfig(cortexDir, caDir)
	if err != nil {
		t.Fatalf("writeBuiltinConfig: %v", err)
	}
	if filepath.Dir(p) != cortexDir {
		t.Errorf("config written to %q, want inside %q", p, cortexDir)
	}

	cfg, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	config.ApplyPreset(cfg)
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	if cfg.Mode != config.ModeProxySidecar {
		t.Errorf("Mode = %q, want %q", cfg.Mode, config.ModeProxySidecar)
	}

	roles := cfg.Listener.ActiveRoles()
	if !roles[config.RoleForward] || roles[config.RoleReverse] {
		t.Errorf("expected forward-only roles, got %v", roles)
	}

	// The listeners the demo uses must bind loopback on the uncommon ports the
	// installer probes/prints, never a wildcard that would expose an open forward
	// proxy, the stats endpoint, or the unauthenticated session API (decrypted
	// bodies + injected tokens) to the LAN. The transparent listener isn't started
	// under --local (main.go gates it), so it's not asserted here.
	if got := cfg.Listener.ForwardProxyAddr; got != "127.0.0.1:47600" {
		t.Errorf("ForwardProxyAddr = %q, want loopback 127.0.0.1:47600", got)
	}
	if got := cfg.Listener.SessionAPIAddr; got != "127.0.0.1:47601" {
		t.Errorf("SessionAPIAddr = %q, want loopback 127.0.0.1:47601", got)
	}
	if got := cfg.Stats.StatsAddress; got != "127.0.0.1:47602" {
		t.Errorf("Stats.StatsAddress = %q, want loopback 127.0.0.1:47602", got)
	}

	if cfg.TLSBridge == nil {
		t.Fatalf("expected tls_bridge config, got nil")
	}
	if cfg.TLSBridge.Mode != "enabled" || !cfg.TLSBridge.GenerateCA {
		t.Errorf("expected tls_bridge enabled with generate_ca, got %+v", cfg.TLSBridge)
	}
	if cfg.TLSBridge.CADir != caDir {
		t.Errorf("CADir = %q, want %q", cfg.TLSBridge.CADir, caDir)
	}

	// Assert the exact parser set and order, not just the count — a swapped or
	// renamed plugin would otherwise pass silently.
	gotPlugins := make([]string, len(cfg.Pipeline.Outbound.Plugins))
	for i, p := range cfg.Pipeline.Outbound.Plugins {
		gotPlugins[i] = p.Name
	}
	// tool-prune must come last: it is the request-body mutator, and the
	// pipeline refuses to build a chain where a body reader follows it.
	wantPlugins := []string{"inference-parser", "mcp-parser", "a2a-parser", "tool-prune"}
	if !slices.Equal(gotPlugins, wantPlugins) {
		t.Errorf("outbound plugins = %v, want %v", gotPlugins, wantPlugins)
	}

	// tool-prune ships inert, and that is a property worth pinning: the demo
	// must never silently start rewriting a user's traffic. The empty remove
	// list is the guard — with no tool named there is nothing to remove, whatever
	// the policy — so filling the list is the single, deliberate act that
	// enables it. Asserting the policy too would just pin a default that is
	// meant to be edited.
	var tp *config.PluginEntry
	for i := range cfg.Pipeline.Outbound.Plugins {
		if cfg.Pipeline.Outbound.Plugins[i].Name == "tool-prune" {
			tp = &cfg.Pipeline.Outbound.Plugins[i]
		}
	}
	if tp == nil {
		t.Fatal("tool-prune entry not found")
	}
	if !strings.Contains(string(tp.Config), "\"remove\":[]") &&
		!strings.Contains(string(tp.Config), "\"remove\": []") {
		t.Errorf("tool-prune must ship with an empty remove list, got %s", tp.Config)
	}
}

// TestWriteDemoConfig_PreservesAnExistingFile: the config's own header invites
// editing it, and `abctl tools scan --write` writes a prune list into it. This
// function also runs before any port is bound, so an unconditional overwrite
// meant a --local start that then failed on a port clash silently destroyed those
// edits — which is exactly how a populated remove list was lost in practice.
func TestWriteDemoConfig_PreservesAnExistingFile(t *testing.T) {
	cortexDir := t.TempDir()
	caDir := filepath.Join(cortexDir, "ca")
	p, err := writeBuiltinConfig(cortexDir, caDir)
	if err != nil {
		t.Fatal(err)
	}
	edited := "# operator edit\nmode: proxy-sidecar\n"
	if err := os.WriteFile(p, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	// A second call — a restart — must not clobber it.
	p2, err := writeBuiltinConfig(cortexDir, caDir)
	if err != nil {
		t.Fatal(err)
	}
	if p2 != p {
		t.Errorf("path changed: %q vs %q", p2, p)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != edited {
		t.Errorf("edits were overwritten:\n%s", got)
	}
}

// The moved-CA-dir warning (issue #1033). `--local` derives ca_dir from $HOME via
// defaultCortexDir, so a redirected $HOME — a sandbox, a per-project home, a
// wrapper that sets HOME=$PWD — silently gives every environment a CA of its own.
// All of them are spelled ~/.cortex/ca and all carry CN=authbridge-tls-bridge-ca,
// so nothing in a log line or a directory listing says which one a client holds.
//
// abctl records the value it displaced in .cortex/claude-code-state.json's `prior`
// map, so the two halves of the answer are already on disk. staleClientCAWarning
// compares them and returns the warning args, or nil when there is nothing to say.

// TestStaleClientCAWarning_FiresWhenPriorCAIsElsewhere is the real-world case from
// the issue: prior points into $HOME/.cortex, the CA now in force is a sandbox's.
func TestStaleClientCAWarning_FiresWhenPriorCAIsElsewhere(t *testing.T) {
	prior := "/Users/dev/.cortex/ca/ca.crt"
	current := "/Users/dev/sandbox/proj/.cortex/ca"

	got := staleClientCAWarning(current, prior)

	if got == nil {
		t.Fatal("no warning for a prior CA in a different .cortex dir; this is the state " +
			"that produces 'Self-signed certificate detected' with a healthy proxy")
	}
	joined := fmt.Sprint(got...)
	for _, want := range []string{prior, current} {
		if !strings.Contains(joined, want) {
			t.Errorf("warning omitted %q, so the user cannot see which two CAs are in play: %v", want, got)
		}
	}
}

// TestStaleClientCAWarning_SilentWhenPriorMatches: the overwhelmingly common case
// is one $HOME and a client already pointed at the right file. A warning there
// would fire on every boot and train people to ignore it.
func TestStaleClientCAWarning_SilentWhenPriorMatches(t *testing.T) {
	current := "/Users/dev/.cortex/ca"
	for _, prior := range []string{
		"/Users/dev/.cortex/ca/ca.crt",     // the trust anchor in the active dir
		"/Users/dev/.cortex/ca/bundle.crt", // the bundle in the same dir is equally fine
	} {
		if got := staleClientCAWarning(current, prior); got != nil {
			t.Errorf("staleClientCAWarning(%q, %q) = %v, want nil", current, prior, got)
		}
	}
}

// TestStaleClientCAWarning_SilentWithoutAPriorRecord: no record means nothing to
// compare. A first install and a hand-managed client both land here, and neither
// is evidence of a problem.
func TestStaleClientCAWarning_SilentWithoutAPriorRecord(t *testing.T) {
	if got := staleClientCAWarning("/Users/dev/.cortex/ca", ""); got != nil {
		t.Errorf("staleClientCAWarning with no prior = %v, want nil", got)
	}
}

// TestStaleClientCAWarning_ComparesResolvedPaths: `..` and trailing slashes are
// spelling, not meaning. A false positive here is worse than silence — it would
// tell someone their correctly-configured client is broken.
func TestStaleClientCAWarning_ComparesResolvedPaths(t *testing.T) {
	current := "/Users/dev/.cortex/ca"
	prior := "/Users/dev/sandbox/../.cortex/ca/ca.crt" // resolves into current
	if got := staleClientCAWarning(current, prior); got != nil {
		t.Errorf("staleClientCAWarning did not resolve %q against %q: %v", prior, current, got)
	}
}

// TestPriorCAFromState reads the path out of the record abctl actually writes, so
// a change to that file's shape breaks here rather than silently disabling the
// warning.
func TestPriorCAFromState(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "claude-code-state.json")
	// The shape abctl writes: managed keys, with a null for a key that was absent.
	const state = `{
	  "settings": "/Users/dev/sandbox/proj/.claude/settings.json",
	  "prior": {
	    "NODE_EXTRA_CA_CERTS": "/Users/dev/.cortex/ca/ca.crt",
	    "SSL_CERT_FILE": null,
	    "HTTPS_PROXY": "http://127.0.0.1:47600"
	  }
	}`
	if err := os.WriteFile(statePath, []byte(state), 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}

	if got := priorCAFromState(statePath); got != "/Users/dev/.cortex/ca/ca.crt" {
		t.Errorf("priorCAFromState = %q, want the recorded NODE_EXTRA_CA_CERTS", got)
	}
}

// TestPriorCAFromState_ToleratesMissingAndMalformed: this feeds a diagnostic, so
// every failure mode must answer "" rather than error out or panic. A state file
// that cannot be read is not a reason to fail a proxy boot.
func TestPriorCAFromState_ToleratesMissingAndMalformed(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"absent":            "", // no file written at all
		"not json":          "{{{",
		"no prior key":      `{"settings":"/x"}`,
		"prior null entry":  `{"prior":{"NODE_EXTRA_CA_CERTS":null}}`,
		"prior key missing": `{"prior":{"HTTPS_PROXY":"http://127.0.0.1:47600"}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(dir, name+".json")
			if body != "" {
				if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
					t.Fatalf("write: %v", err)
				}
			}
			if got := priorCAFromState(p); got != "" {
				t.Errorf("priorCAFromState(%s) = %q, want \"\"", name, got)
			}
		})
	}
}
