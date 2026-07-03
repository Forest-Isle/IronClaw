package canary

import (
	"context"
	"fmt"
	"time"

	"github.com/Forest-Isle/daimon/internal/agent"
	"github.com/Forest-Isle/daimon/internal/episode"
	"github.com/Forest-Isle/daimon/internal/mind"
	"github.com/Forest-Isle/daimon/internal/world"
)

// Runner executes golden tasks in fresh sandboxes and reports pass rates. N
// trials per task because LLM trajectories are stochastic: the regression signal
// is the pass-rate drop, not a single failure.
type Runner struct {
	// Provider is the LLM backend used by the real episode kernel.
	Provider mind.Provider
	// Model is forwarded to CognitiveRequest.Model.
	Model string
	// ProviderName is forwarded to CognitiveRequest.Provider.
	ProviderName string
	// MaxTokens is forwarded to CognitiveRequest.MaxTokens.
	MaxTokens int
	// Trials is the number of stochastic trajectories to run; <=0 means 1.
	Trials int
	// TrialTimeout bounds one trial; <=0 means 5 minutes.
	TrialTimeout time.Duration
}

// RunTask executes one canary task for the configured number of trials.
func (r *Runner) RunTask(ctx context.Context, task Task) (TaskResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	trials := r.Trials
	if trials <= 0 {
		trials = 1
	}
	timeout := r.TrialTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}

	result := TaskResult{
		Task:    task.Name,
		Results: make([]TrialResult, 0, trials),
	}
	for i := 0; i < trials; i++ {
		if err := ctx.Err(); err != nil {
			finalizeTaskResult(&result)
			return result, err
		}

		trial, err := r.runTrial(ctx, task, timeout)
		if err != nil {
			finalizeTaskResult(&result)
			return result, err
		}
		result.Results = append(result.Results, trial)
		result.Trials = len(result.Results)
		if trial.Passed {
			result.Passes++
		}
		if err := ctx.Err(); err != nil {
			finalizeTaskResult(&result)
			return result, err
		}
	}
	finalizeTaskResult(&result)
	return result, nil
}

func (r *Runner) runTrial(ctx context.Context, task Task, timeout time.Duration) (TrialResult, error) {
	sb, err := NewSandbox()
	if err != nil {
		return TrialResult{}, err
	}
	defer func() {
		_ = sb.Close()
	}()

	if err := sb.Seed(task.Seeds); err != nil {
		return TrialResult{}, err
	}

	trialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// CostRecorder is intentionally left nil here. Gateway/CLI wiring owns cost
	// attribution when canary execution is connected to a governed runtime.
	kernel := episode.NewRunner(r.Provider, sb.World, &world.Identity{Dir: sb.Root}, nil)
	outcome, execErr := kernel.Execute(trialCtx, agent.CognitiveRequest{
		Goal:          task.Goal,
		Trigger:       "canary:" + task.Name,
		ActivityClass: "canary",
		Model:         r.Model,
		Provider:      r.ProviderName,
		MaxTokens:     r.MaxTokens,
		ToolDefs:      sb.ToolDefs(),
		Invoke:        sb.Invoke(),
	})

	checks := evaluateChecks(sb.Root, outcome, task.Checks)
	trial := TrialResult{
		Outcome: outcome,
		Checks:  checks,
		Passed:  execErr == nil && checksPassed(checks),
	}
	if execErr != nil {
		trial.Err = execErr.Error()
	}

	if err := sb.Close(); err != nil {
		return trial, fmt.Errorf("canary: close trial sandbox: %w", err)
	}
	return trial, nil
}

func finalizeTaskResult(result *TaskResult) {
	result.Trials = len(result.Results)
	if result.Trials == 0 {
		result.PassRate = 0
		return
	}
	result.PassRate = float64(result.Passes) / float64(result.Trials)
}
