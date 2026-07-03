package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/Forest-Isle/daimon/internal/appdir"
	"github.com/Forest-Isle/daimon/internal/channel/telegram"
	"github.com/Forest-Isle/daimon/internal/config"
	"github.com/Forest-Isle/daimon/internal/gateway"
	"github.com/Forest-Isle/daimon/internal/selfops"
	"github.com/Forest-Isle/daimon/internal/skill"
	"github.com/Forest-Isle/daimon/internal/userdir"
	"github.com/spf13/cobra"
)

var (
	cfgPath string
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	root := &cobra.Command{
		Use:   "daimon",
		Short: "Daimon — Local-first AI Agent Runtime",
	}

	var startDevMode bool
	startCmd := &cobra.Command{
		Use:   "start",
		Short: "Start the Daimon agent runtime",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStart(cfgPath, startDevMode)
		},
	}
	startCmd.Flags().StringVarP(&cfgPath, "config", "c", "", "path to config file (auto-discovered if empty)")
	startCmd.Flags().BoolVarP(&startDevMode, "dev", "d", false, "dev mode: use configs/daimon.yaml")

	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("daimon %s (commit: %s, built: %s)\n", version, commit, date)
		},
	}

	root.AddCommand(startCmd, versionCmd, newTUICmd(), newSkillCmd(), newMemoryCmd(), newMCPCmd(), newReplayCmd(), newProposalsCmd(), newCostsCmd(), newCorrectCmd(), newUndoCmd(), newHoldsCmd(), newWorldCmd(), newAttentionCmd(), newTrustCmd(), newBenchCmd(), newSoulCmd())

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// skillsDir returns the path to ~/.daimon/skills/.
func skillsDir() (string, error) {
	dir := appdir.SkillsDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create skills dir: %w", err)
	}
	return dir, nil
}

// resolveWorkdir returns the workdir for clawhub CLI.
// If dir is empty, defaults to ~/.daimon.
func resolveWorkdir(dir string) (string, error) {
	if dir != "" {
		return dir, nil
	}
	return appdir.BaseDir(), nil
}

// newSkillCmd builds the `daimon skill` subcommand group.
func newSkillCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skill",
		Short: "Manage Daimon skills",
	}
	cmd.AddCommand(
		newSkillListCmd(),
		newSkillSearchCmd(),
		newSkillInstallCmd(),
		newSkillUpdateCmd(),
		newSkillRemoveCmd(),
		newSkillDraftsCmd(),
		newSkillPromoteCmd(),
		newSkillDemoteCmd(),
	)
	return cmd
}

func newSkillListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List installed skills",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := skillsDir()
			if err != nil {
				return err
			}

			mgr := skill.New()
			if err := mgr.LoadBuiltin(); err != nil {
				return fmt.Errorf("load builtin skills: %w", err)
			}
			if err := mgr.LoadDir(dir); err != nil {
				return fmt.Errorf("load skills: %w", err)
			}

			skills := mgr.All()
			if len(skills) == 0 {
				fmt.Println("No skills installed. Use `daimon skill install <slug>` to install from ClawHub.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "NAME\tVERSION\tAUTHOR\tTAGS\tDESCRIPTION")
			for _, s := range skills {
				tags := strings.Join(s.Tags, ", ")
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
					s.Name, s.Version, s.Author, tags, truncate(s.Description, 50))
			}
			return w.Flush()
		},
	}
}

func newSkillSearchCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search ClawHub for skills",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := strings.Join(args, " ")
			c := exec.Command("clawhub", "search", query,
				"--limit", fmt.Sprintf("%d", limit), "--no-input")
			c.Stdout = os.Stdout
			c.Stderr = os.Stderr
			if err := c.Run(); err != nil {
				return fmt.Errorf("search failed (requires clawhub CLI: npm install -g clawhub): %w", err)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 10, "max results")
	return cmd
}

