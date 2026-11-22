package cli

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SuperMarioYL/hflock/internal/lockfile"
)

// fakeHF serves a single pinned file so the verify command can run offline.
func fakeHF(t *testing.T, body string) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/resolve/") {
			io.WriteString(w, body)
			return
		}
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestVerifyCmd_EndToEnd(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "weights.lock.yaml")
	if err := os.WriteFile(lockPath, []byte(`version: "0.1.0"
weights:
  - repo: deepseek-ai/DeepSeek-V3
    revision: v3.0
    files: ["config.json"]
`), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	work := t.TempDir()
	manifest := filepath.Join(work, "manifest.json")

	root := NewRootCmd()
	root.SetArgs([]string{
		"verify", lockPath,
		"--hf-base", fakeHF(t, "{\"k\":1}"),
		"--workdir", work,
		"--manifest", manifest,
	})
	if err := root.Execute(); err != nil {
		t.Fatalf("verify: %v", err)
	}
	b, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatalf("manifest not written: %v", err)
	}
	if !strings.Contains(string(b), "\"sha256\"") {
		t.Fatalf("manifest missing sha256: %s", b)
	}
}

// initFixture serves the three endpoints init touches: revision resolution,
// the tree listing, and resolve downloads.
func initFixture(t *testing.T, tree string) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/models/o/r/revision/main", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"sha":"abc123def456"}`)
	})
	// the tree is served for any revision: init enumerates at the revision it
	// pins (the resolved sha), not only at the ref the flag named
	mux.HandleFunc("/api/models/o/r/tree/", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, tree)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "{}") // resolve downloads
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

const initTree = `[{"type":"file","path":"config.json","size":2},
{"type":"file","path":"generation_config.json","size":3},
{"type":"file","path":"tokenizer.json","size":4},
{"type":"file","path":"model-00001.safetensors","size":5}]`

func TestInitCmd_DefaultMetadataPins(t *testing.T) {
	base := initFixture(t, initTree)
	out := filepath.Join(t.TempDir(), "weights.lock.yaml")

	root := NewRootCmd()
	root.SetArgs([]string{"init", "o/r", "--hf-base", base, "--out", out})
	if err := root.Execute(); err != nil {
		t.Fatalf("init: %v", err)
	}
	lock, err := lockfile.Load(out) // the generated lockfile must itself be valid
	if err != nil {
		t.Fatalf("generated lockfile does not Load: %v", err)
	}
	w := lock.Weights[0]
	if w.Revision != "abc123def456" {
		t.Fatalf("revision = %q, want the resolved commit sha", w.Revision)
	}
	want := []string{"config.json", "generation_config.json", "tokenizer.json"}
	if len(w.Files) != len(want) {
		t.Fatalf("files = %v, want the default metadata set", w.Files)
	}
	for i := range want {
		if w.Files[i] != want[i] {
			t.Fatalf("files = %v, want %v", w.Files, want)
		}
	}
	if w.Source != "huggingface" {
		t.Fatalf("source = %q", w.Source)
	}
}

func TestInitCmd_FilesAndAllFlags(t *testing.T) {
	base := initFixture(t, initTree)
	dir := t.TempDir()

	out := filepath.Join(dir, "explicit.yaml")
	root := NewRootCmd()
	root.SetArgs([]string{"init", "o/r", "--hf-base", base, "--out", out, "--files", "config.json, *.safetensors"})
	if err := root.Execute(); err != nil {
		t.Fatalf("init --files: %v", err)
	}
	lock, err := lockfile.Load(out)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(lock.Weights[0].Files) != 2 || lock.Weights[0].Files[0] != "config.json" || lock.Weights[0].Files[1] != "*.safetensors" {
		t.Fatalf("files = %v", lock.Weights[0].Files)
	}

	outAll := filepath.Join(dir, "all.yaml")
	root = NewRootCmd()
	root.SetArgs([]string{"init", "o/r", "--hf-base", base, "--out", outAll, "--all"})
	if err := root.Execute(); err != nil {
		t.Fatalf("init --all: %v", err)
	}
	lock, err = lockfile.Load(outAll)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(lock.Weights[0].Files) != 4 {
		t.Fatalf("--all files = %v, want every tree file", lock.Weights[0].Files)
	}
}

