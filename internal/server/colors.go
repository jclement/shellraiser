package server

import (
	"encoding/json"
	"os"
	"sync"
)

// colorStore persists per-worktree UI color tags (keyed by worktree path).
type colorStore struct {
	path string
	mu   sync.Mutex
	m    map[string]string
}

func newColorStore(path string) *colorStore {
	c := &colorStore{path: path, m: map[string]string{}}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &c.m)
	}
	return c
}

func (c *colorStore) get(path string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[path]
}

func (c *colorStore) set(path, color string) {
	c.mu.Lock()
	if color == "" {
		delete(c.m, path)
	} else {
		c.m[path] = color
	}
	b, _ := json.MarshalIndent(c.m, "", "  ")
	c.mu.Unlock()
	_ = os.WriteFile(c.path, b, 0o600)
}
