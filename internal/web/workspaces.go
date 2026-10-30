package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type WorkspaceRecord struct {
	Path       string    `json:"path"`
	Name       string    `json:"name"`
	Key        string    `json:"key"`
	LastOpened time.Time `json:"last_opened"`
	Exists     bool      `json:"exists"`
}

type workspaceRegistry struct {
	mu    sync.Mutex
	path  string
	items map[string]WorkspaceRecord
}

func newRegistry() *workspaceRegistry {
	r := &workspaceRegistry{items: map[string]WorkspaceRecord{}}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		r.path = filepath.Join(x, "conclave", "workspaces.json")
	} else {
		home, _ := os.UserHomeDir()
		r.path = filepath.Join(home, ".config", "conclave", "workspaces.json")
	}
	r.load()
	return r
}

func (r *workspaceRegistry) load() {
	b, err := os.ReadFile(r.path)
	if err != nil {
		return
	}
	var list []WorkspaceRecord
	if json.Unmarshal(b, &list) == nil {
		for _, item := range list {
			// Prune missing entries once at startup. The hot list endpoint does
			// not stat paths, which keeps it fast on external/FUSE mounts.
			if st, err := os.Stat(item.Path); err == nil && st.IsDir() {
				r.items[item.Path] = item
			}
		}
	}
}

func (r *workspaceRegistry) register(path, name, key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[path] = WorkspaceRecord{
		Path: path, Name: name, Key: key, LastOpened: time.Now(),
	}
	r.saveLocked()
}

func (r *workspaceRegistry) remove(path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.items, path)
	r.saveLocked()
}

func (r *workspaceRegistry) list() []WorkspaceRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]WorkspaceRecord, 0, len(r.items))
	for _, item := range r.items {
		// Do not stat workspace paths here. They may live on slow external or
		// network mounts; opening the workspace reports missing paths.
		item.Exists = true
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastOpened.After(out[j].LastOpened) })
	return out
}

func (r *workspaceRegistry) saveLocked() {
	list := make([]WorkspaceRecord, 0, len(r.items))
	for _, item := range r.items {
		list = append(list, item)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].LastOpened.After(list[j].LastOpened) })
	if len(list) > 50 {
		list = list[:50]
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(r.path, b, 0o600)
}
