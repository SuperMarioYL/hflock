// Package cli wires the cobra root command and subcommands. m1 ships the
// `verify` command end-to-end; init/sync/list are registered as the documented
// CLI surface and wired in their respective milestones (m2/m3).
package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/SuperMarioYL/hflock/internal/lockfile"
	"github.com/SuperMarioYL/hflock/internal/mirror"
	"github.com/SuperMarioYL/hflock/internal/verify"
	"github.com/spf13/cobra"
)

// envHFBase / envWorkDir let the offline demo fixture and CI override the
// Hugging Face host and cache location without flags.
const (
	envHFBase  = "HFLOCK_HF_BASE"
	envWorkDir = "HFLOCK_WORKDIR"
)

// NewRootCmd builds the hflock command tree.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "hflock",
		Short: "国产模型权重镜像与哈希溯源 CLI",
		Long: `hflock pins DeepSeek/Qwen/GLM weights in a declarative lockfile, mirrors
them to Gitee AI + ModelScope, and emits a SHA256 hash-provenance manifest
machine-checkable in CI.

v0.1 (milestone m1) ships lockfile parse + hash verification. Mirror sync
(init/sync/list) lands in m2/m3.`,
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
	return &cobra.Command{
		Use:   "sync [lockfile]",
		Short: "Mirror pinned weights to Gitee AI + ModelScope, then verify (m2)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(*cobra.Command, []string) error { return notYet("m2") },
	}
}

func newListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list [lockfile]",
		Short: "Show per-weight pin/mirror/hash status (m3)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(*cobra.Command, []string) error { return notYet("m3") },
	}
}
