package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/texhik/conclave/conclave-core/internal/domain"
)

// adminRoutes registers the configuration management API (admin key required).
func (s *Server) adminRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /v1/admin/providers", s.admin(s.handleListProviders))
	m.HandleFunc("POST /v1/admin/providers", s.admin(s.handleCreateProvider))
	m.HandleFunc("PUT /v1/admin/providers/{id}", s.admin(s.handleUpdateProvider))
	m.HandleFunc("DELETE /v1/admin/providers/{id}", s.admin(s.handleDeleteProvider))

	m.HandleFunc("GET /v1/admin/providers/{id}/models", s.admin(s.handleListModels))
	m.HandleFunc("POST /v1/admin/providers/{id}/models", s.admin(s.handleCreateModel))
	m.HandleFunc("PUT /v1/admin/models/{id}", s.admin(s.handleUpdateModel))
	m.HandleFunc("DELETE /v1/admin/models/{id}", s.admin(s.handleDeleteModel))

	m.HandleFunc("GET /v1/admin/roles", s.admin(s.handleAdminListRoles))
	m.HandleFunc("POST /v1/admin/roles", s.admin(s.handleCreateRole))
	m.HandleFunc("PUT /v1/admin/roles/{id}", s.admin(s.handleUpdateRole))
	m.HandleFunc("DELETE /v1/admin/roles/{id}", s.admin(s.handleDeleteRole))

	m.HandleFunc("GET /v1/admin/roles/{id}/grades", s.admin(s.handleListGrades))
	m.HandleFunc("POST /v1/admin/roles/{id}/grades", s.admin(s.handleCreateGrade))
	m.HandleFunc("PUT /v1/admin/grades/{id}", s.admin(s.handleUpdateGrade))
	m.HandleFunc("DELETE /v1/admin/grades/{id}", s.admin(s.handleDeleteGrade))

	m.HandleFunc("GET /v1/admin/brain-configs", s.admin(s.handleListBrains))
	m.HandleFunc("PUT /v1/admin/brain-configs/{name}", s.admin(s.handleUpsertBrain))
	m.HandleFunc("DELETE /v1/admin/brain-configs/{name}", s.admin(s.handleDeleteBrain))
}

// --- providers ---

func (s *Server) handleListProviders(w http.ResponseWriter, r *http.Request) {
	ps, err := s.deps.Store.ListProviders(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, nonNilProviders(ps))
}

type providerBody struct {
	Name    string            `json:"name"`
	BaseURL string            `json:"base_url"`
	APIKey  string            `json:"api_key"`
	Headers map[string]string `json:"headers"`
	Enabled *bool             `json:"enabled"`
}

func (s *Server) handleCreateProvider(w http.ResponseWriter, r *http.Request) {
	var body providerBody
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(body.Name) == "" || strings.TrimSpace(body.BaseURL) == "" {
		writeError(w, http.StatusBadRequest, "name and base_url are required")
		return
	}
	if _, err := s.deps.Store.GetProviderByName(r.Context(), body.Name); err == nil {
		writeError(w, http.StatusConflict, "provider name already exists")
		return
	}
	p := domain.Provider{
		ID: uuid.NewString(), Name: body.Name, BaseURL: body.BaseURL, APIKey: body.APIKey,
		Headers: body.Headers, Enabled: enabledOr(body.Enabled, true),
	}
	if err := s.deps.Store.UpsertProvider(r.Context(), p); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) handleUpdateProvider(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := s.deps.Store.GetProvider(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}
	var body providerBody
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Name != "" {
		existing.Name = body.Name
	}
	if body.BaseURL != "" {
		existing.BaseURL = body.BaseURL
	}
	if body.Headers != nil {
		existing.Headers = body.Headers
	}
	if body.Enabled != nil {
		existing.Enabled = *body.Enabled
	}
	if body.APIKey != "" {
		existing.APIKey = body.APIKey
	}
	if err := s.deps.Store.UpsertProvider(r.Context(), *existing); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, existing)
}

