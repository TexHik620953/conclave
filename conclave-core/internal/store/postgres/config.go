package postgres

import (
	"context"
	"encoding/json"

	"github.com/texhik/conclave/conclave-core/internal/domain"
)

// --- Configuration (hot-reloadable) ---

func (s *Store) ConfigVersion(ctx context.Context) (int64, error) {
	var v int64
	err := s.pool.QueryRow(ctx, `SELECT version FROM config_version WHERE id=1`).Scan(&v)
	if err != nil {
		if _, insErr := s.pool.Exec(ctx, `INSERT INTO config_version (id, version) VALUES (1,1) ON CONFLICT DO NOTHING`); insErr != nil {
			return 0, insErr
		}
		return 1, nil
	}
	return v, nil
}

func jsonBytes(v any) []byte {
	if v == nil {
		return []byte("[]")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("[]")
	}
	return b
}

func jsonMap(v map[string]string) []byte {
	if len(v) == 0 {
		return []byte("{}")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}

func (s *Store) ListProviders(ctx context.Context) ([]domain.Provider, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, base_url, api_key, headers, enabled, created_at, updated_at
		 FROM providers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Provider
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *Store) GetProvider(ctx context.Context, id string) (*domain.Provider, error) {
	return scanProvider(s.pool.QueryRow(ctx,
		`SELECT id, name, base_url, api_key, headers, enabled, created_at, updated_at
		 FROM providers WHERE id=$1`, id))
}

func (s *Store) GetProviderByName(ctx context.Context, name string) (*domain.Provider, error) {
	return scanProvider(s.pool.QueryRow(ctx,
		`SELECT id, name, base_url, api_key, headers, enabled, created_at, updated_at
		 FROM providers WHERE name=$1`, name))
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanProvider(row rowScanner) (*domain.Provider, error) {
	var p domain.Provider
	var headers []byte
	if err := row.Scan(&p.ID, &p.Name, &p.BaseURL, &p.APIKey, &headers, &p.Enabled, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, mapNotFound(err)
	}
	_ = json.Unmarshal(headers, &p.Headers)
	return &p, nil
}

func (s *Store) UpsertProvider(ctx context.Context, p domain.Provider) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO providers (id, name, base_url, api_key, headers, enabled, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,now(),now())
		 ON CONFLICT (id) DO UPDATE SET
		   name=excluded.name, base_url=excluded.base_url,
		   api_key=CASE WHEN excluded.api_key <> '' THEN excluded.api_key ELSE providers.api_key END,
		   headers=excluded.headers, enabled=excluded.enabled, updated_at=now()`,
		p.ID, p.Name, p.BaseURL, p.APIKey, jsonMap(p.Headers), p.Enabled)
	return err
}

func (s *Store) DeleteProvider(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM providers WHERE id=$1`, id)
	return err
}