func TestListCmd_StatusTable(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "weights.lock.yaml")
	if err := os.WriteFile(lockPath, []byte(`version: "0.1.0"
weights:
  - repo: o/r
    revision: main
    files: ["config.json"]
  - repo: q/s
    revision: main
    files: ["config.json"]
`), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	manifest := filepath.Join(dir, "manifest.json")
	m := lockfile.HashManifest{LockVersion: lockfile.Version, Entries: []lockfile.HashEntry{
		{Repo: "o/r", Revision: "main", File: "config.json", SHA256: "abc", Size: 2,
			Mirrors: []string{"gitee-ai:o/r", "modelscope:o/r"}},
	}}
	if err := lockfile.WriteManifest(manifest, &m); err != nil {
		t.Fatal(err)
	}

	var buf strings.Builder
	root := NewRootCmd()
	root.SetOut(&buf)
	root.SetArgs([]string{"list", lockPath, "--manifest", manifest})
	if err := root.Execute(); err != nil {
		t.Fatalf("list: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"o/r", "main", "gitee-ai:o/r,modelscope:o/r", "verified"} {
		if !strings.Contains(out, want) {
			t.Fatalf("list output missing %q:\n%s", want, out)
		}
	}
	// q/s has no manifest entries: unverified with no mirrors
	qsLine := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "q/s") {
			qsLine = line
		}
	}
	if !strings.Contains(qsLine, "unverified") || strings.Contains(qsLine, "gitee") {
		t.Fatalf("q/s row = %q, want unverified without mirrors", qsLine)
	}
}

func TestListCmd_MissingManifestIsUnverified(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "weights.lock.yaml")
	if err := os.WriteFile(lockPath, []byte(`version: "0.1.0"
weights:
  - repo: o/r
    revision: main
    files: ["config.json"]
`), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	var buf strings.Builder
	root := NewRootCmd()
	root.SetOut(&buf)
	root.SetArgs([]string{"list", lockPath, "--manifest", filepath.Join(dir, "nope.json")})
	if err := root.Execute(); err != nil {
		t.Fatalf("list must not fail on a missing manifest: %v", err)
	}
	if !strings.Contains(buf.String(), "unverified") {
		t.Fatalf("output = %q", buf.String())
	}
}

func TestVerifyCmd_CheckGate(t *testing.T) {
	hf := fakeHF(t, "SAME-BYTES")
	work := t.TempDir()
	lockPath := filepath.Join(work, "weights.lock.yaml")
	if err := os.WriteFile(lockPath, []byte(`version: "0.1.0"
weights:
  - repo: o/r
    revision: main
    files: ["config.json"]
`), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	fresh := filepath.Join(work, "fresh.json")
	baseline := filepath.Join(work, "baseline.json")

	// a baseline that matches the fixture: check passes. The baseline must be a
	// distinct file: --check never reads back the file its own run writes.
	root := NewRootCmd()
	root.SetArgs([]string{"verify", lockPath, "--hf-base", hf, "--workdir", work, "--manifest", fresh})
	if err := root.Execute(); err != nil {
		t.Fatalf("verify: %v", err)
	}
	freshBytes, err := os.ReadFile(fresh)
	if err != nil {
		t.Fatalf("read fresh manifest: %v", err)
	}
	if err := os.WriteFile(baseline, freshBytes, 0o644); err != nil {
		t.Fatalf("write baseline: %v", err)
	}
	root = NewRootCmd()
	root.SetArgs([]string{"verify", lockPath, "--hf-base", hf, "--workdir", work,
		"--manifest", fresh, "--check", baseline})
	if err := root.Execute(); err != nil {
		t.Fatalf("check against an identical baseline must pass: %v", err)
	}

	// a tampered baseline: the gate must fail naming the file
	m, err := lockfile.ReadManifest(fresh)
	if err != nil {
		t.Fatal(err)
	}
	m.Entries[0].SHA256 = strings.Repeat("0", 64)
	if err := lockfile.WriteManifest(baseline, m); err != nil {
		t.Fatal(err)
	}
	root = NewRootCmd()
	root.SetArgs([]string{"verify", lockPath, "--hf-base", hf, "--workdir", work,
		"--manifest", fresh, "--check", baseline})
	err = root.Execute()
	if err == nil || !strings.Contains(err.Error(), "check failed") {
		t.Fatalf("err = %v, want check failure", err)
	}
	var buf strings.Builder
	root = NewRootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"verify", lockPath, "--hf-base", hf, "--workdir", work,
		"--manifest", fresh, "--check", baseline})
	_ = root.Execute()
	if !strings.Contains(buf.String(), "hash mismatch") || !strings.Contains(buf.String(), "o/r@main/config.json") {
		t.Fatalf("diff detail missing:\n%s", buf.String())
	}

	// a baseline that omits a pinned file: the gate must fail naming it
	m2, _ := lockfile.ReadManifest(fresh)
	m2.Entries = nil
	if err := lockfile.WriteManifest(baseline, m2); err != nil {
		t.Fatal(err)
	}
	root = NewRootCmd()
	root.SetArgs([]string{"verify", lockPath, "--hf-base", hf, "--workdir", work,
		"--manifest", fresh, "--check", baseline})
	err = root.Execute()
	if err == nil || !strings.Contains(err.Error(), "check failed") {
		t.Fatalf("err = %v, want check failure on missing baseline entry", err)
	}
	var buf2 strings.Builder
	root = NewRootCmd()
	root.SetOut(&buf2)
	root.SetErr(&buf2)
	root.SetArgs([]string{"verify", lockPath, "--hf-base", hf, "--workdir", work,
		"--manifest", fresh, "--check", baseline})
	_ = root.Execute()
	if !strings.Contains(buf2.String(), "unexpected o/r@main/config.json") {
		t.Fatalf("missing-baseline detail wrong:\n%s", buf2.String())
	}
}

