# Changelog

All notable changes to hflock are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.3.0] - 2026-10-20

### Fixed

- Cache paths are now collision-free: each of repo, revision and file is
  split into path atoms that are sanitized separately, so subdirectory files
  keep real subdirectories and a flat name like `sub_model.bin` can no longer
  share a cache file with `sub/model.bin`. Previously the flattened cache
  paths collided, the mirror received one pinned file's bytes under the
  other's name, and the manifest recorded both as verified.
- Gitee AI sync recovers from an interrupted first clone: a clone directory
  left behind without `.git` (killed process, OOM, sleep during a long push)
  is cleared and re-cloned instead of failing every later run with
  "destination path already exists". Repo ids that would place the clone
  outside the clone root are refused outright.
- `hflock list` keys status by repo@revision (the same key the `--check`
  gate diffs on). Previously a manifest still holding the old revision's
  entries marked a newly pinned revision verified, and two weights pinning
  different revisions of one repo merged their hash counts and mirrors.

### Changed

- `hflock sync` validates the `gitee-ai` target up front: on a machine
  without git in PATH the run now fails before any download starts instead
  of after the full weight download.

## [0.2.0] - 2026-09-15

### Fixed

- Glob expansion now follows the Hugging Face tree API's `Link rel="next"`
  pagination. Repos spanning multiple pages (default page size 1000) no
  longer silently lose every file past page 1 from the hash manifest.
- A glob pattern that matches zero files now fails the run with an error
  naming the pattern and repo@revision. Previously it exited 0 and wrote an
  empty manifest — the CI gate verified nothing and still passed.

### Added

- `hflock sync` (the plan's m2 milestone): concurrent, resumable downloads
  (Range-based) and mirror upload to ModelScope (their REST API: repo
  ensure, LFS batch, presigned PUT, commit) and Gitee AI (git push, the
  platform's documented upload mechanism). The manifest records each
  entry's mirrors. Tokens come from `HFLOCK_GITEE_TOKEN` /
  `HFLOCK_MODELSCOPE_TOKEN`.
- `hflock init` (m3): generates a weights.lock.yaml pinning a Hugging Face
  repo — the revision is resolved to its commit sha when possible, default
  pins are the repo's metadata files, and `--files` / `--all` override.
- `hflock list` (m3): per-weight status table — pins, hashed files,
  mirrors, verified/unverified.
- `hflock verify --check <baseline.json>` (m3): diffs a freshly computed
  manifest against a trusted baseline and exits 1 on any hash, size,
  missing or extra entry — the CI air-gap gate.
- `hflock --version` and this CHANGELOG.

## [0.1.0] - 2026-08-27

### Added

- Declarative `weights.lock.yaml` (parse + validate) and the SHA256
  verifier: `hflock verify` downloads pinned weights from Hugging Face and
  emits `weights.lock.manifest.json`.

[Unreleased]: https://github.com/SuperMarioYL/hflock/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/SuperMarioYL/hflock/releases/tag/v0.3.0
[0.2.0]: https://github.com/SuperMarioYL/hflock/releases/tag/v0.2.0
[0.1.0]: https://github.com/SuperMarioYL/hflock/releases/tag/v0.1.0
