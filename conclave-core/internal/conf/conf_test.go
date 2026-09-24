package conf

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/texhik/conclave/conclave-core/internal/config"
	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/playbook"
	"github.com/texhik/conclave/conclave-core/internal/store/memory"
)

func TestSeedReloadAndHotChange(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	reg, err := playbook.LoadBuiltin()
	if err != nil {
		t.Fatal(err)
	}

	in := SeedInput{
		Providers: map[string]config.Provider{
			"deepseek": {BaseURL: "http://deepseek", APIKey: "sk-x", Models: []string{"deepseek-chat"}},
		},
		DefaultProvider: "deepseek",
		DefaultModel:    "deepseek-chat",
		Roles:           reg.List(),
	}
	if err := Seed(ctx, st, in, nil); err != nil {
		t.Fatal(err)
	}
	// Seeding is idempotent.
	if err := Seed(ctx, st, in, nil); err != nil {
		t.Fatal(err)
	}

	mgr := New(st, time.Minute, nil)
	if err := mgr.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	snap := mgr.Current()
	if got := snap.BrainModel(domain.BrainController); got != "deepseek/deepseek-chat" {
		t.Fatalf("controller model = %q", got)
	}
	pb, ok := mgr.Get("ba")
	if !ok || len(pb.Roles) == 0 {
		t.Fatalf("ba role missing: %+v", pb)
	}
	for _, g := range pb.Roles {
		if g.Model == "" {
			t.Fatalf("grade %q has no model", g.Tier)
		}
	}
	if ps, _ := st.ListProviders(ctx); len(ps) != 1 || ps[0].APIKey != "sk-x" {
		t.Fatalf("providers = %+v", ps)
	}

	// Hot change: add a provider + model and point the controller at it.
	pid := uuid.NewString()
	if err := st.UpsertProvider(ctx, domain.Provider{ID: pid, Name: "ollama", BaseURL: "http://ollama", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	mid := uuid.NewString()
	if err := st.UpsertProviderModel(ctx, domain.ProviderModel{ID: mid, ProviderID: pid, Name: "qwen", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertBrainConfig(ctx, domain.BrainConfig{Name: domain.BrainController, ModelID: mid}); err != nil {
		t.Fatal(err)
	}
	mgr.reloadIfChanged(ctx)
	if got := mgr.Current().BrainModel(domain.BrainController); got != "ollama/qwen" {
		t.Fatalf("controller model after reload = %q, want ollama/qwen", got)
	}
}
