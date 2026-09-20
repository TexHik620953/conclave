package provider

import (
	"fmt"
	"os"
	"sort"

	"github.com/texhik/conclave/internal/config"
)

// Registry holds configured provider clients.
type Registry struct {
	clients map[string]*Client
	order   []string
}

// NewRegistry instantiates a client for every configured provider.
func NewRegistry(providers map[string]config.Provider) (*Registry, error) {
	r := &Registry{clients: map[string]*Client{}}
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := providers[name]
		apiKey := p.APIKey
		if apiKey == "" && p.APIKeyEnv != "" {
			apiKey = os.Getenv(p.APIKeyEnv)
		}
		c := New(name, p.BaseURL, apiKey, p.Timeout.Duration(), WithHeaders(p.Headers))
		r.clients[name] = c
		r.order = append(r.order, name)
	}
	return r, nil
}

// Client returns the client for a provider name.
func (r *Registry) Client(name string) (*Client, error) {
	c, ok := r.clients[name]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q", name)
	}
	return c, nil
}

// Names returns configured provider names in sorted order.
func (r *Registry) Names() []string { return r.order }

// Target is a resolved provider/model pair.
type Target struct {
	Provider string
	Model    string
}

// Resolve splits a "provider/model" reference.
func Resolve(ref string) (Target, error) {
	prov, model, err := config.SplitModel(ref)
	if err != nil {
		return Target{}, err
	}
	return Target{Provider: prov, Model: model}, nil
}