// Regression test for the v0.1.0 defect where `hflock verify` exited 0 and
// wrote a 0-entry manifest when a pinned glob matched nothing — the CI
// air-gap gate verified nothing and still passed.
func TestVerifyCmd_GlobNoMatchFails(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/tree/") {
			// repo contains only config.json — nothing matches *.safetensors
			io.WriteString(w, `[{"type":"file","path":"config.json","size":2}]`)
			return
		}
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	work := t.TempDir()
	lockPath := filepath.Join(work, "weights.lock.yaml")
	if err := os.WriteFile(lockPath, []byte(`version: "0.1.0"
weights:
  - repo: o/r
    revision: main
    files: ["*.safetensors"]
`), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	manifest := filepath.Join(work, "manifest.json")

	root := NewRootCmd()
	root.SetArgs([]string{"verify", lockPath, "--hf-base", srv.URL, "--workdir", work, "--manifest", manifest})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "matched no files") {
		t.Fatalf("err = %v, want 'matched no files'", err)
	}
	if _, statErr := os.Stat(manifest); statErr == nil {
		t.Fatalf("no manifest must be written on a failed verify")
	}
}

// sync end-to-end through the real CLI: fixture HF source + mock ModelScope
// target (small non-LFS pin so the flow is ensure-repo + commit).
func TestSyncCmd_EndToEnd(t *testing.T) {
	hf := fakeHF(t, `{"k":1}`) // serves any /resolve/ path with this body
	ms := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/models/"):
			io.WriteString(w, `{"Code":200,"Data":{}}`)
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/commit/"):
			io.WriteString(w, `{"Code":200,"Data":{"CommitId":"c"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ms.Close)

	work := t.TempDir()
	lockPath := filepath.Join(work, "weights.lock.yaml")
	if err := os.WriteFile(lockPath, []byte(`version: "0.1.0"
weights:
  - repo: o/r
    revision: main
    files: ["config.json"]
`), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	manifest := filepath.Join(work, "manifest.json")

	root := NewRootCmd()
	root.SetArgs([]string{
		"sync", lockPath,
		"--hf-base", hf,
		"--workdir", work,
		"--manifest", manifest,
		"--mirrors", "modelscope",
		"--modelscope-base", ms.URL,
		"--modelscope-token", "t",
	})
	if err := root.Execute(); err != nil {
		t.Fatalf("sync: %v", err)
	}
	b, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatalf("manifest not written: %v", err)
	}
	if !strings.Contains(string(b), `"modelscope:o/r"`) {
		t.Fatalf("manifest missing mirror record:\n%s", b)
	}
}

func TestSyncCmd_UnknownMirrorRejected(t *testing.T) {
	root := NewRootCmd()
	root.SetArgs([]string{"sync", "whatever.yaml", "--mirrors", "baidu-pan"})
	err := root.Execute()
	// the unknown-mirror error must surface before the lockfile is even read
	if err == nil || !strings.Contains(err.Error(), "unknown mirror") {
		t.Fatalf("err = %v, want unknown-mirror error", err)
	}
}

// Regression test for the v0.2.0 defect where list aggregated status by repo
// alone: a manifest still holding the old revision's entries marked a new
// revision verified, and two weights pinning different revisions of one repo
// merged their hash counts and mirrors.
func TestListCmd_StatusKeyedByRevision(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "weights.lock.yaml")
	if err := os.WriteFile(lockPath, []byte(`version: "0.1.0"
weights:
  - repo: o/r
    revision: v1
    files: ["config.json"]
  - repo: o/r
    revision: v2
    files: ["config.json"]
`), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	// the manifest only covers the OLD revision v1
	manifest := filepath.Join(dir, "manifest.json")
	m := lockfile.HashManifest{LockVersion: lockfile.Version, Entries: []lockfile.HashEntry{
		{Repo: "o/r", Revision: "v1", File: "config.json", SHA256: "abc", Size: 2,
			Mirrors: []string{"gitee-ai:o/r"}},
	}}
	if err := lockfile.WriteManifest(manifest, &m); err != nil {
		t.Fatal(err)
	}

	var buf strings.Builder
	root := NewRootCmd()
	root.SetOut(&buf)
	root.SetArgs([]string{"list", lockPath, "--manifest", manifest})
	if err := root.Execute(); err != nil {
		t.Fatalf("list: %v", err)
	}
	var v1Line, v2Line string
	for _, line := range strings.Split(buf.String(), "\n") {
		// tabwriter pads columns with spaces, so match on content, not tabs
		switch {
		case strings.Contains(line, "o/r") && strings.Contains(line, " v1 "):
			v1Line = line
		case strings.Contains(line, "o/r") && strings.Contains(line, " v2 "):
			v2Line = line
		}
	}
	if !strings.Contains(v1Line, "gitee-ai:o/r") || strings.Contains(v1Line, "unverified") {
		t.Fatalf("v1 row = %q, want verified with mirrors", v1Line)
	}
	if !strings.Contains(v2Line, "unverified") || strings.Contains(v2Line, "gitee-ai") {
		t.Fatalf("v2 row = %q, want unverified without merged status from v1", v2Line)
	}
}

// Regression test for the v0.2.0 behavior where a git-less machine got the
// gitee-ai "git not found" error only after the full (possibly multi-GB)
// download: buildTargets must reject the gitee-ai target up front.
func TestBuildTargets_GiteeRequiresGitUpFront(t *testing.T) {
	t.Setenv("PATH", "")
	_, err := buildTargets("gitee-ai,modelscope", "", "", "", "", "", "")
	if err == nil || !strings.Contains(err.Error(), "git not found") {
		t.Fatalf("err = %v, want the gitee-ai git-not-found error before any download", err)
	}
	// a PATH with git still builds the target
	t.Setenv("PATH", os.Getenv("HOME")+"/bin:"+"/usr/bin:/bin:/usr/sbin:/sbin")
	if _, err := exec.LookPath("git"); err == nil {
		targets, err := buildTargets("gitee-ai", "", "", "", "", "", "")
		if err != nil {
			t.Fatalf("buildTargets with git available: %v", err)
		}
		if len(targets) != 1 || targets[0].Name() != "gitee-ai" {
			t.Fatalf("targets = %v", targets)
		}
	}
}

// Regression test for the v0.3.0 gate defect: --manifest defaults to
// weights.lock.manifest.json and verify wrote the fresh manifest there before
// reading --check, so the documented invocation
// (`verify weights.lock.yaml --check weights.lock.manifest.json`) diffs the
// fresh manifest against itself — the gate always passed — and the committed
// baseline was overwritten by untrusted bytes.
func TestVerifyCmd_CheckBaselineNotClobbered(t *testing.T) {
	hf := fakeHF(t, "FRESH-BYTES")
	dir := t.TempDir()
	// the default --manifest path is resolved against the working directory,
	// exactly as it is for an operator running from the repo root
	t.Chdir(dir)

	lockPath := filepath.Join(dir, "weights.lock.yaml")
	if err := os.WriteFile(lockPath, []byte(`version: "0.1.0"
weights:
  - repo: o/r
    revision: main
    files: ["config.json"]
`), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	// a tampered baseline living at the documented --check path
	baselinePath := filepath.Join(dir, "weights.lock.manifest.json")
	tampered := lockfile.HashManifest{LockVersion: lockfile.Version, Entries: []lockfile.HashEntry{
		{Repo: "o/r", Revision: "main", File: "config.json", SHA256: strings.Repeat("0", 64), Size: 11},
	}}
	if err := lockfile.WriteManifest(baselinePath, &tampered); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(baselinePath)
	if err != nil {
		t.Fatal(err)
	}

	root := NewRootCmd()
	root.SetArgs([]string{
		"verify", lockPath,
		"--hf-base", hf,
		"--workdir", filepath.Join(dir, "cache"),
		"--check", "weights.lock.manifest.json",
	})
	err = root.Execute()
	if err == nil || !strings.Contains(err.Error(), "same file") {
		t.Fatalf("err = %v, want a --check/--manifest collision error", err)
	}
	after, readErr := os.ReadFile(baselinePath)
	if readErr != nil {
		t.Fatalf("baseline unreadable after the run: %v", readErr)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("the trusted baseline was overwritten by the fresh manifest")
	}
}

// Regression test for the v0.3.0 init gap: --files stored patterns verbatim,
// so a typo'd glob was committed as a lockfile that only failed in the next
// verify/sync run with "matched no files".
func TestInitCmd_RejectsUnmatchedPins(t *testing.T) {
	base := initFixture(t, `[{"type":"file","path":"config.json","size":2}]`)
	out := filepath.Join(t.TempDir(), "weights.lock.yaml")

	root := NewRootCmd()
	root.SetArgs([]string{"init", "o/r", "--hf-base", base, "--out", out, "--files", "*.safetensors"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "matched no files") {
		t.Fatalf("err = %v, want 'matched no files'", err)
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Fatalf("init wrote a lockfile pinning nothing")
	}
}
