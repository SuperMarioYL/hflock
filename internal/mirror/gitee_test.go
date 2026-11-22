package mirror

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGiteeTarget_Metadata(t *testing.T) {
	g := &GiteeTarget{}
	if g.Name() != "gitee-ai" {
		t.Fatalf("name = %q", g.Name())
	}
	if g.MirrorID("deepseek-ai/DeepSeek-V3") != "gitee-ai:deepseek-ai/DeepSeek-V3" {
		t.Fatalf("mirrorid = %q", g.MirrorID("deepseek-ai/DeepSeek-V3"))
	}
	if g.RepoURL("deepseek-ai/DeepSeek-V3") != DefaultGiteeBase+"/deepseek-ai/DeepSeek-V3" {
		t.Fatalf("repo url = %q", g.RepoURL("deepseek-ai/DeepSeek-V3"))
	}
}

func TestGitee_UploadRepoRequiresGit(t *testing.T) {
	g := &GiteeTarget{CloneRoot: t.TempDir()}
	t.Setenv("PATH", "")
	err := g.UploadRepo(context.Background(), "o/r", "", map[string]string{"config.json": "/tmp/x"})
	if err == nil || !strings.Contains(err.Error(), "git not found") {
		t.Fatalf("err = %v, want git-not-found error", err)
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

// The Gitee AI stand-in is a local bare git repo — the platform's documented
// upload mechanism IS git push, so the test exercises the real clone/commit/
// push code path end-to-end, offline.
func TestGitee_UploadRepoGitPush(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	base := t.TempDir() // stands in for https://ai.gitee.com: the remote is {base}/{repo}.git
	if err := os.MkdirAll(filepath.Join(base, "o"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	bare := filepath.Join(base, "o", "r.git")
	runGit(t, base, "init", "--bare", bare)

	g := &GiteeTarget{Base: base, CloneRoot: filepath.Join(t.TempDir(), "clones")}
	src := writeFileT(t, filepath.Join(t.TempDir(), "config.json"), `{"k":1}`)
	files := map[string]string{"config.json": src}

	if err := g.UploadRepo(context.Background(), "o/r", "v1", files); err != nil {
		t.Fatalf("UploadRepo: %v", err)
	}

	// the pushed bytes are exactly the local bytes
	workspace := t.TempDir()
	verify := filepath.Join(workspace, "verify")
	runGit(t, workspace, "clone", bare, verify)
	got, err := os.ReadFile(filepath.Join(verify, "config.json"))
	if err != nil {
		t.Fatalf("read pushed file: %v", err)
	}
	if string(got) != `{"k":1}` {
		t.Fatalf("pushed bytes = %q", got)
	}
	count := strings.TrimSpace(runGit(t, workspace, "--git-dir", bare, "rev-list", "--all", "--count"))
	if count != "1" {
		t.Fatalf("commits after first push = %s, want 1", count)
	}

	// re-uploading identical content makes no new commit (idempotent)
	if err := g.UploadRepo(context.Background(), "o/r", "v1", files); err != nil {
		t.Fatalf("UploadRepo second: %v", err)
	}
	count = strings.TrimSpace(runGit(t, workspace, "--git-dir", bare, "rev-list", "--all", "--count"))
	if count != "1" {
		t.Fatalf("commits after identical re-upload = %s, want 1", count)
	}

	// uploading changed content pushes a new commit
	changed := writeFileT(t, filepath.Join(t.TempDir(), "config2.json"), `{"k":2}`)
	if err := g.UploadRepo(context.Background(), "o/r", "v1", map[string]string{"config.json": changed}); err != nil {
		t.Fatalf("UploadRepo changed: %v", err)
	}
	count = strings.TrimSpace(runGit(t, workspace, "--git-dir", bare, "rev-list", "--all", "--count"))
	if count != "2" {
		t.Fatalf("commits after changed upload = %s, want 2", count)
	}
}

func TestGitee_RedactStripsToken(t *testing.T) {
	g := &GiteeTarget{Token: "sekrit"}
	if got := g.redact("https://oauth2:sekrit@ai.gitee.com/o/r.git"); strings.Contains(got, "sekrit") {
		t.Fatalf("redact leaked token: %q", got)
	}
	if g.remoteURL("o/r") != "https://oauth2:sekrit@ai.gitee.com/o/r.git" {
		t.Fatalf("remoteURL = %q", g.remoteURL("o/r"))
	}
	// a custom user is honored
	g.User = "alice"
	if !strings.HasPrefix(g.remoteURL("o/r"), "https://alice:sekrit@") {
		t.Fatalf("remoteURL = %q", g.remoteURL("o/r"))
	}
}

// Regression test for the v0.2.0 defect where a first clone interrupted after
// git had created the target directory (kill -9, OOM, sleep) left a directory
// without .git behind; every later sync then failed with "destination path
// already exists and is not an empty directory" until a manual rm -rf.
func TestGitee_StaleCloneDirRecovered(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "o"), 0o755); err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(base, "o", "r.git")
	runGit(t, base, "init", "--bare", bare)

	cloneRoot := filepath.Join(t.TempDir(), "clones")
	g := &GiteeTarget{Base: base, CloneRoot: cloneRoot}

	// simulate the killed first clone: a directory with junk, no .git
	stale := filepath.Join(cloneRoot, "o", "r")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "partial.tmp"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}

	src := writeFileT(t, filepath.Join(t.TempDir(), "config.json"), `{"k":1}`)
	if err := g.UploadRepo(context.Background(), "o/r", "v1", map[string]string{"config.json": src}); err != nil {
		t.Fatalf("UploadRepo must recover from a stale clone dir: %v", err)
	}
	workspace := t.TempDir()
	verify := filepath.Join(workspace, "verify")
	runGit(t, workspace, "clone", bare, verify)
	got, err := os.ReadFile(filepath.Join(verify, "config.json"))
	if err != nil || string(got) != `{"k":1}` {
		t.Fatalf("pushed bytes = %q err %v", got, err)
	}
	// the stale directory was replaced by a real clone
	if _, err := os.Stat(filepath.Join(stale, ".git")); err != nil {
		t.Fatalf("clone dir has no .git after recovery: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stale, "partial.tmp")); err == nil {
		t.Fatalf("stale junk file still present — the directory was not cleared")
	}
}

// The stale-clone cleanup must never delete outside the clone root: a repo id
// whose sanitized form is "." or ".." is refused outright.
func TestGitee_UnsafeRepoIdRejected(t *testing.T) {
	g := &GiteeTarget{CloneRoot: t.TempDir()}
	// separator flattening makes any embedded ".." inert (e.g. "a/../.." becomes
	// "a_.._.." inside the clone root); only the exact forms are dangerous
	for _, repo := range []string{"..", "."} {
		err := g.UploadRepo(context.Background(), repo, "", map[string]string{"f": "/tmp/x"})
		if err == nil || !strings.Contains(err.Error(), "refusing unsafe repo id") {
			t.Fatalf("repo %q: err = %v, want unsafe-repo-id rejection", repo, err)
		}
	}
	// nothing outside the clone root was touched
	if _, err := os.Stat(g.cloneRoot()); err != nil {
		t.Fatalf("clone root disturbed: %v", err)
	}
}

// Regression test for the v0.3.0 clone-dir flattening: sanitizeRepo mapped the
// whole repo id to one name, so "o/a_b" and "o_a/b" shared a clone directory.
// The second upload reused the first repo's clone (origin = the first mirror)
// and pushed its bytes there, publishing one repo's files under another's
// mirror and reporting success.
func TestGitee_CloneDirSeparatesCollidingRepos(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	base := t.TempDir()
	const repoA, repoB = "o/a_b", "o_a/b"
	for _, repo := range []string{repoA, repoB} {
		bare := filepath.Join(base, repo+".git")
		if err := os.MkdirAll(filepath.Dir(bare), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		runGit(t, base, "init", "--bare", bare)
	}
	g := &GiteeTarget{Base: base, CloneRoot: filepath.Join(t.TempDir(), "clones")}
	fileA := writeFileT(t, filepath.Join(t.TempDir(), "a.txt"), "AAA")
	fileB := writeFileT(t, filepath.Join(t.TempDir(), "b.txt"), "BBB")

	if err := g.UploadRepo(context.Background(), repoA, "v1", map[string]string{"a.txt": fileA}); err != nil {
		t.Fatalf("UploadRepo %s: %v", repoA, err)
	}
	if err := g.UploadRepo(context.Background(), repoB, "v1", map[string]string{"b.txt": fileB}); err != nil {
		t.Fatalf("UploadRepo %s: %v", repoB, err)
	}

	// each mirror holds only its own repo's file
	cases := []struct {
		repo    string
		want    string
		content string
		absent  string
	}{
		{repoA, "a.txt", "AAA", "b.txt"},
		{repoB, "b.txt", "BBB", "a.txt"},
	}
	for _, c := range cases {
		workspace := t.TempDir()
		checkout := filepath.Join(workspace, "checkout")
		runGit(t, workspace, "clone", filepath.Join(base, c.repo+".git"), checkout)
		got, err := os.ReadFile(filepath.Join(checkout, c.want))
		if err != nil || string(got) != c.content {
			t.Fatalf("%s: %s = %q err %v", c.repo, c.want, got, err)
		}
		if _, err := os.Stat(filepath.Join(checkout, c.absent)); err == nil {
			t.Fatalf("%s received %s from the other repo — the clones collided", c.repo, c.absent)
		}
		// exactly one upload landed in this mirror
		count := strings.TrimSpace(runGit(t, workspace, "--git-dir", filepath.Join(base, c.repo+".git"), "rev-list", "--all", "--count"))
		if count != "1" {
			t.Fatalf("%s commits = %s, want 1", c.repo, count)
		}
	}
}
