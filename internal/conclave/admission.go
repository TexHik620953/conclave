package conclave

import (
	"sync"

	"github.com/texhik/conclave/internal/config"
)

type admission struct {
	mu        sync.Mutex
	global    chan struct{}
	providers map[string]chan struct{}
	workers   map[string]chan struct{}
}

func newAdmission(p config.Profile, cfg *config.Config) *admission {
	max := p.Limits.MaxParallel
	if max <= 0 {
		max = 8
	}
	a := &admission{
		global:    make(chan struct{}, max),
		providers: map[string]chan struct{}{},
		workers:   map[string]chan struct{}{},
	}
	for name, provider := range cfg.Providers {
		limit := provider.MaxParallel
		if limit <= 0 {
			limit = max
		}
		a.providers[name] = make(chan struct{}, limit)
	}
	for _, w := range p.Workers {
		limit := w.Instances
		if limit <= 0 {
			limit = 1
		}
		a.workers[w.ID] = make(chan struct{}, limit)
	}
	return a
}

// tryAcquire is deliberately non-blocking. The run loop retries on its tick,
// which keeps pause/cancel responsive and lets other runs use freed slots.
func (a *admission) tryAcquire(provider, worker string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	select {
	case a.global <- struct{}{}:
	default:
		return false
	}
	pc := a.providers[provider]
	if pc != nil {
		select {
		case pc <- struct{}{}:
		default:
			<-a.global
			return false
		}
	}
	wc := a.workers[worker]
	if wc != nil {
		select {
		case wc <- struct{}{}:
		default:
			if pc != nil {
				<-pc
			}
			<-a.global
			return false
		}
	}
	return true
}

func (a *admission) release(provider, worker string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if wc := a.workers[worker]; wc != nil {
		select {
		case <-wc:
		default:
		}
	}
	if pc := a.providers[provider]; pc != nil {
		select {
		case <-pc:
		default:
		}
	}
	select {
	case <-a.global:
	default:
	}
}
