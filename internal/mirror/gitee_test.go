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
