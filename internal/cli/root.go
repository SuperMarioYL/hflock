// Package cli wires the cobra root command and subcommands: verify (hash
// provenance + --check CI gate), sync (mirror upload), init (lockfile
// generation) and list (pin/mirror/hash status).
package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/SuperMarioYL/hflock/internal/lockfile"
	"github.com/SuperMarioYL/hflock/internal/mirror"
	syncer "github.com/SuperMarioYL/hflock/internal/sync"
	"github.com/SuperMarioYL/hflock/internal/verify"
	"github.com/spf13/cobra"
)

// envHFBase / envWorkDir let the offline demo fixture and CI override the
// Hugging Face host and cache location without flags. The token envs feed
// sync's mirror uploads.
const (
	envHFBase          = "HFLOCK_HF_BASE"
	envWorkDir         = "HFLOCK_WORKDIR"
	envGiteeToken      = "HFLOCK_GITEE_TOKEN"
	envGiteeUser       = "HFLOCK_GITEE_USER"
	envModelScopeToken = "HFLOCK_MODELSCOPE_TOKEN"
)

// Version is the hflock tool version, kept in lockstep with the VERSION file
// and CHANGELOG.md (asserted by version_test.go). The lockfile schema version
// (lockfile.Version) is a separate data contract and stays 0.1.0.
const Version = "0.2.0"

// NewRootCmd builds the hflock command tree.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "hflock",
		Short: "国产模型权重镜像与哈希溯源 CLI",
		Long: `hflock pins DeepSeek/Qwen/GLM weights in a declarative lockfile, mirrors
them to Gitee AI + ModelScope, and emits a SHA256 hash-provenance manifest
machine-checkable in CI.

verify re-downloads and re-hashes the pin set (optionally diffing a trusted
baseline via --check); sync additionally mirrors the files to the CN
platforms; init generates a lockfile from an HF repo; list shows per-weight
status.`,
		Version: Version,
	}
	root.SetVersionTemplate("hflock {{.Version}}\n")
	root.AddCommand(
		newVerifyCmd(),
		newInitCmd(),
		newSyncCmd(),
		newListCmd(),
	)
	return root
}

