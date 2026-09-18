// Package apiclient talks to AuthBridge's session events HTTP API at
// :9094 by default. It owns the wire protocol so the TUI only deals in
// domain types.
package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/rossoctl/cortex/authbridge/authlib/pipeline"
	"github.com/rossoctl/cortex/authbridge/authlib/session"
	"github.com/rossoctl/cortex/authbridge/authlib/usage"
)

// Client is a handle to a session API endpoint. Safe for concurrent use.
//
// Two http.Clients share a single Transport: `http` has a 10s timeout for
// short REST calls, `httpStream` has no timeout for SSE. Sharing the
// Transport keeps the idle-connection pool warm across reconnects so a
// long session doesn't leak Transports.
type Client struct {
	endpoint   string
	http       *http.Client
	httpStream *http.Client
}

// New returns a Client pointed at endpoint (e.g. "http://localhost:9094").
// Trailing slash is tolerated.
func New(endpoint string) *Client {
	// Clone the default transport rather than reuse it so tests / multiple
	// Clients don't share connection pools.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	return &Client{
		endpoint: trimSlash(endpoint),
		http: &http.Client{
			Transport: transport,
			Timeout:   10 * time.Second,
		},
		httpStream: &http.Client{
			Transport: transport,
		},
	}
}

// Endpoint returns the server's base URL. Used by the TUI to display context.
func (c *Client) Endpoint() string { return c.endpoint }

// ListSessions fetches /v1/sessions.
func (c *Client) ListSessions(ctx context.Context) ([]session.SessionSummary, error) {
	var body struct {
		Sessions []session.SessionSummary `json:"sessions"`
	}
	if err := c.getJSON(ctx, "/v1/sessions", &body); err != nil {
		return nil, err
	}
	return body.Sessions, nil
}

// SummaryView is the projection every timeline fetch asks for. Must match
// sessionapi's recognised `view` value.
const SummaryView = "summary"

// SnapshotEventLimit is how many events a snapshot asks for.
//
// Sent explicitly rather than relying on the server's default, so what this client is
// willing to hold is visible here and tunable without a server change.
//
// RAISED FROM 500 TO THE SERVER'S OWN CEILING, because the reason for the smaller
// number is gone. 500 was picked when a snapshot carried message bodies: a day-old
// session was 5000 events and a gigabyte of JSON, 17s against this client's 10s
// timeout, and the whole request failed with an empty timeline to show for it. With
// view=summary an event is ~1KB rather than ~209KB, so 2000 events is about 2MB —
// less than half of what 500 full events cost, fetched in a fraction of the time.
//
// The cost of the old number was not just latency. A 1000-event session showed its
// newest 500 and the operator had to know to press [o] for the rest, so "sort
// oldest first" silently meant "oldest of the newest 500". Reaching the server's
// maxEventLimit means a normal session arrives whole and that trap is gone.
//
// A ceiling still exists, and this is it: 2000 is what the server clamps to, so
// asking for more would be a request the server quietly shrinks. Sessions longer
// than that still page, which is what [o] and the "N older" footer note are for.
const SnapshotEventLimit = 2000

// GetSession fetches the most recent SnapshotEventLimit events of a session. Returns an
// error whose Unwrap chain includes ErrNotFound if the server returned 404.
//
// The returned view's TotalEvents is non-zero when older events exist that this
// response does not carry.
func (c *Client) GetSession(ctx context.Context, id string) (*pipeline.SessionView, error) {
	return c.GetSessionTail(ctx, id, SnapshotEventLimit)
}

// GetSessionTail fetches the most recent limit events of a session. The tail is the newest
// page, so this is GetSessionPage with no cursor.
func (c *Client) GetSessionTail(ctx context.Context, id string, limit int) (*pipeline.SessionView, error) {
	return c.GetSessionPage(ctx, id, 0, limit)
}

// ErrNotFound is returned when the server responds 404.
var ErrNotFound = fmt.Errorf("apiclient: not found")

// PipelineView is the decoded shape of GET /v1/pipeline.
type PipelineView struct {
	Inbound  []PipelinePlugin `json:"inbound"`
	Outbound []PipelinePlugin `json:"outbound"`
}

// PipelinePlugin describes one plugin's position, direction, and
// capabilities. Mirrors the server's pipelinePluginView exactly.
type PipelinePlugin struct {
	Name        string          `json:"name"`
	Direction   string          `json:"direction"`
	Position    int             `json:"position"`
	ReadsBody   bool            `json:"readsBody"`
	Requires    []string        `json:"requires,omitempty"`
	RequiresAny []string        `json:"requiresAny,omitempty"`
	Description string          `json:"description,omitempty"`
	Config      json.RawMessage `json:"config,omitempty"`
	Metrics     []PluginMetric  `json:"metrics,omitempty"`
}

