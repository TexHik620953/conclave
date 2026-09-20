// Package config defines conclave's on-disk configuration schema and loader.
package config

// Config is the fully-merged runtime configuration.
type Config struct {
	Providers map[string]Provider  `yaml:"providers"`
	Roles     map[string]Role      `yaml:"roles"`
	Pipelines map[string]Pipeline  `yaml:"pipelines"`
	MCP       map[string]MCPServer `yaml:"mcp"`
	Settings  Settings             `yaml:"settings"`

	// BaseDir is the directory of the highest-precedence config file that was
	// loaded. Relative prompt/artifact paths resolve against it.
	BaseDir string `yaml:"-"`
}

// Settings holds process-wide defaults.
type Settings struct {
	DefaultModel  string            `yaml:"default_model"`
	Controller    string            `yaml:"controller"`
	MaxParallel   int               `yaml:"max_parallel"`
	MaxIterations int               `yaml:"max_iterations"`
	MaxTokens     int               `yaml:"max_tokens"`
	MaxContext    int               `yaml:"max_context_bytes"`
	Timeout       Duration          `yaml:"timeout"`
	Workspace     string            `yaml:"workspace"`
	Env           map[string]string `yaml:"env"`
	Defaults      ToolDefaults      `yaml:"defaults"`
	ModelLimits   map[string]int    `yaml:"model_limits"`
	OnNoUser      string            `yaml:"on_no_user"`
	Retry         Retry             `yaml:"retry"`
	ModelPrices   map[string]Price  `yaml:"model_prices"`
	BudgetUSD     float64           `yaml:"budget_usd"`
	OnBudget      string            `yaml:"on_budget"`

	// SummarizeThreshold (0..1) triggers context summarization when the
	// estimated prompt size reaches this fraction of the model's limit.
	// Nil defaults to 0.8; set to 0 to disable.
	SummarizeThreshold *float64 `yaml:"summarize_threshold"`
	SummarizerModel    string   `yaml:"summarizer_model"`

	WebSearch WebSearch `yaml:"web_search"`
}

// WebSearch configures the web_search tool.
type WebSearch struct {
	Provider   string `yaml:"provider"` // exa | searxng
	BaseURL    string `yaml:"base_url"`
	APIKey     string `yaml:"api_key"`
	APIKeyEnv  string `yaml:"api_key_env"`
	MaxResults int    `yaml:"max_results"`
	SearchType string `yaml:"search_type"`
}

// Retry configures transient-error retries for provider calls.
type Retry struct {
	MaxAttempts int      `yaml:"max_attempts"`
	BaseDelay   Duration `yaml:"base_delay"`
	MaxDelay    Duration `yaml:"max_delay"`
}

// Price is per-million-token pricing for a model.
type Price struct {
	InputPer1M  float64 `yaml:"input_per_1m"`
	OutputPer1M float64 `yaml:"output_per_1m"`
}

// ToolDefaults are process-wide sandbox and permission defaults.
type ToolDefaults struct {
	FSRead          *bool    `yaml:"fs_read"`
	FSWrite         *bool    `yaml:"fs_write"`
	Shell           *bool    `yaml:"shell"`
	Network         *bool    `yaml:"network"`
	AllowedHosts    []string `yaml:"allowed_hosts"`
	AllowedCommands []string `yaml:"allowed_commands"`
	DeniedCommands  []string `yaml:"denied_commands"`
	CommandTimeout  Duration `yaml:"command_timeout"`
	MaxOutputBytes  int      `yaml:"max_output_bytes"`
}

// MCPServer configures an external MCP server whose tools roles may use.
type MCPServer struct {
	Command []string          `yaml:"command"`
	URL     string            `yaml:"url"`
	Headers map[string]string `yaml:"headers"`
	Env     map[string]string `yaml:"env"`
	Enabled *bool             `yaml:"enabled"`
}

// Provider describes an OpenAI-compatible endpoint.
type Provider struct {
	Type      string            `yaml:"type"`
	BaseURL   string            `yaml:"base_url"`
	APIKey    string            `yaml:"api_key"`
	APIKeyEnv string            `yaml:"api_key_env"`
	Headers   map[string]string `yaml:"headers"`
	Timeout   Duration          `yaml:"timeout"`
	Models    []string          `yaml:"models"`
}

// Role is an editable agent persona.
type Role struct {
	ID           string      `yaml:"id"`
	Title        string      `yaml:"title"`
	Description  string      `yaml:"description"`
	Model        string      `yaml:"model"`
	Fallback     []string    `yaml:"fallback"`
	SystemPrompt string      `yaml:"system_prompt"`
	PromptFile   string      `yaml:"prompt_file"`
	Temperature  *float64    `yaml:"temperature"`
	MaxTokens    int         `yaml:"max_tokens"`
	Tools        []string    `yaml:"tools"`
	Permissions  Permissions `yaml:"permissions"`
	Outputs      []string    `yaml:"outputs"`
}

// Permissions gates what a role may do. Nil means "inherit default".
type Permissions struct {
	FSRead  *bool `yaml:"fs_read"`
	FSWrite *bool `yaml:"fs_write"`
	Shell   *bool `yaml:"shell"`
	Network *bool `yaml:"network"`
}

// Allowed reports whether a permission is granted, using def when unset.
func (p Permissions) Allowed(name string, def bool) bool {
	var v *bool
	switch name {
	case "fs_read":
		v = p.FSRead
	case "fs_write":
		v = p.FSWrite
	case "shell":
		v = p.Shell
	case "network":
		v = p.Network
	}
	if v == nil {
		return def
	}
	return *v
}

// Pipeline is a declarative task graph with optional controller decisions.
type Pipeline struct {
	Description string           `yaml:"description"`
	Inputs      []string         `yaml:"inputs"`
	Start       string           `yaml:"start"`
	Nodes       []Node           `yaml:"nodes"`
	Edges       []Edge           `yaml:"edges"`
	Settings    PipelineSettings `yaml:"settings"`
}

// PipelineSettings overrides process defaults for a single pipeline.
type PipelineSettings struct {
	MaxParallel   int      `yaml:"max_parallel"`
	MaxIterations int      `yaml:"max_iterations"`
	Workspace     string   `yaml:"workspace"`
	Outputs       []string `yaml:"outputs"`
}

// Node is one unit of work in a pipeline.
type Node struct {
	ID      string   `yaml:"id"`
	Type    string   `yaml:"type"` // agent|parallel|controller|loop|gate|transform
	Role    string   `yaml:"role"`
	Roles   []string `yaml:"roles"`
	Model   string   `yaml:"model"`
	Prompt  string   `yaml:"prompt"`
	Tools   []string `yaml:"tools"`
	Inputs  []string `yaml:"inputs"`
	Output  string   `yaml:"output"`
	Choices []string `yaml:"choices"`

	// loop
	Until   string   `yaml:"until"`
	Body    []string `yaml:"body"`
	MaxIter int      `yaml:"max_iterations"`

	// gate
	Condition string `yaml:"condition"`
	Ask       bool   `yaml:"ask"`
	Else      string `yaml:"else"`

	// transform
	Template string `yaml:"template"`

	Next []string `yaml:"next"`
}

// Edge connects two nodes, optionally under a condition.
type Edge struct {
	From string `yaml:"from"`
	To   string `yaml:"to"`
	When string `yaml:"when"`
}
