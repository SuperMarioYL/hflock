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

type syncJob struct {
	weightIdx int
	file      string
}

// Run mirrors the lockfile's pinned weights and writes the manifest to
// outPath, returning it on success. Every file is downloaded into the cache
// (resuming any partial previous download), each weight's files are uploaded
// to every target as one batch, and the manifest records each entry's
// mirrors.
func (r *Runner) Run(ctx context.Context, lock *lockfile.WeightLock, outPath string) (*lockfile.HashManifest, error) {
	if lock == nil {
		return nil, fmt.Errorf("sync: nil lockfile")
	}
	if r.Source == nil {
		return nil, fmt.Errorf("sync: no source configured")
	}

	// Expand every pin to its concrete file list (globs included; a pattern
	// matching nothing is an error, same contract as verify).
	var jobs []syncJob
	filesByWeight := make([][]string, len(lock.Weights))
	for i, w := range lock.Weights {
		files, err := r.Source.ListFiles(ctx, w.Repo, w.Revision, w.Files)
		if err != nil {
			return nil, fmt.Errorf("list %s@%s: %w", w.Repo, w.Revision, err)
		}
		filesByWeight[i] = files
		for _, f := range files {
			jobs = append(jobs, syncJob{weightIdx: i, file: f})
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
	for i, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-runCtx.Done():
				return
			}
			defer func() { <-sem }()
			w := lock.Weights[j.weightIdx]
			res, err := verify.Fetch(runCtx, r.Source, r.WorkDir, w.Repo, w.Revision, j.file, true)
			if err != nil {
				fail(fmt.Errorf("fetch %s@%s/%s: %w", w.Repo, w.Revision, j.file, err))
				return
			}
			mu.Lock()
			results[i] = res
			mu.Unlock()
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}

	// Upload each weight's files to every target as one batch.
	cachePaths := make([]map[string]string, len(lock.Weights))
	for i := range cachePaths {
		cachePaths[i] = make(map[string]string)
	}
	for i, j := range jobs {
		cachePaths[j.weightIdx][j.file] = results[i].Path
	}
	mirrorIDs := make([][]string, len(lock.Weights))
	for i, w := range lock.Weights {
		if len(cachePaths[i]) == 0 {
			continue
		}
		for _, t := range r.Targets {
			if err := t.UploadRepo(ctx, w.Repo, w.Revision, cachePaths[i]); err != nil {
				return nil, fmt.Errorf("upload %s to %s: %w", w.Repo, t.Name(), err)
			}
			mirrorIDs[i] = append(mirrorIDs[i], t.MirrorID(w.Repo))
		}
	}

	m := &lockfile.HashManifest{
		LockVersion: lock.Version,
		GeneratedAt: time.Now().UTC(),
	}
	for i, j := range jobs {
		w := lock.Weights[j.weightIdx]
		m.Entries = append(m.Entries, lockfile.HashEntry{
			Repo:     w.Repo,
			Revision: w.Revision,
			File:     j.file,
			SHA256:   results[i].SHA256,
			Size:     results[i].Size,
			Mirrors:  mirrorIDs[j.weightIdx],
		})
	}
	if err := lockfile.WriteManifest(outPath, m); err != nil {
		return nil, err
	}
	return m, nil
}