func (s *Store) ListProviderModels(ctx context.Context) ([]domain.ProviderModel, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, provider_id, name, display_name, context_window, input_price_per_1m, output_price_per_1m,
		        enabled, created_at, updated_at
		 FROM provider_models ORDER BY provider_id, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ProviderModel
	for rows.Next() {
		var m domain.ProviderModel
		if err := rows.Scan(&m.ID, &m.ProviderID, &m.Name, &m.DisplayName, &m.ContextWindow,
			&m.InputPricePer1M, &m.OutputPricePer1M, &m.Enabled, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) GetProviderModel(ctx context.Context, id string) (*domain.ProviderModel, error) {
	var m domain.ProviderModel
	err := s.pool.QueryRow(ctx,
		`SELECT id, provider_id, name, display_name, context_window, input_price_per_1m, output_price_per_1m,
		        enabled, created_at, updated_at
		 FROM provider_models WHERE id=$1`, id).
		Scan(&m.ID, &m.ProviderID, &m.Name, &m.DisplayName, &m.ContextWindow,
			&m.InputPricePer1M, &m.OutputPricePer1M, &m.Enabled, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return &m, nil
}

func (s *Store) UpsertProviderModel(ctx context.Context, m domain.ProviderModel) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO provider_models (id, provider_id, name, display_name, context_window,
		    input_price_per_1m, output_price_per_1m, enabled, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,now(),now())
		 ON CONFLICT (id) DO UPDATE SET
		   provider_id=excluded.provider_id, name=excluded.name, display_name=excluded.display_name,
		   context_window=excluded.context_window, input_price_per_1m=excluded.input_price_per_1m,
		   output_price_per_1m=excluded.output_price_per_1m, enabled=excluded.enabled, updated_at=now()`,
		m.ID, m.ProviderID, m.Name, m.DisplayName, m.ContextWindow,
		m.InputPricePer1M, m.OutputPricePer1M, m.Enabled)
	return err
}

func (s *Store) DeleteProviderModel(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM provider_models WHERE id=$1`, id)
	return err
}

func (s *Store) ListRoles(ctx context.Context) ([]domain.Role, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, key, title, description, control, guidelines, inputs, outputs, enabled, created_at, updated_at
		 FROM roles ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Role
	for rows.Next() {
		r, err := scanRole(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *Store) GetRole(ctx context.Context, id string) (*domain.Role, error) {
	return scanRole(s.pool.QueryRow(ctx,
		`SELECT id, key, title, description, control, guidelines, inputs, outputs, enabled, created_at, updated_at
		 FROM roles WHERE id=$1`, id))
}

func scanRole(row rowScanner) (*domain.Role, error) {
	var r domain.Role
	var inputs, outputs []byte
	if err := row.Scan(&r.ID, &r.Key, &r.Title, &r.Description, &r.Control, &r.Guidelines,
		&inputs, &outputs, &r.Enabled, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, mapNotFound(err)
	}
	_ = json.Unmarshal(inputs, &r.Inputs)
	_ = json.Unmarshal(outputs, &r.Outputs)
	return &r, nil
}

func (s *Store) UpsertRole(ctx context.Context, r domain.Role) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO roles (id, key, title, description, control, guidelines, inputs, outputs, enabled, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,now(),now())
		 ON CONFLICT (id) DO UPDATE SET
		   key=excluded.key, title=excluded.title, description=excluded.description, control=excluded.control,
		   guidelines=excluded.guidelines, inputs=excluded.inputs, outputs=excluded.outputs,
		   enabled=excluded.enabled, updated_at=now()`,
		r.ID, r.Key, r.Title, r.Description, r.Control, r.Guidelines, jsonBytes(r.Inputs), jsonBytes(r.Outputs), r.Enabled)
	return err
}

func (s *Store) DeleteRole(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM roles WHERE id=$1`, id)
	return err
}

func (s *Store) ListRoleGrades(ctx context.Context) ([]domain.RoleGrade, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, role_id, grade, rank, model_id, tools, system_prompt, enabled, created_at, updated_at
		 FROM role_grades ORDER BY role_id, rank`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.RoleGrade
	for rows.Next() {
		g, err := scanRoleGrade(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	return out, rows.Err()
}

func scanRoleGrade(row rowScanner) (*domain.RoleGrade, error) {
	var g domain.RoleGrade
	var tools []byte
	if err := row.Scan(&g.ID, &g.RoleID, &g.Grade, &g.Rank, &g.ModelID, &tools, &g.SystemPrompt,
		&g.Enabled, &g.CreatedAt, &g.UpdatedAt); err != nil {
		return nil, mapNotFound(err)
	}
	_ = json.Unmarshal(tools, &g.Tools)
	return &g, nil
}

func (s *Store) UpsertRoleGrade(ctx context.Context, g domain.RoleGrade) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO role_grades (id, role_id, grade, rank, model_id, tools, system_prompt, enabled, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,now(),now())
		 ON CONFLICT (id) DO UPDATE SET
		   role_id=excluded.role_id, grade=excluded.grade, rank=excluded.rank, model_id=excluded.model_id,
		   tools=excluded.tools, system_prompt=excluded.system_prompt, enabled=excluded.enabled, updated_at=now()`,
		g.ID, g.RoleID, g.Grade, g.Rank, g.ModelID, jsonBytes(g.Tools), g.SystemPrompt, g.Enabled)
	return err
}

func (s *Store) DeleteRoleGrade(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM role_grades WHERE id=$1`, id)
	return err
}

func (s *Store) ListBrainConfigs(ctx context.Context) ([]domain.BrainConfig, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT name, model_id, system_prompt, max_steps, updated_at FROM brain_configs ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.BrainConfig
	for rows.Next() {
		var b domain.BrainConfig
		if err := rows.Scan(&b.Name, &b.ModelID, &b.SystemPrompt, &b.MaxSteps, &b.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) GetBrainConfig(ctx context.Context, name string) (*domain.BrainConfig, error) {
	var b domain.BrainConfig
	err := s.pool.QueryRow(ctx,
		`SELECT name, model_id, system_prompt, max_steps, updated_at FROM brain_configs WHERE name=$1`, name).
		Scan(&b.Name, &b.ModelID, &b.SystemPrompt, &b.MaxSteps, &b.UpdatedAt)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return &b, nil
}

func (s *Store) UpsertBrainConfig(ctx context.Context, b domain.BrainConfig) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO brain_configs (name, model_id, system_prompt, max_steps, updated_at)
		 VALUES ($1,$2,$3,$4,now())
		 ON CONFLICT (name) DO UPDATE SET
		   model_id=excluded.model_id, system_prompt=excluded.system_prompt,
		   max_steps=excluded.max_steps, updated_at=now()`,
		b.Name, b.ModelID, b.SystemPrompt, b.MaxSteps)
	return err
}

func (s *Store) DeleteBrainConfig(ctx context.Context, name string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM brain_configs WHERE name=$1`, name)
	return err
}
