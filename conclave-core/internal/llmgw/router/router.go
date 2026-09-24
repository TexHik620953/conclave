// Package router resolves "provider/model" references to OpenAI-compatible
// clients so different roles can use different providers/models.
package router

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/texhik/conclave/conclave-core/internal/config"
	"github.com/texhik/conclave/conclave-core/internal/llmgw"
	"github.com/texhik/conclave/conclave-core/internal/llmgw/openai"
)

// Config configures the router.
type Config struct {
	Providers       map[string]config.Provider
	DefaultProvider string
	Timeout         time.Duration
}

// Router dispatches completion requests by provider prefix.
type Router struct {
	clients map[string]llmgw.Client
	def     llmgw.Client
	defName string
}

// New builds provider clients. Each provider is wrapped with retries.
func New(cfg Config) (*Router, error) {
	if len(cfg.Providers) == 0 {
		return nil, fmt.Errorf("router: no providers configured")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	clients := make(map[string]llmgw.Client, len(cfg.Providers))
	for name, p := range cfg.Providers {
		if p.BaseURL == "" {
			return nil, fmt.Errorf("router: provider %q has no base_url", name)
		}
		client := openai.New(p.BaseURL, p.APIKey, timeout)
		clients[name] = llmgw.Retry{Client: client, Attempts: 3, BaseDelay: 500 * time.Millisecond, MaxDelay: 10 * time.Second}
	}
	r := &Router{clients: clients, defName: cfg.DefaultProvider}
	if r.defName == "" {
		r.defName = "default"
	}
	if c, ok := clients[r.defName]; ok {
		r.def = c
	} else if len(clients) == 1 {
		for _, c := range clients {
			r.def = c
		}
	}
	return r, nil
}

// resolve splits a model reference and returns the client and model name.
func (r *Router) resolve(model string) (llmgw.Client, string, error) {
	if i := strings.IndexByte(model, '/'); i >= 0 {
		name, rest := model[:i], model[i+1:]
		if c, ok := r.clients[name]; ok {
			return c, rest, nil
		}
	}
	if r.def != nil {
		return r.def, model, nil
	}
	return nil, "", fmt.Errorf("router: no provider for model %q (configure CORE_PROVIDERS or prefix the model with a provider)", model)
}

// Complete implements llmgw.Client.
func (r *Router) Complete(ctx context.Context, req llmgw.Request) (llmgw.Response, error) {
	client, model, err := r.resolve(req.Model)
	if err != nil {
		return llmgw.Response{}, err
	}
	req.Model = model
	return client.Complete(ctx, req)
}

// Stream implements llmgw.StreamingClient.
func (r *Router) Stream(ctx context.Context, req llmgw.Request, onDelta func(llmgw.Delta)) (llmgw.Response, error) {
	client, model, err := r.resolve(req.Model)
	if err != nil {
		return llmgw.Response{}, err
	}
	req.Model = model
	return llmgw.Stream(ctx, client, req, onDelta)
}
