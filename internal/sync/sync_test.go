package sync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SuperMarioYL/hflock/internal/lockfile"
	"github.com/SuperMarioYL/hflock/internal/mirror"
)

// fakeHF mimics the two HF endpoints hflock hits, keyed by
// "{repo}/resolve/{rev}/{file}" -> content (same shape as the verify tests').
func fakeHF(t *testing.T, files map[string]string) *mirror.HFSource {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/models/", func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/api/models/")
		idx := strings.Index(p, "/tree/")
		if idx < 0 {
			http.NotFound(w, r)
			return
		}
		repo, rev := p[:idx], p[idx+len("/tree/"):]
		prefix := repo + "/resolve/" + rev + "/"
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("["))
		first := true
		for k := range files {
			if strings.HasPrefix(k, prefix) {
				if !first {
					w.Write([]byte(","))
				}
				first = false
				w.Write([]byte(`{"type":"file","path":"` + strings.TrimPrefix(k, prefix) + `","size":` + itoa(len(files[k])) + `}`))
			}
		}
		w.Write([]byte("]"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if c, ok := files[strings.TrimPrefix(r.URL.Path, "/")]; ok {
			io.WriteString(w, c)
			return
		}
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &mirror.HFSource{Base: srv.URL, Client: srv.Client()}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// mockModelScope is the minimal ModelScope surface sync needs: repo exists,
// LFS blobs already present (batch returns no upload action), commit accepted.
func mockModelScope(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/models/"):
			io.WriteString(w, `{"Code":200,"Data":{}}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/info/lfs/objects/batch"):
			io.WriteString(w, `{"Code":200,"Data":{"objects":[]}}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/commit/master"):
			io.WriteString(w, `{"Code":200,"Data":{"CommitId":"c"}}`)
		default:
			t.Errorf("unexpected modelscope request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestSyncRun_MirrorsAndManifest(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	files := map[string]string{
		"o/r/resolve/main/config.json":             "C1",
		"o/r/resolve/main/model-00001.safetensors": "S1",
		"q/s/resolve/main/config.json":             "C2",
	}
	src := fakeHF(t, files)
	work := t.TempDir()
	giteeBase := t.TempDir() // local bare repos stand in for ai.gitee.com
	for _, repo := range []string{"o/r.git", "q/s.git"} {
		parts := strings.Split(repo, "/")
		if err := os.MkdirAll(filepath.Join(giteeBase, parts[0]), 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("git", "init", "--bare", filepath.Join(giteeBase, parts[0], parts[1]))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init --bare: %v: %s", err, out)
		}
	}
	targets := []mirror.Target{
		&mirror.GiteeTarget{Base: giteeBase, CloneRoot: filepath.Join(work, "gitee-ai")},
		&mirror.ModelScopeTarget{Base: mockModelScope(t), Token: "t"},
	}
	lock := &lockfile.WeightLock{Version: lockfile.Version, Weights: []lockfile.PinnedWeight{
		{Repo: "o/r", Revision: "main", Files: []string{"config.json", "model-00001.safetensors"}},
		{Repo: "q/s", Revision: "main", Files: []string{"config.json"}},
	}}
	out := filepath.Join(work, "manifest.json")

	r := Runner{Source: src, Targets: targets, WorkDir: work, Concurrency: 2}
	m, err := r.Run(context.Background(), lock, out)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(m.Entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(m.Entries))
	}
	byKey := map[string]lockfile.HashEntry{}
	for _, e := range m.Entries {
		byKey[e.Repo+"/"+e.File] = e
	}
	e := byKey["o/r/config.json"]
	if e.SHA256 != sha256Hex("C1") || e.Size != 2 {
		t.Fatalf("o/r config entry = %+v", e)
	}
	if len(e.Mirrors) != 2 || e.Mirrors[0] != "gitee-ai:o/r" || e.Mirrors[1] != "modelscope:o/r" {
		t.Fatalf("mirrors = %v", e.Mirrors)
	}
	if e := byKey["q/s/config.json"]; len(e.Mirrors) != 2 || e.Mirrors[0] != "gitee-ai:q/s" {
		t.Fatalf("q/s mirrors = %v", e.Mirrors)
	}

	// manifest file parses and carries the mirrors
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("manifest not written: %v", err)
	}
	var round lockfile.HashManifest
	if err := json.Unmarshal(b, &round); err != nil {
		t.Fatalf("manifest unmarshal: %v", err)
	}
	if len(round.Entries) != 3 {
		t.Fatalf("round-trip entries = %d", len(round.Entries))
	}

	// the gitee stand-in actually received the bytes
	workspace := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = workspace
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	run("clone", filepath.Join(giteeBase, "o", "r.git"), filepath.Join(workspace, "o-r"))
	got, err := os.ReadFile(filepath.Join(workspace, "o-r", "config.json"))
	if err != nil || string(got) != "C1" {
		t.Fatalf("gitee bytes = %q err %v", got, err)
	}
}

func TestSyncRun_ConcurrencySmoke(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < 5; i++ {
		files[fmt.Sprintf("o/r/resolve/main/f%d.bin", i)] = strings.Repeat(string(rune('a'+i)), 100+i)
	}
	src := fakeHF(t, files)
	work := t.TempDir()
	lock := &lockfile.WeightLock{Version: lockfile.Version, Weights: []lockfile.PinnedWeight{
		{Repo: "o/r", Revision: "main", Files: []string{"f0.bin", "f1.bin", "f2.bin", "f3.bin", "f4.bin"}},
	}}
	// no targets: upload phase is a no-op; the concurrent fetch is the subject
	r := Runner{Source: src, WorkDir: work, Concurrency: 2}
	m, err := r.Run(context.Background(), lock, filepath.Join(work, "manifest.json"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(m.Entries) != 5 {
		t.Fatalf("entries = %d, want 5", len(m.Entries))
	}
	for _, e := range m.Entries {
		if e.SHA256 != sha256Hex(files["o/r/resolve/main/"+e.File]) {
			t.Fatalf("hash mismatch for %s", e.File)
		}
		if e.Mirrors != nil {
			t.Fatalf("no targets configured — mirrors must be empty: %v", e.Mirrors)
		}
	}
}

func TestSyncRun_UploadFailureFails(t *testing.T) {
	files := map[string]string{"o/r/resolve/main/config.json": "C"}
	src := fakeHF(t, files)
	work := t.TempDir()
	lock := &lockfile.WeightLock{Version: lockfile.Version, Weights: []lockfile.PinnedWeight{
		{Repo: "o/r", Revision: "main", Files: []string{"config.json"}},
	}}
	// ModelScope without a token fails fast — the sync must surface it
	r := Runner{Source: src, WorkDir: work, Targets: []mirror.Target{&mirror.ModelScopeTarget{}}}
	_, err := r.Run(context.Background(), lock, filepath.Join(work, "manifest.json"))
	if err == nil || !strings.Contains(err.Error(), "upload o/r to modelscope") {
		t.Fatalf("err = %v, want upload failure naming repo and target", err)
	}
}

// recordingTarget captures the bytes a mirror would receive, per repo-relative
// path — the observable half of the cache-collision defect.
type recordingTarget struct {
	got map[string][]byte
}

func (r *recordingTarget) Name() string                { return "recording" }
func (r *recordingTarget) MirrorID(repo string) string { return "recording:" + repo }
func (r *recordingTarget) RepoURL(repo string) string  { return "recording:" + repo }
func (r *recordingTarget) UploadRepo(ctx context.Context, repo, revision string, files map[string]string) error {
	for rel, local := range files {
		b, err := os.ReadFile(local)
		if err != nil {
			return err
		}
		r.got[repo+"/"+rel] = b
	}
	return nil
}

// Regression test for the v0.2.0 cache-path collision: cachePath flattened
// "/" to "_", so "sub/model.bin" and "sub_model.bin" shared one cache file and
// the mirror received one file's bytes under the other's name while the
// manifest recorded both as verified. Each pin must now keep its own bytes.
func TestSyncRun_CachePathCollisionUploadsCorrectBytes(t *testing.T) {
	subContent := "SUBDIR-BYTES-0000000000000000000000000000"
	flatContent := "FLAT-BYTES-111111111111111111111111111111"
	files := map[string]string{
		"o/r/resolve/main/sub/model.bin": subContent,
		"o/r/resolve/main/sub_model.bin": flatContent,
	}
	src := fakeHF(t, files)
	work := t.TempDir()
	lock := &lockfile.WeightLock{Version: lockfile.Version, Weights: []lockfile.PinnedWeight{
		{Repo: "o/r", Revision: "main", Files: []string{"sub/model.bin", "sub_model.bin"}},
	}}
	rec := &recordingTarget{got: map[string][]byte{}}
	r := Runner{Source: src, Targets: []mirror.Target{rec}, WorkDir: work, Concurrency: 1}
	m, err := r.Run(context.Background(), lock, filepath.Join(work, "manifest.json"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	byFile := map[string]lockfile.HashEntry{}
	for _, e := range m.Entries {
		byFile[e.File] = e
	}
	if e := byFile["sub/model.bin"]; e.SHA256 != sha256Hex(subContent) {
		t.Fatalf("manifest sha for sub/model.bin = %s, want its own content's hash", e.SHA256)
	}
	if e := byFile["sub_model.bin"]; e.SHA256 != sha256Hex(flatContent) {
		t.Fatalf("manifest sha for sub_model.bin = %s, want its own content's hash", e.SHA256)
	}
	if got := string(rec.got["o/r/sub/model.bin"]); got != subContent {
		t.Fatalf("mirror received wrong bytes for sub/model.bin: %q", got)
	}
	if got := string(rec.got["o/r/sub_model.bin"]); got != flatContent {
		t.Fatalf("mirror received wrong bytes for sub_model.bin: %q", got)
	}
}

// Regression test for the v0.3.0 duplicate-identity defect: sync built one job
// per (weight, expanded file) with no dedupe, so a file covered by two pins was
// downloaded twice — concurrently when the limit allowed — into one cache path,
// and the manifest recorded the same physical file twice.
func TestSyncRun_DedupesSharedFileIdentity(t *testing.T) {
	var downloads atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/api/models/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/tree/") {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, `[{"type":"file","path":"config.json","size":5},`+
			`{"type":"file","path":"generation_config.json","size":5}]`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		io.WriteString(w, "BYTES")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// two pins over one repo@revision: the glob already covers config.json
	lock := &lockfile.WeightLock{Version: lockfile.Version, Weights: []lockfile.PinnedWeight{
		{Repo: "o/r", Revision: "main", Files: []string{"*.json"}},
		{Repo: "o/r", Revision: "main", Files: []string{"config.json"}},
	}}
	work := t.TempDir()
	r := Runner{Source: &mirror.HFSource{Base: srv.URL, Client: srv.Client()}, WorkDir: work, Concurrency: 2}
	m, err := r.Run(context.Background(), lock, filepath.Join(work, "manifest.json"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := downloads.Load(); n != 2 {
		t.Fatalf("downloads = %d, want 2 (one per physical file, not one per pin)", n)
	}
	keys := map[string]bool{}
	for _, e := range m.Entries {
		k := e.Repo + "@" + e.Revision + "/" + e.File
		if keys[k] {
			t.Fatalf("duplicate manifest entry for one physical file: %s", k)
		}
		keys[k] = true
	}
	if len(m.Entries) != 2 {
		t.Fatalf("entries = %d, want 2 unique files", len(m.Entries))
	}
}