func newSkillInstallCmd() *cobra.Command {
	var workdir string
	cmd := &cobra.Command{
		Use:   "install <slug>",
		Short: "Install a skill from ClawHub",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := resolveWorkdir(workdir)
			if err != nil {
				return err
			}
			c := exec.Command("clawhub", "install", args[0],
				"--workdir", dir, "--no-input")
			c.Stdout = os.Stdout
			c.Stderr = os.Stderr
			if err := c.Run(); err != nil {
				return fmt.Errorf("install failed (requires clawhub CLI: npm install -g clawhub): %w", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&workdir, "workdir", "", "skills working directory (default: ~/.daimon)")
	return cmd
}

func newSkillUpdateCmd() *cobra.Command {
	var workdir string
	cmd := &cobra.Command{
		Use:   "update [slug]",
		Short: "Update installed skills from ClawHub",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := resolveWorkdir(workdir)
			if err != nil {
				return err
			}
			cmdArgs := []string{"update"}
			if len(args) > 0 {
				cmdArgs = append(cmdArgs, args[0])
			} else {
				cmdArgs = append(cmdArgs, "--all")
			}
			cmdArgs = append(cmdArgs, "--workdir", dir, "--no-input")
			c := exec.Command("clawhub", cmdArgs...)
			c.Stdout = os.Stdout
			c.Stderr = os.Stderr
			if err := c.Run(); err != nil {
				return fmt.Errorf("update failed (requires clawhub CLI: npm install -g clawhub): %w", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&workdir, "workdir", "", "skills working directory (default: ~/.daimon)")
	return cmd
}

func newSkillRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove an installed skill",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := skillsDir()
			if err != nil {
				return err
			}

			name := args[0]
			skillPath := filepath.Join(dir, name)

			if _, err := os.Stat(skillPath); os.IsNotExist(err) {
				return fmt.Errorf("skill %q not found in %s", name, dir)
			}

			fmt.Printf("Remove skill %q from %s? [y/N] ", name, dir)
			var answer string
			_, _ = fmt.Scanln(&answer)
			if strings.ToLower(strings.TrimSpace(answer)) != "y" {
				fmt.Println("Aborted.")
				return nil
			}

			if err := os.RemoveAll(skillPath); err != nil {
				return fmt.Errorf("remove skill: %w", err)
			}
			fmt.Printf("Skill %q removed.\n", name)
			return nil
		},
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func runStart(configPath string, devMode bool) error {
	// Setup logging
	setupLogging("info")

	if err := userdir.EnsureMigrated(); err != nil {
		return fmt.Errorf("migrate user config: %w", err)
	}

	resolvedPath, err := config.FindConfigPath(configPath, devMode)
	if err != nil {
		if isInteractive() {
			fmt.Println(err)
			fmt.Println()
			resolvedPath, err = runSetupWizard()
			if err != nil {
				return fmt.Errorf("setup: %w", err)
			}
		} else {
			return fmt.Errorf("find config: %w", err)
		}
	}
	slog.Info("loading config", "path", resolvedPath)
	cfg, err := config.Load(resolvedPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	setupLogging(cfg.Log.Level)

	if err := userdir.Apply(cfg); err != nil {
		return fmt.Errorf("load user config: %w", err)
	}

	gw, err := gateway.New(cfg, gateway.GatewayOptions{ConfigPath: resolvedPath})
	if err != nil {
		return fmt.Errorf("init gateway: %w", err)
	}

	// Create and register Telegram channel only when a real token is set.
	// An empty or unexpanded ${VAR} placeholder would make telegram.New call
	// the Bot API and fail, taking down the entire runtime.
	if tgToken := cfg.Telegram.Token; tgToken == "" || strings.HasPrefix(tgToken, "${") {
		slog.Info("telegram channel disabled (no token configured)")
	} else {
		tgTimeout := 30
		if cfg.Telegram.Timeout > 0 {
			tgTimeout = int(cfg.Telegram.Timeout.Seconds())
		}
		tg, err := telegram.New(tgToken, cfg.Telegram.AllowedUserIDs, tgTimeout)
		if err != nil {
			return fmt.Errorf("init telegram: %w", err)
		}
		if cfg.Agent.Execution.ApprovalTimeoutSeconds > 0 {
			tg.SetApprovalTimeout(cfg.Agent.Execution.ApprovalTimeoutSeconds)
		}
		gw.AddChannel(tg)
		gw.SetSchedulerNotifier(tg)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := gw.Start(ctx); err != nil {
		return fmt.Errorf("start gateway: %w", err)
	}

	slog.Info("daimon is running, press Ctrl+C to stop")

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	slog.Info("shutting down...")
	cancel()
	return gw.Stop(context.Background())
}

func setupLogging(level string) {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	base := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})
	slog.SetDefault(slog.New(selfops.NewTeeHandler(base)))
}
