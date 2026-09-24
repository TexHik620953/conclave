// Package conf loads the database-backed configuration into an immutable
// snapshot and hot-reloads it in every replica. It also acts as the LLM gateway:
// model references are "provider/model" resolved against the current snapshot.
package conf

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/llmgw"
	"github.com/texhik/conclave/conclave-core/internal/playbook"
	"github.com/texhik/conclave/conclave-core/internal/store"
)

// Snapshot is an immutable view of the configuration at one version.
type Snapshot struct {
	Version int64

	Providers map[string]domain.Provider      // by name (enabled only)
	Models    map[string]domain.ProviderModel // by id
	ModelRefs map[string]string               // model id -> "provider/model"
	Roles     map[string]domain.Role          // by key (enabled only)
	Grades    map[string][]domain.RoleGrade   // by role id (enabled only)
	Brains    map[string]domain.BrainConfig   // by name

	clients         map[string]llmgw.Client
	defaultProvider string
}

// build reads the whole configuration from the store and builds a snapshot.
func build(ctx context.Context, st store.Store, logger *slog.Logger, newClient func(baseURL, key string) llmgw.Client) (*Snapshot, error) {
	version, err := st.ConfigVersion(ctx)
	if err != nil {
		return nil, err
	}
	provs, err := st.ListProviders(ctx)
	if err != nil {
		return nil, err
	}
	models, err := st.ListProviderModels(ctx)
	if err != nil {
		return nil, err
	}
	roles, err := st.ListRoles(ctx)
	if err != nil {
		return nil, err
	}
	grades, err := st.ListRoleGrades(ctx)
	if err != nil {
		return nil, err
	}
	brains, err := st.ListBrainConfigs(ctx)
	if err != nil {
		return nil, err
	}

	snap := &Snapshot{
		Version:   version,
		Providers: map[string]domain.Provider{},
		Models:    map[string]domain.ProviderModel{},
		ModelRefs: map[string]string{},
		Roles:     map[string]domain.Role{},
		Grades:    map[string][]domain.RoleGrade{},
		Brains:    map[string]domain.BrainConfig{},
		clients:   map[string]llmgw.Client{},
	}

	providerName := map[string]string{} // provider id -> name
	loaded := map[string]bool{}         // provider id -> client built
	for _, p := range provs {
		providerName[p.ID] = p.Name
		if !p.Enabled {
			continue
		}
		snap.Providers[p.Name] = p
		snap.clients[p.Name] = newClient(p.BaseURL, p.APIKey)
		loaded[p.ID] = true
	}

	for _, m := range models {
		if !m.Enabled || !loaded[m.ProviderID] {
			continue
		}
		snap.Models[m.ID] = m
		if name, ok := providerName[m.ProviderID]; ok {
			snap.ModelRefs[m.ID] = name + "/" + m.Name
		}
	}

	for _, r := range roles {
		if r.Enabled {
			snap.Roles[r.Key] = r
		}
	}
	for _, g := range grades {
		if g.Enabled {
			snap.Grades[g.RoleID] = append(snap.Grades[g.RoleID], g)
		}
	}
	for id := range snap.Grades {
		gs := snap.Grades[id]
		sort.Slice(gs, func(i, j int) bool { return gs[i].Rank < gs[j].Rank })
		snap.Grades[id] = gs
	}
	for _, b := range brains {
		snap.Brains[b.Name] = b
	}

	// Deterministic default provider: one enabled provider, or one named "default".
	if p, ok := snap.Providers["default"]; ok {
		snap.defaultProvider = p.Name
	} else if len(snap.Providers) == 1 {
		for name := range snap.Providers {
			snap.defaultProvider = name
		}
	}
	return snap, nil
}

// Playbooks materializes the runtime role templates expected by the orchestrator.
func (s *Snapshot) Playbooks() []playbook.Playbook {
	out := make([]playbook.Playbook, 0, len(s.Roles))
	for _, r := range s.Roles {
		pb := playbook.Playbook{
			ID: r.Key, Version: 1, Title: r.Title,
			Inputs: r.Inputs, Outputs: r.Outputs, Control: r.Control, Guidelines: r.Guidelines,
		}
		for _, g := range s.Grades[r.ID] {
			pb.Roles = append(pb.Roles, playbook.Role{
				Tier: g.Grade, Model: s.ModelRefs[g.ModelID], Tools: g.Tools,
			})
		}
		out = append(out, pb)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// GetPlaybook returns a runtime role template by key.
func (s *Snapshot) GetPlaybook(key string) (playbook.Playbook, bool) {
	r, ok := s.Roles[key]
	if !ok {
		return playbook.Playbook{}, false
	}
	pb := playbook.Playbook{
		ID: r.Key, Version: 1, Title: r.Title,
		Inputs: r.Inputs, Outputs: r.Outputs, Control: r.Control, Guidelines: r.Guidelines,
	}
	for _, g := range s.Grades[r.ID] {
		pb.Roles = append(pb.Roles, playbook.Role{
			Tier: g.Grade, Model: s.ModelRefs[g.ModelID], Tools: g.Tools,
		})
	}
	return pb, true
}

// BrainModel returns the model reference for a top-level brain.
func (s *Snapshot) BrainModel(name string) string {
	if b, ok := s.Brains[name]; ok {
		return s.ModelRefs[b.ModelID]
	}
	return ""
}

// BrainMaxSteps returns the configured step cap for a brain (0 = unset).
func (s *Snapshot) BrainMaxSteps(name string) int {
	if b, ok := s.Brains[name]; ok {
		return b.MaxSteps
	}
	return 0
}

// Price returns per-million input/output prices for a model reference.
func (s *Snapshot) Price(model string) (float64, float64) {
	prov, name, ok := splitRef(model)
	if !ok {
		return 0, 0
	}
	for _, m := range s.Models {
		if m.Name == name {
			if pn, ok := s.providerName(m.ProviderID); ok && pn == prov {
				return m.InputPricePer1M, m.OutputPricePer1M
			}
		}
	}
	return 0, 0
}

func (s *Snapshot) providerName(id string) (string, bool) {
	for name, p := range s.Providers {
		if p.ID == id {
			return name, true
		}
	}
	return "", false
}

func splitRef(ref string) (string, string, bool) {
	i := strings.IndexByte(ref, '/')
	if i < 0 {
		return "", ref, false
	}
	return ref[:i], ref[i+1:], true
}

// resolveRef returns the client and bare model name for a model reference.
func (s *Snapshot) resolveRef(ref string) (llmgw.Client, string, error) {
	if prov, name, ok := splitRef(ref); ok {
		if c, ok := s.clients[prov]; ok {
			return c, name, nil
		}
	}
	if s.defaultProvider != "" {
		if c, ok := s.clients[s.defaultProvider]; ok {
			return c, ref, nil
		}
	}
	if len(s.clients) == 1 {
		for _, c := range s.clients {
			return c, ref, nil
		}
	}
	return nil, "", fmt.Errorf("conf: no provider for model %q", ref)
}
