package gateway

import "testing"

func TestServerIsNotRuntimeFeature(t *testing.T) {
	cfg := testConfig(t)
	cfg.Server.Enabled = true
	features := InitFeatures(cfg).Registry.List()
	for _, entry := range features {
		if entry.Name == "server" {
			t.Fatal("server must be controlled only by YAML, not the runtime feature registry")
		}
	}
}
