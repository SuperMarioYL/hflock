// Package verify downloads pinned weights from Hugging Face, recomputes each
// file's SHA256, and writes a weights.lock.manifest.json. The manifest is the
// m1 hash-provenance record; m2 sync reuses Fetch (with resume) to build the
// mirror manifest, and --check (m3) diffs a fresh run against a trusted
// baseline manifest for the CI air-gap gate.
package verify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

// Result is one fetched file's provenance: the sha256 of the bytes written,
// the byte count, and the cache path those bytes live at.
type Result struct {
	SHA256 string
	Size   int64
	Path   string
}

// Verify runs the full m1 flow over a lockfile and writes the manifest to
// outPath. It returns the populated manifest on success. Every file is
// downloaded fresh (no resume) — the verify trust model is re-download and
// re-hash on every run.
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
			res, err := Fetch(ctx, v.Source, v.WorkDir, w.Repo, w.Revision, f, false)
			if err != nil {
				return nil, fmt.Errorf("verify %s@%s/%s: %w", w.Repo, w.Revision, f, err)
			}
			m.Entries = append(m.Entries, lockfile.HashEntry{
				Repo:     w.Repo,
				Revision: w.Revision,
				File:     f,
				SHA256:   res.SHA256,
				Size:     res.Size,
				// Mirrors intentionally empty here; m2 sync populates the
				// manifest it emits with the upload targets.
			})
		}
	}
	if err := lockfile.WriteManifest(outPath, m); err != nil {
		return nil, err
	}
	return m, nil
}

// Fetch downloads one file into the workdir cache while hashing the stream,
// so the recorded hash always reflects the bytes actually written to disk.
//
// With resume == false the cache file is truncated and the file downloaded
// whole. With resume == true a partial cache file is completed with a Range
// request: 206 appends the remainder, a 200 (server ignored the range)
// restarts from zero, and a 416 whose advertised total equals the cached
// size means the file is already complete on disk and is hashed locally
// without downloading anything.
func Fetch(ctx context.Context, src mirror.Source, workDir, repo, revision, file string, resume bool) (Result, error) {
	if src == nil {
		return Result{}, fmt.Errorf("fetch: no source configured")
	}
	cachePath, err := cachePath(workDir, repo, revision, file)
	if err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return Result{}, fmt.Errorf("create cache dir: %w", err)
	}

	if resume {
		if info, statErr := os.Stat(cachePath); statErr == nil && info.Size() > 0 {
			res, done, err := resumeFrom(ctx, src, cachePath, info.Size(), repo, revision, file)
			if err != nil {
				return Result{}, err
			}
			if done {
				return res, nil
			}
			// done == false: the partial append was unusable (server
			// ignored the range or the cache is stale) — fall through to a
			// fresh fetch, which truncates the cache.
		}
	}

	f, err := os.Create(cachePath)
	if err != nil {
		return Result{}, fmt.Errorf("create cache file %s: %w", cachePath, err)
	}
	h := sha256.New()
	start, n, err := src.Download(ctx, repo, revision, file, 0, io.MultiWriter(f, h))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Result{}, err
	}
	if start != 0 {
		return Result{}, fmt.Errorf("fetch %s: server returned offset %d for a full request", file, start)
	}
	return Result{SHA256: hex.EncodeToString(h.Sum(nil)), Size: n, Path: cachePath}, nil
}

// resumeFrom continues a partially cached download. done == false (with a nil
// error) tells the caller the attempt was unusable and a full restart is
// required; the corrupted partial append is left for the caller's truncating
// re-create.
func resumeFrom(ctx context.Context, src mirror.Source, cachePath string, cached int64, repo, revision, file string) (res Result, done bool, err error) {
	f, err := os.OpenFile(cachePath, os.O_RDWR, 0o644)
	if err != nil {
		return Result{}, false, fmt.Errorf("open cache file %s: %w", cachePath, err)
	}
	defer f.Close()
	h := sha256.New()
	// Hash the bytes already on disk so the manifest reflects exactly the
	// bytes the mirror will upload, not an assumed prefix.
	if _, err := io.Copy(h, io.NewSectionReader(f, 0, cached)); err != nil {
		return Result{}, false, err
	}
	if _, err := f.Seek(cached, io.SeekStart); err != nil {
		return Result{}, false, err
	}
	start, n, derr := src.Download(ctx, repo, revision, file, cached, io.MultiWriter(f, h))
	sum := hex.EncodeToString(h.Sum(nil))
	var ru *mirror.RangeUnsatisfiable
	switch {
	case derr == nil && start == cached:
		// range honored: appended at the right offset
		return Result{SHA256: sum, Size: cached + n, Path: cachePath}, true, nil
	case derr == nil:
		// server ignored the range (or returned an unexpected offset): the
		// appended bytes are unusable — restart from zero
		return Result{}, false, nil
	case errors.As(derr, &ru):
		if ru.HasTotal && ru.Total == cached {
			// the cached file is already the complete file
			return Result{SHA256: sum, Size: cached, Path: cachePath}, true, nil
		}
		return Result{}, false, nil
	default:
		return Result{}, false, derr
	}
}

// cachePath places each file under workdir/{repo}/{revision}/{file}, flattening
// slashes in repo/revision/file atoms so a pinned path can't escape the cache.
func cachePath(workDir, repo, revision, file string) (string, error) {
	dir := workDir
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
