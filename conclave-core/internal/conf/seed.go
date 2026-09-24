package conf

import (
	"context"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/texhik/conclave/conclave-core/internal/config"
	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/playbook"
	"github.com/texhik/conclave/conclave-core/internal/store"
)

// SeedInput carries the bootstrap values used to populate an empty database.
type SeedInput struct {
	Providers       map[string]config.Provider
	DefaultProvider string
	DefaultModel    string
	Roles           []playbook.Playbook
}

// Seed populates providers, roles/grades and brain configs when their tables are
// empty. Existing rows are never overwritten, so manual edits survive restarts.
func Seed(ctx context.Context, st store.Store, in SeedInput, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	provModels, err := seedProviders(ctx, st, in, logger)
	if err != nil {
		return err
	}
	if err := seedRoles(ctx, st, in, provModels, logger); err != nil {
		return err
	}
	return seedBrains(ctx, st, in, provModels, logger)
}

func seedProviders(ctx context.Context, st store.Store, in SeedInput, logger *slog.Logger) (map[string]map[string]string, error) {
	existing, err := st.ListProviders(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]string{}
	if len(existing) > 0 {
		models, err := st.ListProviderModels(ctx)
		if err != nil {
			return nil, err
		}
		provName := map[string]string{}
		for _, p := range existing {
			provName[p.ID] = p.Name
		}
		for _, m := range models {
			if out[provName[m.ProviderID]] == nil {
				out[provName[m.ProviderID]] = map[string]string{}
			}
			out[provName[m.ProviderID]][m.Name] = m.ID
		}
		return out, nil
	}
	if len(in.Providers) == 0 {
		logger.Warn("no providers configured in DB or env; configure providers via the admin API")
		return out, nil
	}
	for name, p := range in.Providers {
		pid := uuid.NewString()
		prov := domain.Provider{
			ID: pid, Name: name, BaseURL: p.BaseURL, APIKey: p.APIKey,
			Headers: p.Headers, Enabled: true,
		}
		if err := st.UpsertProvider(ctx, prov); err != nil {
			return nil, err
		}
		names := append([]string{}, p.Models...)
		if name == in.DefaultProvider && in.DefaultModel != "" && !contains(names, in.DefaultModel) {
			names = append(names, in.DefaultModel)
		}
		out[name] = map[string]string{}
		for _, mname := range names {
			if mname = strings.TrimSpace(mname); mname == "" {
				continue
			}
			mid := uuid.NewString()
			if err := st.UpsertProviderModel(ctx, domain.ProviderModel{
				ID: mid, ProviderID: pid, Name: mname, Enabled: true,
			}); err != nil {
				return nil, err
			}
			out[name][mname] = mid
		}
	}
	logger.Info("seeded providers from environment", "providers", len(in.Providers))
	return out, nil
}

func seedRoles(ctx context.Context, st store.Store, in SeedInput, provModels map[string]map[string]string, logger *slog.Logger) error {
	existing, err := st.ListRoles(ctx)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	if len(in.Roles) == 0 {
		return nil
	}
	defaultModel := pickModel("", provModels, in.DefaultProvider, in.DefaultModel)
	if defaultModel == "" {
		logger.Warn("no models available; skipping role seeding (configure providers first)")
		return nil
	}
	for _, pb := range in.Roles {
		rid := uuid.NewString()
		control := pb.Control
		if control == "" {
			control = playbook.ControlSupervisor
		}
		role := domain.Role{
			ID: rid, Key: pb.ID, Title: pb.Title, Control: control,
			Guidelines: pb.Guidelines, Inputs: pb.Inputs, Outputs: pb.Outputs, Enabled: true,
		}
		if err := st.UpsertRole(ctx, role); err != nil {
			return err
		}
		for i, r := range pb.Roles {
			mid := pickModel(r.Model, provModels, in.DefaultProvider, in.DefaultModel)
			if mid == "" {
				mid = defaultModel
			}
			g := domain.RoleGrade{
				ID: uuid.NewString(), RoleID: rid, Grade: r.Tier, Rank: rankFor(r.Tier, i),
				ModelID: mid, Tools: r.Tools, Enabled: true,
			}
			if err := st.UpsertRoleGrade(ctx, g); err != nil {
				return err
			}
		}
	}
	logger.Info("seeded roles from builtin playbooks", "roles", len(in.Roles))
	return nil
}

func seedBrains(ctx context.Context, st store.Store, in SeedInput, provModels map[string]map[string]string, logger *slog.Logger) error {
	defaultModel := pickModel("", provModels, in.DefaultProvider, in.DefaultModel)
	if defaultModel == "" {
		return nil
	}
	names := []string{domain.BrainController, domain.BrainPlanner, domain.BrainInterviewer, domain.BrainCritic, domain.BrainSupervisor}
	for _, name := range names {
		if _, err := st.GetBrainConfig(ctx, name); err == nil {
			continue
		}
		if err := st.UpsertBrainConfig(ctx, domain.BrainConfig{Name: name, ModelID: defaultModel}); err != nil {
			return err
		}
	}
	return nil
}

func pickModel(ref string, provModels map[string]map[string]string, defaultProv, defaultModel string) string {
	if prov, name, ok := splitRef(ref); ok {
		if id, ok := provModels[prov][name]; ok {
			return id
		}
	}
	if id, ok := provModels[defaultProv][defaultModel]; ok {
		return id
	}
	for _, models := range provModels {
		for _, id := range models {
			return id
		}
	}
	return ""
}

func rankFor(grade string, fallback int) int {
	switch strings.ToLower(grade) {
	case "junior":
		return 0
	case "middle", "mid":
		return 1
	case "senior":
		return 2
	case "lead", "principal":
		return 3
	}
	return fallback
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
