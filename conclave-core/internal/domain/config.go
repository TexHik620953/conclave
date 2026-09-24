package domain

import "time"

// Provider is an OpenAI-compatible LLM endpoint. APIKey is stored as-is and is
// never serialized to clients.
type Provider struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	BaseURL   string            `json:"base_url"`
	APIKey    string            `json:"-"`
	Headers   map[string]string `json:"headers,omitempty"`
	Enabled   bool              `json:"enabled"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// ProviderModel is a model served by a provider.
type ProviderModel struct {
	ID               string    `json:"id"`
	ProviderID       string    `json:"provider_id"`
	Name             string    `json:"name"`
	DisplayName      string    `json:"display_name,omitempty"`
	ContextWindow    int       `json:"context_window,omitempty"`
	InputPricePer1M  float64   `json:"input_price_per_1m,omitempty"`
	OutputPricePer1M float64   `json:"output_price_per_1m,omitempty"`
	Enabled          bool      `json:"enabled"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// Role is a reusable function (backend, frontend, dba, ...) with graded staff.
type Role struct {
	ID          string    `json:"id"`
	Key         string    `json:"key"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Control     string    `json:"control"`
	Guidelines  string    `json:"guidelines,omitempty"`
	Inputs      []string  `json:"inputs,omitempty"`
	Outputs     []string  `json:"outputs,omitempty"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// RoleGrade binds a grade (junior/middle/senior/...) of a role to a model.
type RoleGrade struct {
	ID           string    `json:"id"`
	RoleID       string    `json:"role_id"`
	Grade        string    `json:"grade"`
	Rank         int       `json:"rank"`
	ModelID      string    `json:"model_id"`
	Tools        []string  `json:"tools,omitempty"`
	SystemPrompt string    `json:"system_prompt,omitempty"`
	Enabled      bool      `json:"enabled"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Brain names for the top-level LLM roles.
const (
	BrainController  = "controller"
	BrainPlanner     = "planner"
	BrainInterviewer = "interviewer"
	BrainCritic      = "critic"
	BrainSupervisor  = "supervisor"
)

// BrainConfig is the model and prompt for a top-level brain.
type BrainConfig struct {
	Name         string    `json:"name"`
	ModelID      string    `json:"model_id"`
	SystemPrompt string    `json:"system_prompt,omitempty"`
	MaxSteps     int       `json:"max_steps,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
}
