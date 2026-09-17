package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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

// The moved-CA warning (issue #1033). `--local` derives ca_dir from $HOME via
// defaultCortexDir, so a redirected $HOME — a sandbox, a per-project home, a
// wrapper that sets HOME=$PWD — silently gives every environment a CA of its own.
// All of them are spelled ~/.cortex/ca and all carry CN=authbridge-tls-bridge-ca,
// so nothing in a log line or a directory listing says which one a client holds.
//
// The comparison is on CERTIFICATES, not paths, and these tests pin why. Comparing
// directories looked equivalent and was not: `prior` holds whatever the user's
// settings had before `abctl claude-code enable`, which is often not a Cortex CA at
// all, and abctl freezes that record on first write — so a path comparison both
// fired on innocent setups and could never go quiet again.

// writeTestCA persists a self-signed CA at path with the given CN and returns its
// PEM, so a test can build both "one of ours" and "somebody else's" anchors.
func writeTestCA(t *testing.T, path, commonName string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()), // distinct per call
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if path != "" {
		if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return pemBytes
}

// TestStaleClientCAWarning_FiresOnADifferentBridgeCA is the real-world case from the
// issue: the client's anchor is one of ours, but not the one now in force.
func TestStaleClientCAWarning_FiresOnADifferentBridgeCA(t *testing.T) {
	dir := t.TempDir()
	clientCA := filepath.Join(dir, "client-ca.crt")
	writeTestCA(t, clientCA, "authbridge-tls-bridge-ca")
	inForce := writeTestCA(t, "", "authbridge-tls-bridge-ca")

	got := staleClientCAWarning("/Users/dev/sandbox/proj/.cortex/ca", clientCA, inForce)

	if got == nil {
		t.Fatal("no warning for a client holding a different bridge CA; this is the state " +
			"that produces 'Self-signed certificate detected' against a healthy proxy")
	}
	joined := fmt.Sprint(got...)
	// Both fingerprints must appear, since telling the two CAs apart is the entire
	// point — their subjects are identical.
	for _, want := range []string{clientCA, "client_ca_fingerprint", "ca_fingerprint"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warning omitted %q: %v", want, got)
		}
	}
}

// TestStaleClientCAWarning_SilentWhenClientHoldsTheCAInForce is the case a path
// comparison could never reach. abctl records `prior` on the FIRST enable only and
// refuses to overwrite it, so the recorded path stays pointing at the old location
// forever. Once the client actually trusts the current CA the warning must stop,
// or it fires on every boot for the rest of the install's life and teaches the user
// to ignore it.
func TestStaleClientCAWarning_SilentWhenClientHoldsTheCAInForce(t *testing.T) {
	dir := t.TempDir()
	clientCA := filepath.Join(dir, "ca.crt")
	// Same bytes on both sides: the recorded path is stale, the CONTENT is current.
	inForce := writeTestCA(t, clientCA, "authbridge-tls-bridge-ca")

	if got := staleClientCAWarning("/some/other/.cortex/ca", clientCA, inForce); got != nil {
		t.Errorf("warned about a client that already trusts the CA in force, from a stale "+
			"recorded path — this warning can never go quiet: %v", got)
	}
}

// TestStaleClientCAWarning_SilentForAForeignCA is the corporate-proxy false positive.
// `prior` is whatever was in the user's settings before enable, which behind a
// corporate proxy is routinely a system root bundle. A directory comparison fired on
// that — a setup that was never broken — and then advised `--ca-dir /etc/ssl/certs`,
// which would point the bridge's generate path at a system directory.
func TestStaleClientCAWarning_SilentForAForeignCA(t *testing.T) {
	dir := t.TempDir()
	inForce := writeTestCA(t, "", "authbridge-tls-bridge-ca")

	corporate := filepath.Join(dir, "corp-root.pem")
	writeTestCA(t, corporate, "ACME Corporate Root CA")
	if got := staleClientCAWarning("/Users/dev/.cortex/ca", corporate, inForce); got != nil {
		t.Errorf("warned about a corporate root CA, and would advise pointing --ca-dir at "+
			"a system directory: %v", got)
	}

	// Not a certificate at all, and a path that does not exist: silence, not a claim.
	garbage := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(garbage, []byte("not a certificate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{garbage, filepath.Join(dir, "absent.crt")} {
		if got := staleClientCAWarning("/Users/dev/.cortex/ca", p, inForce); got != nil {
			t.Errorf("staleClientCAWarning(%q) = %v, want nil", p, got)
		}
	}
}

// TestStaleClientCAWarning_SilentWithoutInputs: no record and no loaded CA are both
// ordinary states (a first install, a bridge that is off). Neither is evidence.
func TestStaleClientCAWarning_SilentWithoutInputs(t *testing.T) {
	inForce := writeTestCA(t, "", "authbridge-tls-bridge-ca")
	if got := staleClientCAWarning("/Users/dev/.cortex/ca", "", inForce); got != nil {
		t.Errorf("warned with no prior record: %v", got)
	}
	dir := t.TempDir()
	clientCA := filepath.Join(dir, "ca.crt")
	writeTestCA(t, clientCA, "authbridge-tls-bridge-ca")
	if got := staleClientCAWarning("/Users/dev/.cortex/ca", clientCA, nil); got != nil {
		t.Errorf("warned with no CA in force: %v", got)
	}
}

// TestCertFingerprint_MatchesOpenSSL: the value exists to be compared by eye against
// `openssl x509 -noout -fingerprint -sha256`, so it must be that command's encoding
// — uppercase hex, colon-separated. Mirrors the forward proxy's caFingerprint test.
func TestCertFingerprint_MatchesOpenSSL(t *testing.T) {
	pemBytes := writeTestCA(t, "", "authbridge-tls-bridge-ca")
	blk, _ := pem.Decode(pemBytes)
	crt, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	sum := sha256.Sum256(crt.Raw)
	want := make([]string, 0, len(sum))
	for _, b := range sum {
		want = append(want, fmt.Sprintf("%02X", b))
	}
	if got, expected := certFingerprint(crt), strings.Join(want, ":"); got != expected {
		t.Errorf("certFingerprint() = %q, want %q", got, expected)
	}
}
