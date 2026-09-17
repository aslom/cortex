package edit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// localConfigMode is the permission a config file gets when Apply has to
// create one. 0600, not 0644: this file names the TLS bridge's CA directory
// and the addresses every local agent proxies through.
const localConfigMode os.FileMode = 0o600

// FileStore edits the runtime config of a Cortex running on this machine —
// ~/.cortex/config.yaml, the file the proxy was started with and is watching.
//
// Simpler than ConfigMapStore in every step, because the wrapper the cluster
// path spends most of its code on is absent: the file IS the runtime YAML, so
// there is no data.config.yaml to extract, no literal-block scalar to re-emit,
// and no server-managed metadata to strip. Apply is a write instead of a
// server-side apply, and the proxy's own fsnotify watcher plays the part
// kubelet's ConfigMap sync plays in a cluster.
type FileStore struct {
	Path string
}

// Fetch reads the file and locates the pipeline subtree.
//
// Original and InnerYAML are the same bytes here. That is not redundancy: it
// is what makes Build a no-op, and it means the rollback path (re-Apply the
// bytes Fetch returned) restores the file exactly.
//
// ctx is unused: this is local file I/O with no cancellable wait, unlike the
// kubectl round-trips ConfigMapStore makes. Kept in the signature because Store
// is one interface and a context-free method on it would be the odd one out.
func (s FileStore) Fetch(ctx context.Context) (*FetchedPipeline, error) {
	// Refuse a symlink rather than quietly doing the wrong thing with it.
	//
	// Apply is a rename over Path, which would replace the link with a regular
	// file and permanently detach a config someone deliberately points at their
	// dotfiles. Resolving the link instead is worse: the proxy's reloader
	// watches filepath.Dir of the path it was GIVEN and matches on
	// filepath.Base, so writing the link's target — in another directory —
	// fires no event, and the edit would apply, never reload, time out at
	// PollDeadline and roll back. Failing here, before $EDITOR opens, costs the
	// operator nothing and names the fix.
	if fi, err := os.Lstat(s.Path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		target, rerr := filepath.EvalSymlinks(s.Path)
		if rerr != nil {
			target = "its target"
		}
		return nil, fmt.Errorf(
			"%s is a symlink to %s; edit that file directly, or start the proxy with --config %s so it watches the real path",
			s.Path, target, target)
	}
	b, err := os.ReadFile(s.Path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.Path, err)
	}
	start, end, err := FindPipelineRange(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", s.Path, err)
	}
	return &FetchedPipeline{
		Original:      b,
		InnerYAML:     b,
		PipelineStart: start,
		PipelineEnd:   end,
	}, nil
}

// Describe names the file, not "ConfigMap", and states the real latency: the
// proxy's fsnotify watcher plus its reload debounce, not a kubelet sync. The
// unreachable hint drops the port-forward, which does not exist here — the
// stats endpoint is the local proxy itself.
func (s FileStore) Describe() Target {
	return Target{
		Noun: "config file",
		// Kept short enough to render on one line in the overlay's 76-column
		// box, like the ConfigMap hint it sits opposite.
		WaitHint:        "the proxy watches this file; reloads land in about a second",
		UnreachableHint: "is the local proxy still running?",
	}
}

// Build returns newInner unchanged: a file has no outer document to rebuild.
//
// Deliberately not a yaml round-trip. Passing the config through
// yaml.Marshal would reflow it and drop every comment — and this file ships
// with comments explaining why each port is pinned, which is exactly the
// content a user editing it needs to keep.
func (s FileStore) Build(orig *FetchedPipeline, newInner []byte) ([]byte, error) {
	return newInner, nil
}

// Apply writes payload to a temp file in the same directory and renames it
// over the target.
//
// Write-then-rename, not a truncate-and-write, because the proxy is watching
// this exact path: a reader that catches the file mid-write sees invalid YAML
// and books a reload failure against an edit the user never made. rename(2)
// is atomic within a filesystem, so the watcher only ever observes the whole
// file — which also requires the temp to be a sibling, since a rename across
// filesystems fails.
//
// The temp's name must not be the config's own, or the reloader (which
// watches the directory and filters on the base name) would fire on it.
//
// ctx is unused, as in Fetch: a write has no cancellable wait.
func (s FileStore) Apply(ctx context.Context, payload []byte) (time.Time, error) {
	dir := filepath.Dir(s.Path)

	// Carry the existing file's permissions across. CreateTemp makes 0600,
	// which happens to match what Cortex writes, but inheriting rather than
	// assuming means Apply never silently changes the mode of a file someone
	// deliberately locked down further.
	mode := localConfigMode
	if st, err := os.Stat(s.Path); err == nil {
		mode = st.Mode().Perm()
	}

	tmp, err := os.CreateTemp(dir, ".abctl-config-*.yaml")
	if err != nil {
		return time.Time{}, fmt.Errorf("create temp beside %s: %w", s.Path, err)
	}
	tmpName := tmp.Name()
	// Every failure below leaves the original file untouched and takes the
	// temp with it. A half-written sibling in ~/.cortex is litter at best and,
	// if it ever collided with the watched name, a broken proxy at worst.
	defer func() {
		if tmpName != "" {
			os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		return time.Time{}, fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return time.Time{}, fmt.Errorf("chmod temp: %w", err)
	}
	// Sync before rename: rename orders the directory entry, not the data, so
	// a crash between the two could otherwise publish a file whose contents
	// never reached the disk.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return time.Time{}, fmt.Errorf("sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return time.Time{}, fmt.Errorf("close temp: %w", err)
	}

	// Before the rename, so the poll's "did last_success move past this?"
	// comparison cannot be beaten by the reload it is waiting for.
	applyTime := time.Now()
	if err := os.Rename(tmpName, s.Path); err != nil {
		return time.Time{}, fmt.Errorf("replace %s: %w", s.Path, err)
	}
	tmpName = "" // renamed away; nothing left to clean up

	// Sync the directory too. rename(2) orders the data against the entry, but
	// the ENTRY itself is not durable until its directory is synced — a crash
	// here would otherwise lose the edit outright, which is a worse outcome
	// than the torn write the temp-and-rename dance exists to prevent.
	//
	// Best-effort on purpose: the rename has already succeeded, so the edit is
	// live and the proxy will reload it. Returning an error now would report a
	// failure for a change that did take effect.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return applyTime, nil
}