func (s *Server) handleDeleteProvider(w http.ResponseWriter, r *http.Request) {
	if err := s.deps.Store.DeleteProvider(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

// --- models ---

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("id")
	all, err := s.deps.Store.ListProviderModels(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]domain.ProviderModel, 0)
	for _, m := range all {
		if m.ProviderID == pid {
			out = append(out, m)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

type modelBody struct {
	Name             string   `json:"name"`
	DisplayName      string   `json:"display_name"`
	ContextWindow    int      `json:"context_window"`
	InputPricePer1M  float64  `json:"input_price_per_1m"`
	OutputPricePer1M float64  `json:"output_price_per_1m"`
	Enabled          *bool    `json:"enabled"`
	_                struct{} `json:"-"`
}

func (s *Server) handleCreateModel(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("id")
	if _, err := s.deps.Store.GetProvider(r.Context(), pid); err != nil {
		writeError(w, http.StatusBadRequest, "unknown provider")
		return
	}
	var body modelBody
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	m := domain.ProviderModel{
		ID: uuid.NewString(), ProviderID: pid, Name: body.Name, DisplayName: body.DisplayName,
		ContextWindow: body.ContextWindow, InputPricePer1M: body.InputPricePer1M,
		OutputPricePer1M: body.OutputPricePer1M, Enabled: enabledOr(body.Enabled, true),
	}
	if err := s.deps.Store.UpsertProviderModel(r.Context(), m); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) handleUpdateModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := s.deps.Store.GetProviderModel(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "model not found")
		return
	}
	var body modelBody
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Name != "" {
		existing.Name = body.Name
	}
	if body.DisplayName != "" {
		existing.DisplayName = body.DisplayName
	}
	existing.ContextWindow = body.ContextWindow
	existing.InputPricePer1M = body.InputPricePer1M
	existing.OutputPricePer1M = body.OutputPricePer1M
	if body.Enabled != nil {
		existing.Enabled = *body.Enabled
	}
	if err := s.deps.Store.UpsertProviderModel(r.Context(), *existing); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, existing)
}

