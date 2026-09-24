package conf

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/texhik/conclave/conclave-core/internal/llmgw"
	"github.com/texhik/conclave/conclave-core/internal/llmgw/openai"
	"github.com/texhik/conclave/conclave-core/internal/playbook"
	"github.com/texhik/conclave/conclave-core/internal/store"
)

var errNotLoaded = errors.New("conf: configuration is not loaded")

// Manager owns the current configuration snapshot and reloads it when the
// database version changes. It implements llmgw.Client/StreamingClient and
// playbook.Provider.
type Manager struct {
	st      store.Store
	timeout time.Duration
	logger  *slog.Logger

	snap atomic.Pointer[Snapshot]
}

// New creates a configuration manager.
func New(st store.Store, timeout time.Duration, logger *slog.Logger) *Manager {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{st: st, timeout: timeout, logger: logger}
}

// Reload reads the configuration and atomically swaps the snapshot. On error the
// previous snapshot is kept.
func (m *Manager) Reload(ctx context.Context) error {
	snap, err := build(ctx, m.st, m.logger, func(baseURL, key string) llmgw.Client {
		return llmgw.Retry{
			Client:    openai.New(baseURL, key, m.timeout),
			Attempts:  3,
			BaseDelay: 500 * time.Millisecond,
			MaxDelay:  10 * time.Second,
		}
	})
	if err != nil {
		return err
	}
	prev := m.snap.Swap(snap)
	if prev == nil || prev.Version != snap.Version {
		m.logger.Info("configuration loaded", "version", snap.Version,
			"providers", len(snap.Providers), "roles", len(snap.Roles), "brains", len(snap.Brains))
	}
	return nil
}

// Current returns the active snapshot (may be nil before the first reload).
func (m *Manager) Current() *Snapshot { return m.snap.Load() }

// Run polls the config version until the context is cancelled.
func (m *Manager) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.reloadIfChanged(ctx)
		}
	}
}

func (m *Manager) reloadIfChanged(ctx context.Context) {
	ver, err := m.st.ConfigVersion(ctx)
	if err != nil {
		m.logger.Warn("config version check failed", "error", err.Error())
		return
	}
	if cur := m.snap.Load(); cur != nil && cur.Version == ver {
		return
	}
	if err := m.Reload(ctx); err != nil {
		m.logger.Warn("config reload failed; keeping previous snapshot", "error", err.Error())
	}
}

// --- playbook.Provider ---

// List implements playbook.Provider.
func (m *Manager) List() []playbook.Playbook {
	if s := m.snap.Load(); s != nil {
		return s.Playbooks()
	}
	return nil
}

// Get implements playbook.Provider.
func (m *Manager) Get(id string) (playbook.Playbook, bool) {
	if s := m.snap.Load(); s != nil {
		return s.GetPlaybook(id)
	}
	return playbook.Playbook{}, false
}

// ModelResolver: top-level brain models.
func (m *Manager) BrainModel(name string) string {
	if s := m.snap.Load(); s != nil {
		return s.BrainModel(name)
	}
	return ""
}

// BrainMaxSteps returns the configured step cap for a brain.
func (m *Manager) BrainMaxSteps(name string) int {
	if s := m.snap.Load(); s != nil {
		return s.BrainMaxSteps(name)
	}
	return 0
}

// Price returns per-million prices for a model reference.
func (m *Manager) Price(model string) (float64, float64) {
	if s := m.snap.Load(); s != nil {
		return s.Price(model)
	}
	return 0, 0
}

// --- llmgw.Client ---

// Complete implements llmgw.Client.
func (m *Manager) Complete(ctx context.Context, req llmgw.Request) (llmgw.Response, error) {
	s := m.snap.Load()
	if s == nil {
		return llmgw.Response{}, errNotLoaded
	}
	client, model, err := s.resolveRef(req.Model)
	if err != nil {
		return llmgw.Response{}, err
	}
	req.Model = model
	return client.Complete(ctx, req)
}

// Stream implements llmgw.StreamingClient.
func (m *Manager) Stream(ctx context.Context, req llmgw.Request, onDelta func(llmgw.Delta)) (llmgw.Response, error) {
	s := m.snap.Load()
	if s == nil {
		return llmgw.Response{}, errNotLoaded
	}
	client, model, err := s.resolveRef(req.Model)
	if err != nil {
		return llmgw.Response{}, err
	}
	req.Model = model
	return llmgw.Stream(ctx, client, req, onDelta)
}
