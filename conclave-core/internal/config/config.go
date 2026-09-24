// Package config loads conclave-core runtime configuration from the environment.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the fully-resolved runtime configuration.
type Config struct {
	// ListenAddr is the HTTP/WebSocket bind address.
	ListenAddr string
	// DBDSN is the Postgres connection string. When empty, the in-memory store
	// is used (dev/tests only).
	DBDSN string
	// RedisAddr is optional; empty disables Redis-backed fanout.
	RedisAddr string
	// SecretKey signs device tokens and session secrets.
	SecretKey string
	// ShutdownTimeout bounds graceful shutdown.
	ShutdownTimeout time.Duration
	// LogLevel is one of debug|info|warn|error.
	LogLevel string

	// LLMBaseURL/LLMAPIKey/LLMModel configure the default OpenAI-compatible LLM
	// gateway. When LLMBaseURL is empty a deterministic fake is used.
	LLMBaseURL string
	LLMAPIKey  string
	LLMModel   string
	LLMTimeout time.Duration
	// DefaultProvider is the provider name used for model refs without a
	// "provider/" prefix. Defaults to "default".
	DefaultProvider string
	// Providers maps a provider name to an OpenAI-compatible endpoint. Role
	// models may reference them as "provider/model".
	Providers map[string]Provider

	// Quotas and limits.
	MaxParallel     int
	MaxPlanVersions int
	BudgetUSD       float64
	RateLimitRPS    float64
	RateLimitBurst  int
	// PriceInputPer1M / PriceOutputPer1M are default per-million-token prices
	// used for cost accounting and budget enforcement.
	PriceInputPer1M  float64
	PriceOutputPer1M float64
	// PlaybookDir loads extra playbook templates from a directory.
	PlaybookDir string
	// ConfigPoll is how often each replica checks the DB config version.
	ConfigPoll time.Duration
	// Fanout selects the realtime broker: "memory" (single node) or "redis".
	Fanout string
	// StreamHistorySize / StreamHistoryTTL configure Centrifuge channel history.
	StreamHistorySize int
	StreamHistoryTTL  time.Duration
	// DeltaFlush is how often coalesced token deltas are persisted/published.
	DeltaFlush time.Duration
	// ToolTimeout bounds a tool call to a local agent.
	ToolTimeout time.Duration

	// Workers is the number of background execution workers.
	Workers int
	// JobLease is how long a job claim is valid without a heartbeat.
	JobLease time.Duration
	// JobPoll is the worker idle poll interval.
	JobPoll time.Duration
}

// Provider is an OpenAI-compatible endpoint configured under CORE_PROVIDERS.
type Provider struct {
	BaseURL string            `json:"base_url"`
	APIKey  string            `json:"api_key"`
	Headers map[string]string `json:"headers,omitempty"`
	Models  []string          `json:"models,omitempty"`
}

// Load reads configuration from the environment, applying defaults.
func Load() (*Config, error) {
	c := &Config{
		ListenAddr:        env("CORE_LISTEN_ADDR", "127.0.0.1:8090"),
		DBDSN:             env("CORE_DB_DSN", ""),
		RedisAddr:         env("CORE_REDIS_ADDR", ""),
		SecretKey:         env("CORE_SECRET_KEY", ""),
		ShutdownTimeout:   10 * time.Second,
		LogLevel:          env("CORE_LOG_LEVEL", "info"),
		LLMBaseURL:        env("CORE_LLM_BASE_URL", ""),
		LLMAPIKey:         env("CORE_LLM_API_KEY", ""),
		LLMModel:          env("CORE_LLM_MODEL", "gpt-4o-mini"),
		LLMTimeout:        5 * time.Minute,
		MaxParallel:       envInt("CORE_MAX_PARALLEL", 4),
		MaxPlanVersions:   envInt("CORE_MAX_PLAN_VERSIONS", 5),
		BudgetUSD:         envFloat("CORE_BUDGET_USD", 0),
		RateLimitRPS:      envFloat("CORE_RATE_LIMIT_RPS", 0),
		RateLimitBurst:    envInt("CORE_RATE_LIMIT_BURST", 0),
		PriceInputPer1M:   envFloat("CORE_PRICE_INPUT_PER_1M", 0),
		PriceOutputPer1M:  envFloat("CORE_PRICE_OUTPUT_PER_1M", 0),
		PlaybookDir:       env("CORE_PLAYBOOK_DIR", ""),
		ConfigPoll:        envDuration("CORE_CONFIG_POLL", 15*time.Second),
		Fanout:            env("CORE_FANOUT", "memory"),
		StreamHistorySize: envInt("CORE_STREAM_HISTORY_SIZE", 1000),
		StreamHistoryTTL:  envDuration("CORE_STREAM_HISTORY_TTL", time.Hour),
		DeltaFlush:        envDuration("CORE_DELTA_FLUSH_MS", 75*time.Millisecond),
		ToolTimeout:       envDuration("CORE_TOOL_TIMEOUT", 60*time.Second),
		Workers:           envInt("CORE_WORKERS", 2),
		JobLease:          envDuration("CORE_JOB_LEASE", 60*time.Second),
		JobPoll:           envDuration("CORE_JOB_POLL", 500*time.Millisecond),
		DefaultProvider:   env("CORE_DEFAULT_PROVIDER", "default"),
	}
	if v := env("CORE_SHUTDOWN_TIMEOUT", ""); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("CORE_SHUTDOWN_TIMEOUT: %w", err)
		}
		c.ShutdownTimeout = d
	}
	if raw := env("CORE_PROVIDERS", ""); strings.TrimSpace(raw) != "" {
		var providers map[string]Provider
		if err := json.Unmarshal([]byte(raw), &providers); err != nil {
			return nil, fmt.Errorf("CORE_PROVIDERS: %w", err)
		}
		c.Providers = providers
	}
	// The single-endpoint legacy config becomes the default provider.
	if c.LLMBaseURL != "" {
		if c.Providers == nil {
			c.Providers = map[string]Provider{}
		}
		if _, ok := c.Providers[c.DefaultProvider]; !ok {
			c.Providers[c.DefaultProvider] = Provider{BaseURL: c.LLMBaseURL, APIKey: c.LLMAPIKey}
		}
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// Validate checks the configuration for consistency.
func (c *Config) Validate() error {
	if c.ListenAddr == "" {
		return fmt.Errorf("CORE_LISTEN_ADDR must not be empty")
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("CORE_LOG_LEVEL must be debug|info|warn|error, got %q", c.LogLevel)
	}
	switch c.Fanout {
	case "memory", "redis", "off":
	default:
		return fmt.Errorf("CORE_FANOUT must be memory|redis|off, got %q", c.Fanout)
	}
	if c.Fanout == "redis" && c.RedisAddr == "" {
		return fmt.Errorf("CORE_FANOUT=redis requires CORE_REDIS_ADDR")
	}
	return nil
}

// InMemory reports whether the in-memory store should be used.
func (c *Config) InMemory() bool { return strings.TrimSpace(c.DBDSN) == "" }

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
