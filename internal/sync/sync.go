// Package sync implements the m2 mirror flow: read the lockfile, download the
// pinned weights from the source (concurrently, with resume), upload them to
// the configured CN mirror targets, and emit the hash manifest with each
// entry's Mirrors populated.
package sync

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/SuperMarioYL/hflock/internal/lockfile"
	"github.com/SuperMarioYL/hflock/internal/mirror"
	"github.com/SuperMarioYL/hflock/internal/verify"
)

// Runner executes one sync run over a lockfile.
type Runner struct {
	Source      mirror.Source
	Targets     []mirror.Target
	WorkDir     string // download cache dir; default os.TempDir
	Concurrency int    // parallel downloads; default 4
}

// fileKey is one physical pinned file. The cache path, the manifest key and the
// --check diff all key on this triple, so downloads and manifest rows must too:
// two pins that cover the same file are one physical record, not two.
type fileKey struct {
	repo     string
	revision string
	file     string
}

// Run mirrors the lockfile's pinned weights and writes the manifest to
// outPath, returning it on success. Every file is downloaded into the cache
// (resuming any partial previous download), each weight's files are uploaded
// to every target as one batch, and the manifest records each file once with
// the mirrors that hold it.
func (r *Runner) Run(ctx context.Context, lock *lockfile.WeightLock, outPath string) (*lockfile.HashManifest, error) {
	if lock == nil {
		return nil, fmt.Errorf("sync: nil lockfile")
	}
	if r.Source == nil {
		return nil, fmt.Errorf("sync: no source configured")
	}

	// Expand every pin to its concrete file list (globs included; a pattern
	// matching nothing is an error, same contract as verify), then dedupe the
	// download set by physical identity. Without the dedupe, a file covered by
	// two pins would be fetched twice — concurrently, into the same cache file.
	var jobs []fileKey
	filesByWeight := make([][]string, len(lock.Weights))
	queued := make(map[fileKey]bool)
	for i, w := range lock.Weights {
		files, err := r.Source.ListFiles(ctx, w.Repo, w.Revision, w.Files)
		if err != nil {
			return nil, fmt.Errorf("list %s@%s: %w", w.Repo, w.Revision, err)
		}
		filesByWeight[i] = files
		for _, f := range files {
			k := fileKey{repo: w.Repo, revision: w.Revision, file: f}
			if queued[k] {
				continue
			}
			queued[k] = true
			jobs = append(jobs, k)
		}
	}

	// Concurrent, resumable downloads into the cache. On the first failure
	// the context is cancelled so queued downloads stop early.
	results := make([]verify.Result, len(jobs))
	limit := r.Concurrency
	if limit <= 0 {
		limit = 4
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	fail := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if firstErr == nil {
			firstErr = err
			cancel()
		}
	}
	for i, k := range jobs {
		wg.Add(1)
		go func(i int, k fileKey) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-runCtx.Done():
				return
			}
			defer func() { <-sem }()
			res, err := verify.Fetch(runCtx, r.Source, r.WorkDir, k.repo, k.revision, k.file, true)
			if err != nil {
				fail(fmt.Errorf("fetch %s@%s/%s: %w", k.repo, k.revision, k.file, err))
				return
			}
			results[i] = res
		}(i, k)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}

	// Upload each weight's files to every target as one batch. The same
	// physical file can be part of more than one weight's batch; the mirror is
	// idempotent, and the manifest row records the mirrors once.
	resultByFile := make(map[fileKey]verify.Result, len(jobs))
	for i, k := range jobs {
		resultByFile[k] = results[i]
	}
	mirrorsFor := make(map[fileKey][]string, len(jobs))
	for i, w := range lock.Weights {
		if len(filesByWeight[i]) == 0 {
			continue
		}
		cachePaths := make(map[string]string, len(filesByWeight[i]))
		for _, f := range filesByWeight[i] {
			k := fileKey{repo: w.Repo, revision: w.Revision, file: f}
			cachePaths[f] = resultByFile[k].Path
		}
		var ids []string
		for _, t := range r.Targets {
			if err := t.UploadRepo(ctx, w.Repo, w.Revision, cachePaths); err != nil {
				return nil, fmt.Errorf("upload %s to %s: %w", w.Repo, t.Name(), err)
			}
			ids = append(ids, t.MirrorID(w.Repo))
		}
		// every weight is uploaded to the same target set, so the first weight
		// that covers a file supplies its mirror list
		for _, f := range filesByWeight[i] {
			k := fileKey{repo: w.Repo, revision: w.Revision, file: f}
			if _, ok := mirrorsFor[k]; !ok {
				mirrorsFor[k] = ids
			}
		}
	}

	m := &lockfile.HashManifest{
		LockVersion: lock.Version,
		GeneratedAt: time.Now().UTC(),
	}
	for _, k := range jobs {
		res := resultByFile[k]
		m.Entries = append(m.Entries, lockfile.HashEntry{
			Repo:     k.repo,
			Revision: k.revision,
			File:     k.file,
			SHA256:   res.SHA256,
			Size:     res.Size,
			Mirrors:  mirrorsFor[k],
		})
	}
	if err := lockfile.WriteManifest(outPath, m); err != nil {
		return nil, err
	}
	return m, nil
}
