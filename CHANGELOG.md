# Changelog

All notable changes to hflock are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

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

[Unreleased]: https://github.com/SuperMarioYL/hflock/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/SuperMarioYL/hflock/releases/tag/v0.2.0
[0.1.0]: https://github.com/SuperMarioYL/hflock/releases/tag/v0.1.0
