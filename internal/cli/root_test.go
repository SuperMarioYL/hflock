package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestStubCommands(t *testing.T) {
	for _, c := range []string{"init", "list"} {
		root := NewRootCmd()
		root.SetArgs([]string{c})
		if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "ships in milestone") {
			t.Fatalf("%s: err = %v, want ships-in-milestone", c, err)
		}
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
