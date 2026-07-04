package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Forest-Isle/daimon/internal/canary"
	"github.com/Forest-Isle/daimon/internal/config"
	"github.com/Forest-Isle/daimon/internal/mind"
	"github.com/spf13/cobra"
)

func newCanaryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "canary",
		Short: "Run golden sandbox canaries",
	}
	cmd.AddCommand(newCanaryRunCmd())
	return cmd
}

func newCanaryRunCmd() *cobra.Command {
	var suiteDir string
	var taskName string
	var trials int
	var timeoutSeconds int
	var model string
	var providerName string
	var configPath string
	var devMode bool
	var outPath string
	var baselinePath string
	var tolerance float64

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run canary golden tasks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := canaryRunOptions{
				suiteDir:       suiteDir,
				taskName:       taskName,
				trials:         trials,
				timeoutSeconds: timeoutSeconds,
				model:          model,
				providerName:   providerName,
				configPath:     configPath,
				devMode:        devMode,
				outPath:        outPath,
				baselinePath:   baselinePath,
				tolerance:      tolerance,
			}
			return runCanary(cmd.Context(), opts)
		},
	}
	cmd.Flags().StringVar(&suiteDir, "suite", "", "external canary task directory (default: embedded suite)")
	cmd.Flags().StringVar(&taskName, "task", "", "run only the named task")
	cmd.Flags().IntVar(&trials, "trials", 3, "trials per task")
	cmd.Flags().IntVar(&timeoutSeconds, "timeout", 300, "per-trial timeout in seconds")
	cmd.Flags().StringVar(&model, "model", "", "model override (default: config llm.model)")
	cmd.Flags().StringVar(&providerName, "provider-name", "", "provider override (default: config llm.provider)")
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "path to config file (auto-discovered if empty)")
	cmd.Flags().BoolVar(&devMode, "dev", false, "use configs/daimon.yaml in dev mode")
	cmd.Flags().StringVar(&outPath, "out", "", "write JSON canary report to path")
	cmd.Flags().StringVar(&baselinePath, "baseline", "", "compare against baseline report; non-zero exit on regression")
	cmd.Flags().Float64Var(&tolerance, "tolerance", 0.34, "allowed pass-rate drop before baseline regression")
	return cmd
}

type canaryRunOptions struct {
	suiteDir       string
	taskName       string
	trials         int
	timeoutSeconds int
	model          string
	providerName   string
	configPath     string
	devMode        bool
	outPath        string
	baselinePath   string
	tolerance      float64
}

func runCanary(ctx context.Context, opts canaryRunOptions) error {
	tasks, err := loadCanaryTasks(opts.suiteDir)
	if err != nil {
		return err
	}
	tasks, err = filterCanaryTasks(tasks, opts.taskName)
	if err != nil {
		return err
	}

	resolvedPath, err := config.FindConfigPath(opts.configPath, opts.devMode)
	if err != nil {
		return fmt.Errorf("find config: %w", err)
	}
	cfg, err := config.Load(resolvedPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if opts.model != "" {
		cfg.LLM.Model = opts.model
	}
	if opts.providerName != "" {
		cfg.LLM.Provider = opts.providerName
	}

	runner := canary.Runner{
		Provider:     mind.NewProviderFromConfig(cfg.LLM),
		Model:        cfg.LLM.Model,
		ProviderName: cfg.LLM.Provider,
		MaxTokens:    cfg.LLM.MaxTokens,
		Trials:       opts.trials,
		TrialTimeout: time.Duration(opts.timeoutSeconds) * time.Second,
	}

	results := make([]canary.TaskResult, 0, len(tasks))
	for _, task := range tasks {
		result, err := runner.RunTask(ctx, task)
		if err != nil {
			return fmt.Errorf("run canary task %q: %w", task.Name, err)
		}
		results = append(results, result)
	}

	printCanaryRunReport(results)
	printCanaryFailureDetails(results)

	report := canary.NewReport(runner.Model, results)
	if opts.outPath != "" {
		if err := canary.WriteReport(opts.outPath, report); err != nil {
			return err
		}
	}
	if opts.baselinePath != "" {
		baseline, err := canary.LoadReport(opts.baselinePath)
		if err != nil {
			return err
		}
		regressions := canary.CompareBaseline(baseline, report, opts.tolerance)
		if len(regressions) > 0 {
			for _, regression := range regressions {
				fmt.Fprintf(os.Stderr, "REGRESSION\t%s\n", regression)
			}
			return fmt.Errorf("%d canary regression(s)", len(regressions))
		}
	}
	return nil
}

func loadCanaryTasks(suiteDir string) ([]canary.Task, error) {
	if suiteDir != "" {
		tasks, err := canary.LoadSuiteDir(suiteDir)
		if err != nil {
			return nil, fmt.Errorf("load canary suite: %w", err)
		}
		return tasks, nil
	}
	tasks, err := canary.DefaultSuite()
	if err != nil {
		return nil, fmt.Errorf("load default canary suite: %w", err)
	}
	return tasks, nil
}

func filterCanaryTasks(tasks []canary.Task, taskName string) ([]canary.Task, error) {
	taskName = strings.TrimSpace(taskName)
	if taskName == "" {
		return tasks, nil
	}
	for _, task := range tasks {
		if task.Name == taskName {
			return []canary.Task{task}, nil
		}
	}
	return nil, fmt.Errorf("canary task %q not found", taskName)
}

func printCanaryRunReport(results []canary.TaskResult) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "TASK\tTRIALS\tPASSES\tRATE\tTOKENS")
	var totalTrials int
	var totalPasses int
	var totalIn int64
	var totalOut int64
	for _, result := range results {
		totalTrials += result.Trials
		totalPasses += result.Passes
		totalIn += result.InputTokens
		totalOut += result.OutputTokens
		_, _ = fmt.Fprintf(w, "%s\t%d\t%d\t%.2f\t%d+%d\n",
			result.Task, result.Trials, result.Passes, result.PassRate, result.InputTokens, result.OutputTokens)
	}
	totalRate := 0.0
	if totalTrials > 0 {
		totalRate = float64(totalPasses) / float64(totalTrials)
	}
	_, _ = fmt.Fprintf(w, "TOTAL\t%d\t%d\t%.2f\t%d+%d\n", totalTrials, totalPasses, totalRate, totalIn, totalOut)
	_ = w.Flush()
}

func printCanaryFailureDetails(results []canary.TaskResult) {
	for _, result := range results {
		if result.PassRate >= 1.0 {
			continue
		}
		printed := 0
		for trialIndex, trial := range result.Results {
			if trial.Passed {
				continue
			}
			if trial.Err != "" {
				fmt.Fprintf(os.Stderr, "%s trial %d error: %s\n", result.Task, trialIndex+1, trial.Err)
				printed++
			}
			for _, check := range trial.Checks {
				if check.Passed || check.Detail == "" {
					continue
				}
				fmt.Fprintf(os.Stderr, "%s trial %d check %s: %s\n", result.Task, trialIndex+1, describeCanaryCheck(check.Check), check.Detail)
				printed++
				if printed >= 5 {
					break
				}
			}
			if printed >= 5 {
				break
			}
		}
	}
}

func describeCanaryCheck(check canary.Check) string {
	switch {
	case check.Path != "":
		return check.Kind + "(" + check.Path + ")"
	case check.Status != "":
		return check.Kind + "(" + check.Status + ")"
	default:
		return check.Kind
	}
}