// Execute runs the root command and exits non-zero on error.
func Execute() {
	if err := NewRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func newVerifyCmd() *cobra.Command {
	var manifestPath, hfBase, workDir, checkPath string
	var progress bool
	cmd := &cobra.Command{
		Use:   "verify [lockfile]",
		Short: "Download pinned weights from Hugging Face and emit a SHA256 hash manifest",
		Long: `Downloads every pinned weight from Hugging Face, recomputes each file's
SHA256, and writes weights.lock.manifest.json — a machine-checkable provenance
record a CI air-gap gate can fail on.

With --check <baseline.json> the freshly computed manifest is diffed against
the baseline (repo@revision/file keyed): any hash, size, missing or extra
entry fails the run with exit code 1 — the CI air-gap gate. Without --check
the run only records.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			lock, err := lockfile.Load(args[0])
			if err != nil {
				return err
			}
			src := mirror.NewHFSource()
			base := mirror.DefaultHFBase
			if v := os.Getenv(envHFBase); v != "" {
				base = v
			}
			if hfBase != "" {
				base = hfBase
			}
			src.Base = base
			src.ShowBar = progress

			v := verify.New(src)
			dir := os.Getenv(envWorkDir)
			if workDir != "" {
				dir = workDir
			}
			v.WorkDir = dir

			if manifestPath == "" {
				manifestPath = "weights.lock.manifest.json"
			}
			m, err := v.Verify(context.Background(), lock, manifestPath)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "verified %d file(s) from %d weight(s); manifest -> %s\n",
				len(m.Entries), len(lock.Weights), manifestPath)

			if checkPath == "" {
				return nil
			}
			baseline, err := lockfile.ReadManifest(checkPath)
			if err != nil {
				return fmt.Errorf("read baseline: %w", err)
			}
			diffs := verify.CheckManifest(baseline, m)
			if len(diffs) > 0 {
				for _, d := range diffs {
					fmt.Fprintf(cmd.ErrOrStderr(), "check: %s\n", d)
				}
				return fmt.Errorf("check failed: %d difference(s) against %s", len(diffs), checkPath)
			}
			fmt.Fprintf(os.Stdout, "check passed: %d entries match %s\n", len(m.Entries), checkPath)
			return nil
		},
	}
	cmd.Flags().StringVarP(&manifestPath, "manifest", "m", "", "output manifest path (default weights.lock.manifest.json)")
	cmd.Flags().StringVar(&hfBase, "hf-base", "", fmt.Sprintf("Hugging Face base URL override (or $%s)", envHFBase))
	cmd.Flags().StringVar(&workDir, "workdir", "", fmt.Sprintf("download cache dir (or $%s)", envWorkDir))
	cmd.Flags().StringVar(&checkPath, "check", "", "baseline manifest to diff against (exit 1 on any difference)")
	cmd.Flags().BoolVar(&progress, "progress", false, "show a per-file download progress bar")
	return cmd
}

// initDefaultFiles are the metadata files init pins by default — the small,
// build-critical files present in most weight repos.
var initDefaultFiles = []string{"config.json", "generation_config.json", "tokenizer.json"}

func newInitCmd() *cobra.Command {
	var revision, filesFlag, outPath, hfBase string
	var all bool
	cmd := &cobra.Command{
		Use:   "init <owner/repo>",
		Short: "Generate a weights.lock.yaml pinning a Hugging Face repo",
		Long: `Pins a Hugging Face repo at --revision (default main), resolved to its
commit sha when the API answers (so the lockfile references an immutable
revision). By default the lock pins the repo's metadata files
(config/generation_config/tokenizer json); --files pins explicit names or
globs, --all pins every file in the repo.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo := args[0]
			if i := strings.IndexByte(repo, '/'); i <= 0 || i == len(repo)-1 {
				return fmt.Errorf("repo must be owner/name, got %q", repo)
			}
			src := mirror.NewHFSource()
			base := mirror.DefaultHFBase
			if v := os.Getenv(envHFBase); v != "" {
				base = v
			}
			if hfBase != "" {
				base = hfBase
			}
			src.Base = base

			rev := revision
			// fail-soft: keep the given ref when the API cannot resolve it
			if resolved, err := src.ResolveRevision(context.Background(), repo, rev); err == nil {
				rev = resolved
			}

			var files []string
			switch {
			case all:
				tree, err := src.ListAll(context.Background(), repo, revision)
				if err != nil {
					return fmt.Errorf("list %s@%s: %w", repo, revision, err)
				}
				if len(tree) == 0 {
					return fmt.Errorf("repo %s@%s has no files to pin", repo, revision)
				}
				files = tree
			case filesFlag != "":
				for _, f := range strings.Split(filesFlag, ",") {
					if f = strings.TrimSpace(f); f != "" {
						files = append(files, f)
					}
				}
				if len(files) == 0 {
					return fmt.Errorf("--files named no files")
				}
			default:
				tree, err := src.ListAll(context.Background(), repo, revision)
				if err != nil {
					return fmt.Errorf("list %s@%s: %w", repo, revision, err)
				}
				present := make(map[string]bool, len(tree))
				for _, f := range tree {
					present[f] = true
				}
				for _, f := range initDefaultFiles {
					if present[f] {
						files = append(files, f)
					}
				}
				if len(files) == 0 {
					return fmt.Errorf("no default metadata files in %s@%s; pass --files or --all", repo, revision)
				}
			}

			lock := &lockfile.WeightLock{
				Version: lockfile.Version,
				Weights: []lockfile.PinnedWeight{{
					Repo:     repo,
					Revision: rev,
					Files:    files,
					Source:   lockfile.SourceHuggingFace,
				}},
			}
			if err := lock.Save(outPath); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "pinned %s@%s (%d file pattern(s)) -> %s\n", repo, rev, len(files), outPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&revision, "revision", "main", "revision (branch, tag or commit) to pin")
	cmd.Flags().StringVar(&filesFlag, "files", "", "comma-separated file names or globs to pin (default: repo metadata files)")
	cmd.Flags().BoolVar(&all, "all", false, "pin every file in the repo")
	cmd.Flags().StringVar(&outPath, "out", "weights.lock.yaml", "output lockfile path")
	cmd.Flags().StringVar(&hfBase, "hf-base", "", fmt.Sprintf("Hugging Face base URL override (or $%s)", envHFBase))
	return cmd
}

func newSyncCmd() *cobra.Command {
	var manifestPath, hfBase, workDir, mirrors string
	var progress bool
	var concurrency int
	var giteeBase, giteeToken, giteeUser, msBase, msToken string
	cmd := &cobra.Command{
		Use:   "sync [lockfile]",
		Short: "Mirror pinned weights to Gitee AI + ModelScope, then emit the hash manifest",
		Long: `Downloads every pinned weight from Hugging Face (concurrently, resuming
partial downloads), uploads each weight's files to the configured CN mirrors,
and writes weights.lock.manifest.json with each entry's mirrors recorded.

ModelScope uploads use the platform's REST API (token via
HFLOCK_MODELSCOPE_TOKEN or --modelscope-token). Gitee AI has no REST upload
API — its documented mechanism is git push (token via HFLOCK_GITEE_TOKEN or
--gitee-token); large LFS files there need the operator's gai/git-lfs setup.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := os.Getenv(envWorkDir)
			if workDir != "" {
				dir = workDir
			}
			// validate the mirror spec before touching the filesystem
			targets, err := buildTargets(mirrors, giteeBase, giteeToken, giteeUser, msBase, msToken, dir)
			if err != nil {
				return err
			}
			lockPath := "weights.lock.yaml"
			if len(args) == 1 {
				lockPath = args[0]
			}
			lock, err := lockfile.Load(lockPath)
			if err != nil {
				return err
			}
			src := mirror.NewHFSource()
			base := mirror.DefaultHFBase
			if v := os.Getenv(envHFBase); v != "" {
				base = v
			}
			if hfBase != "" {
				base = hfBase
			}
			src.Base = base
			src.ShowBar = progress

			if manifestPath == "" {
				manifestPath = "weights.lock.manifest.json"
			}
			r := syncer.Runner{Source: src, Targets: targets, WorkDir: dir, Concurrency: concurrency}
			m, err := r.Run(context.Background(), lock, manifestPath)
			if err != nil {
				return err
			}
			names := make([]string, 0, len(targets))
			for _, t := range targets {
				names = append(names, t.Name())
			}
			fmt.Fprintf(os.Stdout, "synced %d file(s) from %d weight(s) to %s; manifest -> %s\n",
				len(m.Entries), len(lock.Weights), strings.Join(names, "+"), manifestPath)
			return nil
		},
	}
	cmd.Flags().StringVarP(&manifestPath, "manifest", "m", "", "output manifest path (default weights.lock.manifest.json)")
	cmd.Flags().StringVar(&hfBase, "hf-base", "", fmt.Sprintf("Hugging Face base URL override (or $%s)", envHFBase))
	cmd.Flags().StringVar(&workDir, "workdir", "", fmt.Sprintf("download cache dir (or $%s)", envWorkDir))
	cmd.Flags().StringVar(&mirrors, "mirrors", "gitee-ai,modelscope", "comma-separated upload targets (gitee-ai, modelscope)")
	cmd.Flags().IntVar(&concurrency, "concurrency", 4, "parallel downloads")
	cmd.Flags().BoolVar(&progress, "progress", false, "show a per-file download progress bar")
	cmd.Flags().StringVar(&giteeBase, "gitee-base", "", "Gitee AI base URL override")
	cmd.Flags().StringVar(&giteeToken, "gitee-token", "", fmt.Sprintf("Gitee AI access token (or $%s)", envGiteeToken))
	cmd.Flags().StringVar(&giteeUser, "gitee-user", "", fmt.Sprintf("git https username for Gitee AI pushes (or $%s, default oauth2)", envGiteeUser))
	cmd.Flags().StringVar(&msBase, "modelscope-base", "", "ModelScope base URL override")
	cmd.Flags().StringVar(&msToken, "modelscope-token", "", fmt.Sprintf("ModelScope access token (or $%s)", envModelScopeToken))
	return cmd
}

// buildTargets resolves the --mirrors spec into upload targets, applying the
// token envs under flag values.
func buildTargets(spec, giteeBase, giteeToken, giteeUser, msBase, msToken, workDir string) ([]mirror.Target, error) {
	if giteeToken == "" {
		giteeToken = os.Getenv(envGiteeToken)
	}
	if giteeUser == "" {
		giteeUser = os.Getenv(envGiteeUser)
	}
	if msToken == "" {
		msToken = os.Getenv(envModelScopeToken)
	}
	var targets []mirror.Target
	for _, name := range strings.Split(spec, ",") {
		switch strings.TrimSpace(name) {
		case "gitee-ai":
			g := &mirror.GiteeTarget{Base: giteeBase, Token: giteeToken, User: giteeUser}
			if workDir != "" {
				g.CloneRoot = filepath.Join(workDir, "gitee-ai")
			}
			targets = append(targets, g)
		case "modelscope":
			targets = append(targets, &mirror.ModelScopeTarget{Base: msBase, Token: msToken})
		case "":
			return nil, fmt.Errorf("--mirrors must name at least one of: gitee-ai, modelscope")
		default:
			return nil, fmt.Errorf("unknown mirror %q (valid: gitee-ai, modelscope)", name)
		}
	}
	return targets, nil
}

func newListCmd() *cobra.Command {
	var manifestPath string
	cmd := &cobra.Command{
		Use:   "list [lockfile]",
		Short: "Show per-weight pin/mirror/hash status",
		Long: `Prints one row per pinned weight: repo, revision, pinned patterns, how
many files a manifest hashed for it, the mirrors that hold copies, and a
verified/unverified status. The manifest defaults to
weights.lock.manifest.json; when it is absent the row simply reports
unverified (list never fails on a missing manifest).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			lockPath := "weights.lock.yaml"
			if len(args) == 1 {
				lockPath = args[0]
			}
			lock, err := lockfile.Load(lockPath)
			if err != nil {
				return err
			}
			// a missing manifest is information, not an error
			var manifest *lockfile.HashManifest
			if m, err := lockfile.ReadManifest(manifestPath); err == nil {
				manifest = m
			}

			type status struct {
				hashed  int
				mirrors []string
				seen    bool
			}
			byRepo := map[string]*status{}
			if manifest != nil {
				for _, e := range manifest.Entries {
					st := byRepo[e.Repo]
					if st == nil {
						st = &status{}
						byRepo[e.Repo] = st
					}
					st.hashed++
					st.seen = true
					for _, m := range e.Mirrors {
						if !containsStr(st.mirrors, m) {
							st.mirrors = append(st.mirrors, m)
						}
					}
				}
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(w, "REPO\tREVISION\tPINS\tHASHED\tMIRRORS\tSTATUS")
			for _, weight := range lock.Weights {
				st := byRepo[weight.Repo]
				hashed, mirrors, state := "-", "-", "unverified"
				if st != nil && st.seen {
					hashed = strconv.Itoa(st.hashed)
					state = "verified"
					if len(st.mirrors) > 0 {
						mirrors = strings.Join(st.mirrors, ",")
					}
				}
				fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\n",
					weight.Repo, weight.Revision, len(weight.Files), hashed, mirrors, state)
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVarP(&manifestPath, "manifest", "m", "weights.lock.manifest.json", "manifest to read mirror/hash status from")
	return cmd
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
