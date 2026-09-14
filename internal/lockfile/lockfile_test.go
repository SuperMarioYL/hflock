package lockfile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestLoad_ValidYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "weights.lock.yaml")
	mustWrite(t, path, `version: "0.1.0"
weights:
  - repo: deepseek-ai/DeepSeek-V3
    revision: v3.0
    files: ["config.json", "*.safetensors"]
    source: huggingface
  - repo: Qwen/Qwen3-235B-A22B
    revision: main
    files: ["config.json"]
`)
	lock, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if lock.Version != Version {
		t.Fatalf("version = %q, want %q", lock.Version, Version)
	}
	if len(lock.Weights) != 2 {
		t.Fatalf("weights = %d, want 2", len(lock.Weights))
	}
	if lock.Weights[0].Repo != "deepseek-ai/DeepSeek-V3" {
		t.Fatalf("repo0 = %q", lock.Weights[0].Repo)
	}
	if lock.Weights[0].Files[1] != "*.safetensors" {
		t.Fatalf("file1 = %q", lock.Weights[0].Files[1])
	}
}

func TestLoad_SourceDefaultsToHuggingFace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "weights.lock.yaml")
	mustWrite(t, path, `version: "0.1.0"
weights:
  - repo: deepseek-ai/DeepSeek-V3
    revision: v3.0
    files: ["config.json"]
`)
	lock, err := Load(path)
	if err != nil {
		t.Fatalf("Load with empty source: %v", err)
	}
	if lock.Weights[0].Source != "" {
		t.Fatalf("source = %q, want empty (defaulted at validate)", lock.Weights[0].Source)
	}
}

func TestLoad_Errors(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"missing file":    filepath.Join(dir, "nope.yaml"),
		"bad yaml":        "version: 0.1.0\nweights: [oops\n",
		"missing version": "weights: []\n",
		"wrong version":   "version: \"9.9.9\"\nweights: []\n",
		"no weights":      "version: \"0.1.0\"\nweights: []\n",
		"empty repo":      "version: \"0.1.0\"\nweights:\n  - revision: v\n    files: [a]\n",
		"empty revision":  "version: \"0.1.0\"\nweights:\n  - repo: a/b\n    files: [a]\n",
		"empty files":     "version: \"0.1.0\"\nweights:\n  - repo: a/b\n    revision: v\n",
		"bad source":      "version: \"0.1.0\"\nweights:\n  - repo: a/b\n    revision: v\n    files: [a]\n    source: s3\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(name, " ", "_")+".yaml")
			if name != "missing file" {
				mustWrite(t, path, body)
			}
			if _, err := Load(path); err == nil {
				t.Fatalf("expected error, got nil")
			}
		})
	}
}

func TestWriteManifest_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	m := &HashManifest{
		LockVersion: Version,
		GeneratedAt: time.Date(2026, 8, 27, 16, 0, 0, 0, time.UTC),
		Entries: []HashEntry{{
			Repo: "deepseek-ai/DeepSeek-V3", Revision: "v3.0",
			File: "config.json", SHA256: "abc123", Size: 42,
		}},
	}
	if err := WriteManifest(path, m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got HashManifest
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.LockVersion != Version {
		t.Fatalf("lock_version = %q", got.LockVersion)
	}
	if len(got.Entries) != 1 || got.Entries[0].SHA256 != "abc123" {
		t.Fatalf("entries = %+v", got.Entries)
	}
	if got.Entries[0].Mirrors != nil {
		t.Fatalf("mirrors = %v, want nil (m1)", got.Entries[0].Mirrors)
	}
	if !strings.HasSuffix(string(b), "\n") {
		t.Fatalf("manifest not newline-terminated")
	}
}
