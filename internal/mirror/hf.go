// Package mirror resolves and downloads pinned weight files from a source
// (Hugging Face in v0.1) and defines the upload-target contract used by the m2
// mirror-sync flow (Gitee AI + ModelScope). m1 implements only the source half
// (download + hash input); Target.Upload is wired in m2.
package mirror

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/schollz/progressbar/v3"
)

// DefaultHFBase is the canonical Hugging Face host.
const DefaultHFBase = "https://huggingface.co"

// ErrMirrorNotImplemented marks a Target.Upload path that ships in m2. m1
// performs zero mirror uploads; the Target types exist so m2 sync can wire them
// without touching the lockfile/verify packages.
var ErrMirrorNotImplemented = errors.New("mirror: upload not implemented in m1 (ships in m2 sync)")

// Source downloads pinned weight files from a weight host.
type Source interface {
	// ListFiles returns the concrete file names under repo@revision matching
	// the given patterns. Patterns without glob meta-characters are returned
	// as-is (no network); glob patterns (e.g. "*.safetensors") are expanded
	// against the host's file tree.
	ListFiles(ctx context.Context, repo, revision string, files []string) ([]string, error)
	// Download fetches one file into dst, returning the bytes written.
	Download(ctx context.Context, repo, revision, file string, dst io.Writer) (int64, error)
}

// Target uploads a mirrored file to a CN host and reports the provenance
// identifier recorded in the hash manifest. Wired in m2 (mirror sync).
type Target interface {
	// Name is the mirror id prefix recorded in HashEntry.Mirrors, e.g.
	// "gitee-ai" or "modelscope".
	Name() string
	// MirrorID is the manifest record for a pinned repo, e.g. "gitee-ai:owner/repo".
	MirrorID(repo string) string
	// RepoURL is the canonical human-facing mirror URL for a repo.
	RepoURL(repo string) string
	// Upload copies a local weight file to the mirror. m2.
	Upload(ctx context.Context, repo, revision, file, srcPath string) error
}

// HFSource downloads from huggingface.co. Base can be overridden (e.g. an
// offline fixture for the demo, or a self-hosted endpoint) via the HFBase flag
// or the HFLOCK_HF_BASE environment variable.
type HFSource struct {
	Base    string
	Client  *http.Client
	ShowBar bool // render a download progress bar (TTY only)
}

// NewHFSource returns an HFSource pointed at the canonical Hugging Face host.
func NewHFSource() *HFSource {
	return &HFSource{Base: DefaultHFBase, Client: http.DefaultClient}
}

type hfTreeEntry struct {
	Type string `json:"type"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// ListFiles expands the given file patterns. Patterns with no glob
// meta-characters are returned verbatim (no network); any glob pattern is
// expanded against the repo's file tree.
func (h *HFSource) ListFiles(ctx context.Context, repo, revision string, files []string) ([]string, error) {
	var exact, patterns []string
	for _, f := range files {
		if strings.ContainsAny(f, "*?[") {
			patterns = append(patterns, f)
		} else {
			exact = append(exact, f)
		}
	}
	out := dedupe(exact)
	if len(patterns) == 0 {
		return out, nil
	}
	all, err := h.listTree(ctx, repo, revision)
	if err != nil {
		return nil, fmt.Errorf("list tree %s@%s: %w", repo, revision, err)
	}
	for _, p := range patterns {
		for _, f := range all {
			if ok, _ := path.Match(p, f); ok {
				out = append(out, f)
			}
		}
	}
	return dedupe(out), nil
}

// listTree calls the HF tree API and returns the file paths (directories
// excluded). The tree API paginates via the Link response header (rel="next",
// default page size 1000 entries), so every page is followed — a single-page
// read would silently truncate glob expansion on large sharded repos and the
// hash manifest would omit the files past page 1.
func (h *HFSource) listTree(ctx context.Context, repo, revision string) ([]string, error) {
	u := strings.TrimRight(h.baseURL(), "/") + "/" +
		urlPath("api", "models", repo, "tree", revision) + "?recursive=true"
	var files []string
	seen := make(map[string]bool)
	for u != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		resp, err := h.client().Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		var entries []hfTreeEntry
		err = json.NewDecoder(resp.Body).Decode(&entries)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.Type == "directory" || e.Path == "" || seen[e.Path] {
				continue
			}
			seen[e.Path] = true
			files = append(files, e.Path)
		}
		u = nextLink(resp.Header.Get("Link"), u)
	}
	return files, nil
}

// nextLink extracts the rel="next" target from an RFC 8288 Link header
// ("</api/models/o/r/tree/main?page=2>; rel=\"next\""), resolving relative
// targets against the URL they came from. Empty string when no next page.
func nextLink(header, base string) string {
	for _, part := range strings.Split(header, ",") {
		lt := strings.Index(part, "<")
		gt := strings.Index(part, ">")
		if lt < 0 || gt <= lt {
			continue
		}
		raw := strings.TrimSpace(part[lt+1 : gt])
		params := part[gt+1:]
		if !strings.Contains(params, `rel="next"`) && !strings.Contains(params, "rel=next") {
			continue
		}
		target, err := url.Parse(raw)
		if err != nil {
			continue
		}
		if target.IsAbs() {
			return target.String()
		}
		if b, err := url.Parse(base); err == nil {
			return b.ResolveReference(target).String()
		}
		return raw
	}
	return ""
}

// Download fetches one file from {base}/{repo}/resolve/{revision}/{file} into
// dst. When ShowBar is set and the host reports a content length, a progress
// bar is layered over dst.
func (h *HFSource) Download(ctx context.Context, repo, revision, file string, dst io.Writer) (int64, error) {
	u := strings.TrimRight(h.baseURL(), "/") + "/" +
		urlPath(repo, "resolve", revision, file)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := h.client().Do(req)
	if err != nil {
		return 0, fmt.Errorf("download %s: %w", file, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("download %s: HTTP %d", file, resp.StatusCode)
	}
	w := dst
	if h.ShowBar && resp.ContentLength > 0 {
		bar := progressbar.NewOptions64(
			resp.ContentLength,
			progressbar.OptionSetDescription(file),
			progressbar.OptionSetWriter(os.Stderr),
			progressbar.OptionShowBytes(true),
			progressbar.OptionOnCompletion(func() { fmt.Fprintln(os.Stderr) }),
			progressbar.OptionSetWidth(40),
		)
		w = io.MultiWriter(dst, bar)
	}
	return io.Copy(w, resp.Body)
}

func (h *HFSource) baseURL() string {
	if h.Base != "" {
		return h.Base
	}
	return DefaultHFBase
}

func (h *HFSource) client() *http.Client {
	if h.Client != nil {
		return h.Client
	}
	return http.DefaultClient
}

// urlPath builds a URL path segment by escaping each "/"-separated atom so a
// repo like "deepseek-ai/DeepSeek-V3" keeps its slash while special chars in
// any atom are percent-encoded.
func urlPath(segments ...string) string {
	var atoms []string
	for _, s := range segments {
		for _, part := range strings.Split(s, "/") {
			if part != "" {
				atoms = append(atoms, url.PathEscape(part))
			}
		}
	}
	return strings.Join(atoms, "/")
}

// dedupe removes duplicates, preserving first-seen order.
func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
