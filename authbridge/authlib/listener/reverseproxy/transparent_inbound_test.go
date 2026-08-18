package reverseproxy

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/rossoctl/cortex/authbridge/authlib/auth"
	"github.com/rossoctl/cortex/authbridge/authlib/listener/transparentproxy"
	"github.com/rossoctl/cortex/authbridge/authlib/plugins/jwtvalidation/validation"
)

// withOrigDst stands in for http.Server.ConnContext + the transparent inbound
// listener: it injects a recovered original destination into the request
// context, which is the only channel the Director reads it from.
func withOrigDst(dst string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(transparentproxy.ContextWithOrigDst(r.Context(), dst)))
	})
}

func allowAllAuth() *auth.Auth {
	return auth.New(auth.Config{
		Verifier: &mockVerifier{claims: &validation.Claims{Subject: "user"}},
		Identity: auth.IdentityConfig{Audiences: []string{"my-app"}},
	})
}

// portOfURL extracts the port an httptest server bound, so a test can build a
// realistic "podIP:appPort" destination whose PORT resolves to that server.
func portOfURL(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parsing %q: %v", raw, err)
	}
	_, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("splitting %q: %v", u.Host, err)
	}
	return port
}

// TestTransparentInbound_ForwardsToRecoveredPort is the core behavior: the
// forwarding target comes from the destination the client addressed, not from
// config. The recovered destination names a pod IP that does not exist in the
// test environment — proving the Director rewrote the host to loopback while
// keeping the PORT, which is exactly the on-cluster behavior (the egress guard
// RETURNs loopback, so the hop can't be re-captured).
func TestTransparentInbound_ForwardsToRecoveredPort(t *testing.T) {
	var gotHost, gotXFF string
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		gotXFF = r.Header.Get("X-Forwarded-For")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("app-ok"))
	}))
	defer app.Close()

	srv, err := NewTransparentServer(inboundPipelineFromAuth(t, allowAllAuth()), nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}

	// 10.244.0.5 is a plausible pod IP and is NOT where the app listens; only
	// the port is carried over.
	dst := net.JoinHostPort("10.244.0.5", portOfURL(t, app.URL))
	proxy := httptest.NewServer(withOrigDst(dst, srv.Handler()))
	defer proxy.Close()

	req, _ := http.NewRequest("GET", proxy.URL+"/api/data", nil)
	req.Header.Set("Authorization", "Bearer valid-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (Director should have rewritten to loopback:%s)",
			resp.StatusCode, portOfURL(t, app.URL))
	}
	wantHost := net.JoinHostPort("127.0.0.1", portOfURL(t, app.URL))
	if gotHost != wantHost {
		t.Errorf("app saw Host = %q, want %q (loopback, recovered port)", gotHost, wantHost)
	}
	// REDIRECT preserves the client's source IP, so the app must still be able
	// to see it after the loopback hop.
	if gotXFF == "" {
		t.Error("X-Forwarded-For must reach the app so the real client IP survives the loopback hop")
	}
}

// TestTransparentInbound_NoDestinationFailsClosed locks the fail-closed
// contract: a request the listener cannot attribute to a captured destination
// must be rejected, not forwarded to a guessed target. Without this, the parked
// sentinel backend (or worse, a real one) would receive unvalidated traffic.
func TestTransparentInbound_NoDestinationFailsClosed(t *testing.T) {
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("app must not be reached when no destination was recovered")
		w.WriteHeader(http.StatusOK)
	}))
	defer app.Close()

	srv, err := NewTransparentServer(inboundPipelineFromAuth(t, allowAllAuth()), nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}

	// No withOrigDst wrapper: the context carries nothing.
	proxy := httptest.NewServer(srv.Handler())
	defer proxy.Close()

	req, _ := http.NewRequest("GET", proxy.URL+"/api/data", nil)
	req.Header.Set("Authorization", "Bearer valid-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (fail closed with no recovered destination)", resp.StatusCode)
	}
}

// TestTransparentInbound_StillValidatesJWT guards against the per-connection
// backend path accidentally bypassing the inbound pipeline — the entire reason
// this listener exists is that validation cannot be sidestepped.
func TestTransparentInbound_StillValidatesJWT(t *testing.T) {
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("app must not be reached by an unauthenticated request")
		w.WriteHeader(http.StatusOK)
	}))
	defer app.Close()

	a := auth.New(auth.Config{
		Verifier: &mockVerifier{err: fmt.Errorf("invalid token")},
		Identity: auth.IdentityConfig{Audiences: []string{"my-app"}},
	})
	srv, err := NewTransparentServer(inboundPipelineFromAuth(t, a), nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}

	dst := net.JoinHostPort("10.244.0.5", portOfURL(t, app.URL))
	proxy := httptest.NewServer(withOrigDst(dst, srv.Handler()))
	defer proxy.Close()

	req, _ := http.NewRequest("GET", proxy.URL+"/api/data", nil)
	req.Header.Set("Authorization", "Bearer bogus-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		t.Fatalf("status = 200, want a denial — transparent inbound must still validate")
	}
}

// TestTransparentInbound_FallbackBackendKeepsFixedTarget covers the non-empty
// fallback: a deployment that wants transparent capture but also a defined
// target for anything arriving without a recovered destination should forward
// rather than 502.
func TestTransparentInbound_FallbackBackendKeepsFixedTarget(t *testing.T) {
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("fallback-ok"))
	}))
	defer app.Close()

	srv, err := NewTransparentServer(inboundPipelineFromAuth(t, allowAllAuth()), nil, app.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if srv.perConnBackend {
		t.Fatal("a configured fallback must not enable the fail-closed path")
	}

	proxy := httptest.NewServer(srv.Handler())
	defer proxy.Close()

	req, _ := http.NewRequest("GET", proxy.URL+"/api/data", nil)
	req.Header.Set("Authorization", "Bearer valid-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 via the configured fallback", resp.StatusCode)
	}
}

// TestWrapListener_NoMTLSIsPassthrough documents the split-out wrap: with mTLS
// off it must return the listener untouched, so the transparent path's
// *net.TCPListener (needed for SO_ORIGINAL_DST) is not replaced by a wrapper
// that would hide it from the unwrap walk.
func TestWrapListener_NoMTLSIsPassthrough(t *testing.T) {
	srv, err := NewTransparentServer(inboundPipelineFromAuth(t, allowAllAuth()), nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	if got := srv.WrapListener(ln); got != ln {
		t.Errorf("WrapListener with mTLS off = %T, want the same listener back", got)
	}
}
