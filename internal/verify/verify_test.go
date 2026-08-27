package verify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SuperMarioYL/hflock/internal/lockfile"
	"github.com/SuperMarioYL/hflock/internal/mirror"
)

// fakeHF mimics the two HF endpoints hflock hits, keyed by
// "{repo}/resolve/{rev}/{file}" -> content. It serves both the file-tree API
// and the resolve download so a Verifier can run fully offline.
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
				name := strings.TrimPrefix(k, prefix)
				w.Write([]byte(`{"type":"file","path":"` + name + `","size":` + itoa(len(files[k])) + `}`))
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

func sha256Hex(t *testing.T, body string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func TestVerify_HappyPath(t *testing.T) {
	cfg := "{\"vocab\":1}"
	tok := "tokenizer"
	files := map[string]string{
		"deepseek-ai/DeepSeek-V3/resolve/v3.0/config.json": cfg,
		"Qwen/Qwen3-235B-A22B/resolve/main/config.json":      cfg,
		"Qwen/Qwen3-235B-A22B/resolve/main/tokenizer.json":   tok,
	}
	src := fakeHF(t, files)
	lock := &lockfile.WeightLock{Version: lockfile.Version, Weights: []lockfile.PinnedWeight{
		{Repo: "deepseek-ai/DeepSeek-V3", Revision: "v3.0", Files: []string{"config.json"}},
		{Repo: "Qwen/Qwen3-235B-A22B", Revision: "main", Files: []string{"config.json", "tokenizer.json"}},
	}}
	out := filepath.Join(t.TempDir(), "manifest.json")
	v := New(src)
	v.WorkDir = t.TempDir()

	m, err := v.Verify(context.Background(), lock, out)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(m.Entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(m.Entries))
	}
	// build a lookup by file for stable assertions
	got := map[string]lockfile.HashEntry{}
	for _, e := range m.Entries {
		got[e.Repo+"/"+e.File] = e
	}
	if e := got["deepseek-ai/DeepSeek-V3/config.json"]; e.SHA256 != sha256Hex(t, cfg) || e.Size != int64(len(cfg)) {
		t.Fatalf("deepseek config entry = %+v", e)
	}
	if e := got["Qwen/Qwen3-235B-A22B/tokenizer.json"]; e.SHA256 != sha256Hex(t, tok) {
		t.Fatalf("qwen tokenizer entry = %+v", e)
	}
	for _, e := range m.Entries {
		if e.Mirrors != nil {
			t.Fatalf("m1 must leave mirrors empty; got %v for %s", e.Mirrors, e.File)
		}
	}
	if m.LockVersion != lockfile.Version {
		t.Fatalf("lock version = %q", m.LockVersion)
	}
	if m.GeneratedAt.IsZero() {
		t.Fatalf("generated_at zero")
	}
}

func TestVerify_GlobExpanded(t *testing.T) {
	files := map[string]string{
		"deepseek-ai/DeepSeek-V3/resolve/v3.0/config.json":             "C",
		"deepseek-ai/DeepSeek-V3/resolve/v3.0/tokenizer.json":          "T",
		"deepseek-ai/DeepSeek-V3/resolve/v3.0/model-00001.safetensors": "S",
	}
	src := fakeHF(t, files)
	lock := &lockfile.WeightLock{Version: lockfile.Version, Weights: []lockfile.PinnedWeight{
		{Repo: "deepseek-ai/DeepSeek-V3", Revision: "v3.0", Files: []string{"*.json"}},
	}}
	out := filepath.Join(t.TempDir(), "manifest.json")
	v := New(src)
	v.WorkDir = t.TempDir()

	m, err := v.Verify(context.Background(), lock, out)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(m.Entries) != 2 {
		t.Fatalf("entries = %d, want 2 (only *.json)", len(m.Entries))
	}
	for _, e := range m.Entries {
		if !strings.HasSuffix(e.File, ".json") {
			t.Fatalf("non-json leaked through glob: %q", e.File)
		}
		if e.SHA256 != sha256Hex(t, files["deepseek-ai/DeepSeek-V3/resolve/v3.0/"+e.File]) {
			t.Fatalf("hash mismatch for %q", e.File)
		}
	}
}

func TestVerify_NilLockAndSource(t *testing.T) {
	v := New(nil)
	if _, err := v.Verify(context.Background(), nil, "out.json"); err == nil {
		t.Fatal("expected error on nil lock")
	}
	v2 := New(mirror.NewHFSource())
	if _, err := v2.Verify(context.Background(), nil, "out.json"); err == nil {
		t.Fatal("expected error on nil lock")
	}
}
