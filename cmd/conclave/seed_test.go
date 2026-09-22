package main

import (
	"testing"

	"github.com/texhik/conclave/internal/config"
)

// TestSeedConfigLoads ensures `config init` writes a configuration that the
// loader accepts: the shipped seed must stay valid YAML with no duplicate keys.
func TestSeedConfigLoads(t *testing.T) {
	dir := t.TempDir()
	if err := writeSeed(dir, true); err != nil {
		t.Fatalf("writeSeed: %v", err)
	}
	cfg, err := config.Load(config.LoadOptions{
		GlobalDir:      dir,
		ProjectDir:     dir,
		DisableGlobal:  true,
		DisableProject: false,
	})
	if err != nil {
		t.Fatalf("seed config does not load: %v", err)
	}
	if len(cfg.Providers) == 0 || len(cfg.Roles) == 0 || len(cfg.Pipelines) == 0 {
		t.Fatalf("seed config is empty: providers=%d roles=%d pipelines=%d",
			len(cfg.Providers), len(cfg.Roles), len(cfg.Pipelines))
	}
	if len(cfg.Settings.ModelLimits) == 0 {
		t.Fatalf("model_limits did not parse")
	}
	if len(cfg.Settings.ModelPrices) == 0 {
		t.Fatalf("model_prices did not parse")
	}
}
