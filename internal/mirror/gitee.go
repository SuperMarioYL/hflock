package mirror

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DefaultGiteeBase is the canonical Gitee AI (serverless model) host.
const DefaultGiteeBase = "https://ai.gitee.com"

// GiteeTarget mirrors weights to Gitee AI. Gitee AI exposes no REST upload
// API — the platform's documented upload mechanism is git itself (see
// https://ai.gitee.com/docs/products/models/upload): create the model repo in
// the web UI, git clone it, commit the files, push. hflock performs exactly
// that git flow against a persistent clone; large LFS-tracked weights
// additionally require the operator's gai/git-lfs setup, which hflock leaves
// to the environment.
type GiteeTarget struct {
	Base      string // default DefaultGiteeBase
	Token     string // Gitee AI access token (git https credential)
	User      string // git https username; default "oauth2"
	CloneRoot string // directory holding persistent clones; default os.TempDir()/hflock-gitee
}

// Name returns the manifest mirror id prefix.
func (g *GiteeTarget) Name() string { return "gitee-ai" }

// MirrorID is the manifest record for a pinned repo.
func (g *GiteeTarget) MirrorID(repo string) string { return "gitee-ai:" + repo }

// RepoURL is the canonical human-facing mirror URL for a repo.
func (g *GiteeTarget) RepoURL(repo string) string {
	return strings.TrimRight(g.base(), "/") + "/" + repo
}

// UploadRepo clones the mirror repo on first use (clone-of-empty is fine),
// copies the files in, commits when anything changed, and pushes. Re-running
// with identical content makes no new commit (idempotent); the pin set is
// still fully re-processed each run — delta sync is out of scope.
func (g *GiteeTarget) UploadRepo(ctx context.Context, repo, revision string, files map[string]string) error {
	if len(files) == 0 {
		return nil
	}
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("gitee-ai: git not found in PATH — git push is the platform's documented upload mechanism (https://ai.gitee.com/docs/products/models/upload)")
	}
	cloneDir := filepath.Join(g.cloneRoot(), sanitizeRepo(repo))
	if _, err := os.Stat(filepath.Join(cloneDir, ".git")); err != nil {
		if err := os.MkdirAll(filepath.Dir(cloneDir), 0o755); err != nil {
			return fmt.Errorf("gitee-ai: create clone dir: %w", err)
		}
		if out, err := g.git(ctx, "", "clone", g.remoteURL(repo), cloneDir); err != nil {
			return fmt.Errorf("gitee-ai: clone %s: %w: %s", repo, err, g.redact(string(out)))
		}
	}
	for rel, src := range files {
		// rel is repo-relative; keep every destination inside the clone.
		clean := filepath.Clean(filepath.FromSlash(rel))
		if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) || filepath.IsAbs(clean) {
			return fmt.Errorf("gitee-ai: refusing path outside the mirror repo: %q", rel)
		}
		dst := filepath.Join(cloneDir, clean)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("gitee-ai: create %s: %w", dst, err)
		}
		if err := copyFileToLocal(src, dst); err != nil {
			return fmt.Errorf("gitee-ai: copy %s: %w", rel, err)
		}
	}
	if out, err := g.git(ctx, cloneDir, "add", "-A"); err != nil {
		return fmt.Errorf("gitee-ai: stage %s: %w: %s", repo, err, g.redact(string(out)))
	}
	out, err := g.git(ctx, cloneDir, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("gitee-ai: status %s: %w: %s", repo, err, g.redact(string(out)))
	}
	if strings.TrimSpace(string(out)) == "" {
		return nil // nothing changed — mirror already holds these bytes
	}
	msg := "hflock: mirror " + repo
	if revision != "" {
		msg += "@" + revision
	}
	if out, err := g.git(ctx, cloneDir,
		"-c", "user.name=hflock", "-c", "user.email=hflock@users.noreply.github.com",
		"commit", "-m", msg); err != nil {
		return fmt.Errorf("gitee-ai: commit %s: %w: %s", repo, err, g.redact(string(out)))
	}
	if out, err := g.git(ctx, cloneDir, "push", "origin", "HEAD"); err != nil {
		return fmt.Errorf("gitee-ai: push %s: %w: %s", repo, err, g.redact(string(out)))
	}
	return nil
}

// git runs a git command (dir == "" runs in the current directory) and
// returns its combined output. Callers redact the token before reporting it.
func (g *GiteeTarget) git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

// remoteURL builds the git remote. With a token it is embedded as an https
// credential (matching Gitee AI's https git hosting); without one the base is
// used verbatim, which also lets tests point at a local bare repository.
func (g *GiteeTarget) remoteURL(repo string) string {
	base := strings.TrimRight(g.base(), "/")
	if g.Token == "" || !strings.HasPrefix(base, "http") {
		return base + "/" + repo + ".git"
	}
	user := g.User
	if user == "" {
		user = "oauth2"
	}
	host := strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://")
	return fmt.Sprintf("https://%s:%s@%s/%s.git", user, g.Token, host, repo)
}

// redact strips the access token from git output before it can reach an
// error message or log.
func (g *GiteeTarget) redact(s string) string {
	if g.Token == "" {
		return s
	}
	return strings.ReplaceAll(s, g.Token, "***")
}

func (g *GiteeTarget) base() string {
	if g.Base != "" {
		return strings.TrimRight(g.Base, "/")
	}
	return DefaultGiteeBase
}

func (g *GiteeTarget) cloneRoot() string {
	if g.CloneRoot != "" {
		return g.CloneRoot
	}
	return filepath.Join(os.TempDir(), "hflock-gitee")
}

// sanitizeRepo flattens path separators in a repo id so one repo can never
// escape the clone root.
func sanitizeRepo(repo string) string {
	return strings.NewReplacer("/", "_", "\\", "_").Replace(repo)
}

func copyFileToLocal(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