// PluginMetric mirrors authlib/pipeline.Metric on the wire. Kept as a local
// type rather than importing the server struct, matching PluginFieldEntry:
// the client owns its decode shape, and a decode test guards the tags
// against drift.
type PluginMetric struct {
	Name  string  `json:"name"`
	Value float64 `json:"value"`
	Unit  string  `json:"unit,omitempty"`
	Note  string  `json:"note,omitempty"`
}

// GetPipeline fetches /v1/pipeline.
func (c *Client) GetPipeline(ctx context.Context) (*PipelineView, error) {
	var view PipelineView
	if err := c.getJSON(ctx, "/v1/pipeline", &view); err != nil {
		return nil, err
	}
	return &view, nil
}

// PluginCatalog is the decoded shape of GET /v1/plugins.
type PluginCatalog struct {
	Plugins []PluginCatalogEntry `json:"plugins"`
}

// PluginCatalogEntry mirrors the server's sessionapi.CatalogEntry.
// Describes a registered plugin's static type-level metadata; the
// catalog includes plugins not currently in the active pipeline.
type PluginCatalogEntry struct {
	Name        string             `json:"name"`
	Direction   string             `json:"direction,omitempty"`
	ReadsBody   bool               `json:"readsBody,omitempty"`
	Requires    []string           `json:"requires,omitempty"`
	RequiresAny []string           `json:"requiresAny,omitempty"`
	Description string             `json:"description,omitempty"`
	Fields      []PluginFieldEntry `json:"fields,omitempty"`
}

// PluginFieldEntry mirrors sessionapi.FieldSchemaEntry — per-field
// schema metadata for a plugin's config. Used by abctl edit's
// templates renderer; nil for plugins without configs.
type PluginFieldEntry struct {
	Name        string             `json:"name"`
	Type        string             `json:"type"`
	Required    bool               `json:"required,omitempty"`
	Description string             `json:"description,omitempty"`
	Default     string             `json:"default,omitempty"`
	Enum        []string           `json:"enum,omitempty"`
	Fields      []PluginFieldEntry `json:"fields,omitempty"`
}

// GetPluginCatalog fetches /v1/plugins. Returns ErrNotFound when the
// server is too old to serve the endpoint (no WithCatalog option) so
// callers can degrade gracefully.
func (c *Client) GetPluginCatalog(ctx context.Context) (*PluginCatalog, error) {
	var cat PluginCatalog
	if err := c.getJSON(ctx, "/v1/plugins", &cat); err != nil {
		return nil, err
	}
	return &cat, nil
}

// getBody issues the GET and hands back the body for the caller to read and close.
//
// Split out of getJSON so a caller that must NOT buffer the whole response — the session
// snapshot, which reaches hundreds of megabytes — can stream the body while still sharing
// this one place that knows how the API reports 404 and other statuses. See snapshot.go.
func (c *Client) getBody(ctx context.Context, path string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.endpoint+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		// Drained before closing so the connection returns to the pool rather than
		// being torn down — abctl polls this API.
		io.Copy(io.Discard, resp.Body) //nolint:errcheck // draining a discarded body
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%s: %w", path, ErrNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%s: unexpected status %d", path, resp.StatusCode)
	}
	return resp.Body, nil
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	body, err := c.getBody(ctx, path)
	if err != nil {
		return err
	}
	defer body.Close() //nolint:errcheck // read-only body
	if err := json.NewDecoder(body).Decode(out); err != nil {
		return fmt.Errorf("%s: decode: %w", path, err)
	}
	return nil
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

// GetUsage fetches time-bucketed usage aggregates from GET /v1/usage.
//
// sessionID empty means all sessions combined. resolution is the width of the
// buckets returned — the server folds, so a 1h window at 5m arrives as 12
// buckets rather than 60. Read the returned Snapshot.BucketSeconds rather than
// assuming the requested resolution was honored.
//
// Returns ErrNotFound when the proxy has no usage aggregator wired (older
// binary, or session tracking disabled), which callers should render as
// "unavailable" rather than as an empty chart.
func (c *Client) GetUsage(ctx context.Context, window, resolution time.Duration, sessionID string, group usage.Group) (*usage.Snapshot, error) {
	q := url.Values{}
	q.Set("window", window.String())
	q.Set("resolution", resolution.String())
	if sessionID != "" {
		q.Set("session", sessionID)
	}
	if group != "" && group != usage.GroupNone {
		q.Set("group", string(group))
	}
	var snap usage.Snapshot
	if err := c.getJSON(ctx, "/v1/usage?"+q.Encode(), &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}
