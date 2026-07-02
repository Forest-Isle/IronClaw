package main

import (
	"fmt"
	"os"

	"github.com/Forest-Isle/daimon/internal/bench"
	"github.com/Forest-Isle/daimon/internal/config"
	"github.com/Forest-Isle/daimon/internal/mind"
	"github.com/spf13/cobra"
)

func newBenchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bench",
		Short: "Run Daimon benchmarks",
	}
	cmd.AddCommand(newBenchBehaviorCmd())
	return cmd
}

func newBenchBehaviorCmd() *cobra.Command {
	var casesDir string
	var configPath string
	var devMode bool
	var model string

	cmd := &cobra.Command{
		Use:   "behavior",
		Short: "Run forked-world file behavior cases",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBenchBehavior(cmd, casesDir, configPath, devMode, model)
		},
	}
	cmd.Flags().StringVar(&casesDir, "cases", "", "behavior bench cases directory")
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "path to config file (auto-discovered if empty)")
	cmd.Flags().BoolVar(&devMode, "dev", false, "dev mode: use configs/daimon.yaml")
	cmd.Flags().StringVar(&model, "model", "", "model override (default: config llm.model)")
	_ = cmd.MarkFlagRequired("cases")
	return cmd
}

func runBenchBehavior(cmd *cobra.Command, casesDir, configPath string, devMode bool, model string) error {
	cases, err := bench.LoadCases(casesDir)
	if err != nil {
		return fmt.Errorf("load behavior cases: %w", err)
	}
	resolvedPath, err := config.FindConfigPath(configPath, devMode)
	if err != nil {
		return fmt.Errorf("find config: %w", err)
	}
	cfg, err := config.Load(resolvedPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if model == "" {
		model = cfg.LLM.Model
	}
	results, err := bench.Run(cmd.Context(), mind.NewProviderFromConfig(cfg.LLM), cases, bench.Options{
		Model:     model,
		MaxTokens: cfg.LLM.MaxTokens,
	})
	if err != nil {
		return fmt.Errorf("run behavior bench: %w", err)
	}
	bench.WriteReport(os.Stdout, results)
	if failed := failedCaseCount(results); failed > 0 {
		return fmt.Errorf("%d/%d behavior bench cases failed", failed, len(results))
	}
	return nil
}

func failedCaseCount(results []bench.CaseResult) int {
	failed := 0
	for _, result := range results {
		if !result.Passed {
			failed++
		}
	}
	return failed
}
