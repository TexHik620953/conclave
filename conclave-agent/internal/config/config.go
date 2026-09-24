// Package config loads conclave-agent runtime configuration.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config is the agent runtime configuration.
type Config struct {
	// CoreURL is the conclave-core base URL, e.g. http://127.0.0.1:8090.
	CoreURL string
	// UserToken (cu_...) authenticates the client/data WebSocket.
	UserToken string
	// DeviceToken (cc_...) authenticates the agent WebSocket.
	DeviceToken string
	// Workspace is the directory tools operate in.
	Workspace string
	// ListenAddr is the local UI/API bind address.
	ListenAddr string
	// LogLevel is debug|info|warn|error.
	LogLevel string
	// CommandTimeout bounds shell/tool execution.
	CommandTimeout time.Duration
}

// Load reads configuration from the environment. Flags override via Apply.
func Load() (*Config, error) {
	c := &Config{
		CoreURL:        env("CONCLAVE_CORE_URL", "http://127.0.0.1:8090"),
		UserToken:      env("CONCLAVE_USER_TOKEN", ""),
		DeviceToken:    env("CONCLAVE_DEVICE_TOKEN", ""),
		Workspace:      env("CONCLAVE_WORKSPACE", ""),
		ListenAddr:     env("CONCLAVE_AGENT_ADDR", "127.0.0.1:7070"),
		LogLevel:       env("CONCLAVE_LOG_LEVEL", "info"),
		CommandTimeout: 2 * time.Minute,
	}
	if v := env("CONCLAVE_COMMAND_TIMEOUT", ""); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.CommandTimeout = d
		}
	}
	return c, nil
}

// Finalize normalizes the config (resolves the workspace) and validates it.
func (c *Config) Finalize() error {
	if c.Workspace == "" {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		c.Workspace = wd
	}
	if abs, err := filepath.Abs(c.Workspace); err == nil {
		c.Workspace = abs
	}
	if err := os.MkdirAll(c.Workspace, 0o755); err != nil {
		return fmt.Errorf("workspace %q: %w", c.Workspace, err)
	}
	if st, err := os.Stat(c.Workspace); err != nil || !st.IsDir() {
		return fmt.Errorf("workspace %q is not a directory", c.Workspace)
	}
	if c.CoreURL == "" {
		return fmt.Errorf("core URL is required")
	}
	c.CoreURL = strings.TrimRight(c.CoreURL, "/")
	if c.DeviceToken == "" {
		return fmt.Errorf("device token is required (--device-token)")
	}
	if c.ListenAddr == "" {
		return fmt.Errorf("listen address is required")
	}
	return nil
}

// WSURL returns the WebSocket URL for the given path.
func (c *Config) WSURL(path string) string {
	base := c.CoreURL
	base = strings.Replace(base, "https://", "wss://", 1)
	base = strings.Replace(base, "http://", "ws://", 1)
	return strings.TrimRight(base, "/") + path
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}
