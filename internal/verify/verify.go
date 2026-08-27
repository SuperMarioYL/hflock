// Package verify downloads pinned weights from Hugging Face, recomputes each
// file's SHA256, and writes a weights.lock.manifest.json. This is the m1
// milestone: hash provenance is checkable before any mirror upload exists.
//
// m1 scope: source = Hugging Face, zero mirror uploads (HashEntry.Mirrors is
// left empty). m2 sync will populate Mirrors after uploading to Gitee AI +
// ModelScope.
package verify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SuperMarioYL/hflock/internal/lockfile"
	"github.com/SuperMarioYL/hflock/internal/mirror"
)

// Verifier resolves pinned weights via a mirror.Source, hashes each downloaded
// file, and emits a HashManifest.
type Verifier struct {
	Source  mirror.Source
	WorkDir string // download cache dir; default os.TempDir
}

// New returns a Verifier backed by the given source.
func New(s mirror.Source) *Verifier {
	return &Verifier{Source: s}
}

// Verify runs the full m1 flow over a lockfile and writes the manifest to
// outPath. It returns the populated manifest on success.
func (v *Verifier) Verify(ctx context.Context, lock *lockfile.WeightLock, outPath string) (*lockfile.HashManifest, error) {
	if lock == nil {
		return nil, fmt.Errorf("verify: nil lockfile")
	}
	if v.Source == nil {
		return nil, fmt.Errorf("verify: no source configured")
	}
	m := &lockfile.HashManifest{
		LockVersion: lock.Version,
		GeneratedAt: time.Now().UTC(),
	}
	for _, w := range lock.Weights {
		files, err := v.Source.ListFiles(ctx, w.Repo, w.Revision, w.Files)
		if err != nil {
			return nil, fmt.Errorf("list %s@%s: %w", w.Repo, w.Revision, err)
		}
		for _, f := range files {
			sum, size, err := v.hashFile(ctx, w.Repo, w.Revision, f)
			if err != nil {
				return nil, fmt.Errorf("verify %s@%s/%s: %w", w.Repo, w.Revision, f, err)
			}
			m.Entries = append(m.Entries, lockfile.HashEntry{
				Repo:     w.Repo,
				Revision: w.Revision,
				File:     f,
				SHA256:   sum,
				Size:     size,
				// Mirrors intentionally empty in m1; m2 sync populates after upload.
			})
		}
	}
	if err := lockfile.WriteManifest(outPath, m); err != nil {
		return nil, err
	}
	return m, nil
}

// hashFile downloads one file into a cache path while hashing the stream, so
// the manifest is built from bytes actually written — no second pass.
func (v *Verifier) hashFile(ctx context.Context, repo, revision, file string) (string, int64, error) {
	cachePath, err := v.cachePath(repo, revision, file)
	if err != nil {
		return "", 0, err
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return "", 0, fmt.Errorf("create cache dir: %w", err)
	}
	f, err := os.Create(cachePath)
	if err != nil {
		return "", 0, fmt.Errorf("create cache file %s: %w", cachePath, err)
	}
	h := sha256.New()
	mw := io.MultiWriter(f, h)
	n, err := v.Source.Download(ctx, repo, revision, file, mw)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// cachePath places each file under workdir/{repo}/{revision}/{file}, flattening
// slashes in repo/revision/file atoms so a pinned path can't escape the cache.
func (v *Verifier) cachePath(repo, revision, file string) (string, error) {
	dir := v.WorkDir
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, sanitize(repo), sanitize(revision), sanitize(file)), nil
}

// sanitize replaces path separators so a malicious pinned name can never
// traverse out of the cache root.
func sanitize(s string) string {
	return strings.NewReplacer(string(os.PathSeparator), "_", "/", "_", "\\", "_").Replace(s)
}
