// Package cli wires the cobra root command and subcommands. v0.2.0 ships
// verify + sync end-to-end; init/list land with milestone m3.
package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	envHFBase           = "HFLOCK_HF_BASE"
	envWorkDir          = "HFLOCK_WORKDIR"
	envGiteeToken       = "HFLOCK_GITEE_TOKEN"
	envGiteeUser        = "HFLOCK_GITEE_USER"
	envModelScopeToken  = "HFLOCK_MODELSCOPE_TOKEN"
)

// NewRootCmd builds the hflock command tree.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "hflock",
		Short: "国产模型权重镜像与哈希溯源 CLI",
		Long: `hflock pins DeepSeek/Qwen/GLM weights in a declarative lockfile, mirrors
them to Gitee AI + ModelScope, and emits a SHA256 hash-provenance manifest
machine-checkable in CI.

v0.2.0 ships lockfile parse + hash verification (verify) and the mirror
sync flow (sync). init/list land with milestone m3.`,
	}
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
	var manifestPath, hfBase, workDir string
	var progress bool
	cmd := &cobra.Command{
		Use:   "verify [lockfile]",
		Short: "Download pinned weights from Hugging Face and emit a SHA256 hash manifest",
		Long: `Downloads every pinned weight from Hugging Face, recomputes each file's
SHA256, and writes weights.lock.manifest.json — a machine-checkable provenance
record a CI air-gap gate can fail on. m1 performs zero mirror uploads.`,
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
			return nil
		},
	}
	cmd.Flags().StringVarP(&manifestPath, "manifest", "m", "", "output manifest path (default weights.lock.manifest.json)")
	cmd.Flags().StringVar(&hfBase, "hf-base", "", fmt.Sprintf("Hugging Face base URL override (or $%s)", envHFBase))
	cmd.Flags().StringVar(&workDir, "workdir", "", fmt.Sprintf("download cache dir (or $%s)", envWorkDir))
	cmd.Flags().BoolVar(&progress, "progress", false, "show a per-file download progress bar")
	return cmd
}

// notYet returns the standard message for commands landing in a later milestone.
func notYet(milestone string) error {
	return fmt.Errorf("this command ships in milestone %s; m1 ships `verify` only (see README roadmap)", milestone)
}

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init [repo]",
		Short: "Generate a lockfile from Hugging Face repo refs (m3)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(*cobra.Command, []string) error { return notYet("m3") },
	}
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
	return &cobra.Command{
		Use:   "list [lockfile]",
		Short: "Show per-weight pin/mirror/hash status (m3)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(*cobra.Command, []string) error { return notYet("m3") },
	}
}
