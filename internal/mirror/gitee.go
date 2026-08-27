package mirror

import (
	"context"
	"fmt"
	"strings"
)

// DefaultGiteeBase is the canonical Gitee AI (serverless model) host.
const DefaultGiteeBase = "https://ai.gitee.com"

// GiteeTarget mirrors weights to Gitee AI. m1 records no uploads (zero mirror
// uploads); Upload ships in m2 sync. Name/MirrorID/RepoURL are real so the m2
// manifest can record provenance without touching this file's contract.
type GiteeTarget struct {
	Base  string // default DefaultGiteeBase
	Token string // upload credential; m2
}

// Name returns the manifest mirror id prefix.
func (g *GiteeTarget) Name() string { return "gitee-ai" }

// MirrorID is the manifest record for a pinned repo.
func (g *GiteeTarget) MirrorID(repo string) string { return "gitee-ai:" + repo }

// RepoURL is the canonical human-facing mirror URL for a repo.
func (g *GiteeTarget) RepoURL(repo string) string {
	return strings.TrimRight(g.base(), "/") + "/" + repo
}

// Upload copies a local weight file to Gitee AI. m2.
func (g *GiteeTarget) Upload(_ context.Context, _, _, _, _ string) error {
	return fmt.Errorf("%s: %w", g.Name(), ErrMirrorNotImplemented)
}

func (g *GiteeTarget) base() string {
	if g.Base != "" {
		return g.Base
	}
	return DefaultGiteeBase
}
