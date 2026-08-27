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
	for _, c := range []string{"sync", "init", "list"} {
		root := NewRootCmd()
		root.SetArgs([]string{c})
		if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "ships in milestone") {
			t.Fatalf("%s: err = %v, want ships-in-milestone", c, err)
		}
	}
}
