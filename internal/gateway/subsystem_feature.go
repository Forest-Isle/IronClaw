package gateway

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Forest-Isle/daimon/internal/config"
	"github.com/Forest-Isle/daimon/internal/feature"
)

type FeatureSubsystem struct {
	Registry *feature.Registry
}

func (fs *FeatureSubsystem) Name() string                  { return "feature" }
func (fs *FeatureSubsystem) Start(_ context.Context) error { return nil }
func (fs *FeatureSubsystem) Stop(_ context.Context) error  { return nil }

func InitFeatures(cfg *config.Config) *FeatureSubsystem {
	r := feature.NewRegistry()
	r.Register(feature.Feature{Name: "memory", Description: "Memory system", Default: true})
	r.Register(feature.Feature{Name: "skills", Description: "SKILL.md loading", Default: true})
	r.Register(feature.Feature{Name: "multi_agent", Description: "Sub-agent spawning", Default: true})
	r.Register(feature.Feature{Name: "selfops", Description: "Self-ops watchdog (health monitoring)", Default: false})
	for name, srv := range cfg.Tools.MCP.Servers {
		r.Register(feature.Feature{Name: "mcp_" + name, Description: fmt.Sprintf("MCP: %s", srv.Command), Default: true})
	}
	overrides := map[string]bool{
		"memory": cfg.Memory.Enabled, "skills": cfg.Skills.Enabled,
		"multi_agent": cfg.Agents.Enabled,
	}
	if persisted, err := loadFeatureState(defaultFeatureStatePath()); err == nil {
		for name, enabled := range persisted {
			overrides[name] = enabled
		}
	} else {
		slog.Warn("feature: failed to load persisted state", "err", err)
	}
	r.Resolve(context.Background(), overrides)
	return &FeatureSubsystem{Registry: r}
}

func (fs *FeatureSubsystem) IsEnabled(name string) bool {
	if fs.Registry == nil {
		return false
	}
	return fs.Registry.IsEnabled(name)
}
