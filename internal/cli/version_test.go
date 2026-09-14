package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SuperMarioYL/hflock/internal/lockfile"
)

// TestVersionLockstep pins the single-source-of-truth rule for the hflock
// tool version: the cli const, the VERSION file, the CHANGELOG's latest
// section, and the --version output must all agree. A bump that touches only
// one surface re-fails this test.
func TestVersionLockstep(t *testing.T) {
	// the VERSION file at the repo root
	b, err := os.ReadFile(filepath.Join("..", "..", "VERSION"))
	if err != nil {
		t.Fatalf("read VERSION: %v", err)
	}
	fileVersion := strings.TrimSpace(string(b))
	if fileVersion != Version {
		t.Fatalf("VERSION file = %q, cli Version const = %q — bump them in lockstep", fileVersion, Version)
	}

	// the CHANGELOG documents this version with a link reference
	changelog, err := os.ReadFile(filepath.Join("..", "..", "CHANGELOG.md"))
	if err != nil {
		t.Fatalf("read CHANGELOG.md: %v", err)
	}
	heading := "## [" + Version + "]"
	if !strings.Contains(string(changelog), heading) {
		t.Fatalf("CHANGELOG.md has no %q section", heading)
	}
	linkRef := "[" + Version + "]: https://github.com/SuperMarioYL/hflock/releases/tag/v" + Version
	if !strings.Contains(string(changelog), linkRef) {
		t.Fatalf("CHANGELOG.md missing the %s link reference", Version)
	}

	// --version prints the same version
	var buf strings.Builder
	root := NewRootCmd()
	root.SetOut(&buf)
	root.SetArgs([]string{"--version"})
	if err := root.Execute(); err != nil {
		t.Fatalf("--version: %v", err)
	}
	if got := strings.TrimSpace(buf.String()); got != "hflock "+Version {
		t.Fatalf("--version output = %q, want %q", got, "hflock "+Version)
	}
}

// The lockfile schema version is a data contract, not a tool-version surface:
// it must stay stable so existing lockfiles keep parsing, and the schema must
// accept the 0.1.0 lockfiles already in the wild.
func TestLockfileSchemaIsStableContract(t *testing.T) {
	if lockfile.Version != "0.1.0" {
		t.Fatalf("lockfile schema version = %q — changing it rejects every existing lockfile; make it a conscious migration instead", lockfile.Version)
	}
	path := filepath.Join(t.TempDir(), "weights.lock.yaml")
	if err := os.WriteFile(path, []byte(`version: "0.1.0"
weights:
  - repo: o/r
    revision: main
    files: ["config.json"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := lockfile.Load(path); err != nil {
		t.Fatalf("v0.1.0-schema lockfile must still Load: %v", err)
	}
}