func (s *Server) handleDeleteModel(w http.ResponseWriter, r *http.Request) {
	if err := s.deps.Store.DeleteProviderModel(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

// --- roles ---

func (s *Server) handleAdminListRoles(w http.ResponseWriter, r *http.Request) {
	view, err := s.rolesView(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view)
}

type roleBody struct {
	Key         string   `json:"key"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Control     string   `json:"control"`
	Guidelines  string   `json:"guidelines"`
	Inputs      []string `json:"inputs"`
	Outputs     []string `json:"outputs"`
	Enabled     *bool    `json:"enabled"`
}

func (s *Server) handleCreateRole(w http.ResponseWriter, r *http.Request) {
	var body roleBody
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(body.Key) == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}
	role := domain.Role{
		ID: uuid.NewString(), Key: body.Key, Title: body.Title, Description: body.Description,
		Control: controlOr(body.Control), Guidelines: body.Guidelines, Inputs: body.Inputs,
		Outputs: body.Outputs, Enabled: enabledOr(body.Enabled, true),
	}
	if err := s.deps.Store.UpsertRole(r.Context(), role); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, role)
}

func (s *Server) handleUpdateRole(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := s.deps.Store.GetRole(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "role not found")
		return
	}
	var body roleBody
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Key != "" {
		existing.Key = body.Key
	}
	if body.Title != "" {
		existing.Title = body.Title
	}
	existing.Description = body.Description
	if body.Control != "" {
		existing.Control = body.Control
	}
	existing.Guidelines = body.Guidelines
	if body.Inputs != nil {
		existing.Inputs = body.Inputs
	}
	if body.Outputs != nil {
		existing.Outputs = body.Outputs
	}
	if body.Enabled != nil {
		existing.Enabled = *body.Enabled
	}
	if err := s.deps.Store.UpsertRole(r.Context(), *existing); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, existing)
}

func (s *Server) handleDeleteRole(w http.ResponseWriter, r *http.Request) {
	if err := s.deps.Store.DeleteRole(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

// --- grades ---

func (s *Server) handleListGrades(w http.ResponseWriter, r *http.Request) {
	rid := r.PathValue("id")
	all, err := s.deps.Store.ListRoleGrades(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]domain.RoleGrade, 0)
	for _, g := range all {
		if g.RoleID == rid {
			out = append(out, g)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

type gradeBody struct {
	Grade        string   `json:"grade"`
	Rank         *int     `json:"rank"`
	ModelID      string   `json:"model_id"`
	Tools        []string `json:"tools"`
	SystemPrompt string   `json:"system_prompt"`
	Enabled      *bool    `json:"enabled"`
}

func (s *Server) handleCreateGrade(w http.ResponseWriter, r *http.Request) {
	rid := r.PathValue("id")
	if _, err := s.deps.Store.GetRole(r.Context(), rid); err != nil {
		writeError(w, http.StatusBadRequest, "unknown role")
		return
	}
	var body gradeBody
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(body.Grade) == "" || body.ModelID == "" {
		writeError(w, http.StatusBadRequest, "grade and model_id are required")
		return
	}
	if _, err := s.deps.Store.GetProviderModel(r.Context(), body.ModelID); err != nil {
		writeError(w, http.StatusBadRequest, "unknown model_id")
		return
	}
	g := domain.RoleGrade{
		ID: uuid.NewString(), RoleID: rid, Grade: body.Grade, Rank: rankOr(body.Rank, 0),
		ModelID: body.ModelID, Tools: body.Tools, SystemPrompt: body.SystemPrompt,
		Enabled: enabledOr(body.Enabled, true),
	}
	if err := s.deps.Store.UpsertRoleGrade(r.Context(), g); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

func (s *Server) handleUpdateGrade(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := s.deps.Store.ListRoleGrades(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var found *domain.RoleGrade
	for _, g := range existing {
		if g.ID == id {
			cp := g
			found = &cp
			break
		}
	}
	if found == nil {
		writeError(w, http.StatusNotFound, "grade not found")
		return
	}
	var body gradeBody
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Grade != "" {
		found.Grade = body.Grade
	}
	if body.Rank != nil {
		found.Rank = *body.Rank
	}
	if body.ModelID != "" {
		found.ModelID = body.ModelID
	}
	if body.Tools != nil {
		found.Tools = body.Tools
	}
	found.SystemPrompt = body.SystemPrompt
	if body.Enabled != nil {
		found.Enabled = *body.Enabled
	}
	if err := s.deps.Store.UpsertRoleGrade(r.Context(), *found); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, found)
}

func (s *Server) handleDeleteGrade(w http.ResponseWriter, r *http.Request) {
	if err := s.deps.Store.DeleteRoleGrade(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

// --- brain configs ---

func (s *Server) handleListBrains(w http.ResponseWriter, r *http.Request) {
	bs, err := s.deps.Store.ListBrainConfigs(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, bs)
}

type brainBody struct {
	ModelID      string `json:"model_id"`
	SystemPrompt string `json:"system_prompt"`
	MaxSteps     int    `json:"max_steps"`
}

func (s *Server) handleUpsertBrain(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body brainBody
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.ModelID == "" {
		writeError(w, http.StatusBadRequest, "model_id is required")
		return
	}
	if _, err := s.deps.Store.GetProviderModel(r.Context(), body.ModelID); err != nil {
		writeError(w, http.StatusBadRequest, "unknown model_id")
		return
	}
	b := domain.BrainConfig{
		Name: name, ModelID: body.ModelID, SystemPrompt: body.SystemPrompt,
		MaxSteps: body.MaxSteps, UpdatedAt: time.Now().UTC(),
	}
	if err := s.deps.Store.UpsertBrainConfig(r.Context(), b); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) handleDeleteBrain(w http.ResponseWriter, r *http.Request) {
	if err := s.deps.Store.DeleteBrainConfig(r.Context(), r.PathValue("name")); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

// --- read model ---

type gradeView struct {
	domain.RoleGrade
	Model string `json:"model"`
}

type roleView struct {
	domain.Role
	Grades []gradeView `json:"grades"`
}

// handleListRoles is the public read model of roles, grades and model refs.
func (s *Server) handleListRoles(w http.ResponseWriter, r *http.Request) {
	view, err := s.rolesView(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) rolesView(r *http.Request) ([]roleView, error) {
	roles, err := s.deps.Store.ListRoles(r.Context())
	if err != nil {
		return nil, err
	}
	grades, err := s.deps.Store.ListRoleGrades(r.Context())
	if err != nil {
		return nil, err
	}
	models, err := s.deps.Store.ListProviderModels(r.Context())
	if err != nil {
		return nil, err
	}
	provs, err := s.deps.Store.ListProviders(r.Context())
	if err != nil {
		return nil, err
	}
	provName := map[string]string{}
	for _, p := range provs {
		provName[p.ID] = p.Name
	}
	modelRef := map[string]string{}
	for _, m := range models {
		modelRef[m.ID] = provName[m.ProviderID] + "/" + m.Name
	}
	out := make([]roleView, 0, len(roles))
	for _, role := range roles {
		rv := roleView{Role: role, Grades: []gradeView{}}
		for _, g := range grades {
			if g.RoleID == role.ID {
				rv.Grades = append(rv.Grades, gradeView{RoleGrade: g, Model: modelRef[g.ModelID]})
			}
		}
		out = append(out, rv)
	}
	return out, nil
}

// --- helpers ---

func enabledOr(v *bool, def bool) bool {
	if v == nil {
		return def
	}
	return *v
}

func rankOr(v *int, def int) int {
	if v == nil {
		return def
	}
	return *v
}

func controlOr(v string) string {
	if v == "" {
		return "supervisor"
	}
	return v
}

func nonNilProviders(v []domain.Provider) []domain.Provider {
	if v == nil {
		return []domain.Provider{}
	}
	return v
}
