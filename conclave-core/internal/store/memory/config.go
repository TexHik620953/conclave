package memory

import (
	"context"
	"sort"

	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/store"
)

// --- Configuration (hot-reloadable) ---

// ConfigVersion returns a monotonically increasing version bumped on every
// configuration write.
func (s *Store) ConfigVersion(_ context.Context) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.configVersion, nil
}

func (s *Store) ListProviders(_ context.Context) ([]domain.Provider, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Provider, 0, len(s.providers))
	for _, p := range s.providers {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Store) GetProvider(_ context.Context, id string) (*domain.Provider, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.providers[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &p, nil
}

func (s *Store) GetProviderByName(_ context.Context, name string) (*domain.Provider, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.providers {
		if p.Name == name {
			cp := p
			return &cp, nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *Store) UpsertProvider(_ context.Context, p domain.Provider) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.providers[p.ID]; ok {
		p.CreatedAt = existing.CreatedAt
		if p.APIKey == "" {
			p.APIKey = existing.APIKey
		}
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now()
	}
	p.UpdatedAt = now()
	s.providers[p.ID] = p
	s.configVersion++
	return nil
}

func (s *Store) DeleteProvider(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.providers, id)
	s.configVersion++
	return nil
}

func (s *Store) ListProviderModels(_ context.Context) ([]domain.ProviderModel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.ProviderModel, 0, len(s.providerModels))
	for _, m := range s.providerModels {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ProviderID != out[j].ProviderID {
			return out[i].ProviderID < out[j].ProviderID
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (s *Store) GetProviderModel(_ context.Context, id string) (*domain.ProviderModel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.providerModels[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &m, nil
}

func (s *Store) UpsertProviderModel(_ context.Context, m domain.ProviderModel) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.providerModels[m.ID]; ok {
		m.CreatedAt = existing.CreatedAt
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = now()
	}
	m.UpdatedAt = now()
	s.providerModels[m.ID] = m
	s.configVersion++
	return nil
}

func (s *Store) DeleteProviderModel(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.providerModels, id)
	s.configVersion++
	return nil
}

func (s *Store) ListRoles(_ context.Context) ([]domain.Role, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Role, 0, len(s.roles))
	for _, r := range s.roles {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (s *Store) GetRole(_ context.Context, id string) (*domain.Role, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.roles[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &r, nil
}

func (s *Store) UpsertRole(_ context.Context, r domain.Role) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.roles[r.ID]; ok {
		r.CreatedAt = existing.CreatedAt
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now()
	}
	r.UpdatedAt = now()
	s.roles[r.ID] = r
	s.configVersion++
	return nil
}

func (s *Store) DeleteRole(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.roles, id)
	s.configVersion++
	return nil
}

func (s *Store) ListRoleGrades(_ context.Context) ([]domain.RoleGrade, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.RoleGrade, 0, len(s.roleGrades))
	for _, g := range s.roleGrades {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RoleID != out[j].RoleID {
			return out[i].RoleID < out[j].RoleID
		}
		return out[i].Rank < out[j].Rank
	})
	return out, nil
}

func (s *Store) UpsertRoleGrade(_ context.Context, g domain.RoleGrade) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.roleGrades[g.ID]; ok {
		g.CreatedAt = existing.CreatedAt
	}
	if g.CreatedAt.IsZero() {
		g.CreatedAt = now()
	}
	g.UpdatedAt = now()
	s.roleGrades[g.ID] = g
	s.configVersion++
	return nil
}

func (s *Store) DeleteRoleGrade(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.roleGrades, id)
	s.configVersion++
	return nil
}

func (s *Store) ListBrainConfigs(_ context.Context) ([]domain.BrainConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.BrainConfig, 0, len(s.brains))
	for _, b := range s.brains {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Store) GetBrainConfig(_ context.Context, name string) (*domain.BrainConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.brains[name]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &b, nil
}

func (s *Store) UpsertBrainConfig(_ context.Context, b domain.BrainConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b.UpdatedAt = now()
	s.brains[b.Name] = b
	s.configVersion++
	return nil
}

func (s *Store) DeleteBrainConfig(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.brains, name)
	s.configVersion++
	return nil
}
