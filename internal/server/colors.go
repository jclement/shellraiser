package server

import (
	"encoding/json"
	"os"
	"sync"
)

// wtMeta is per-worktree UI metadata (not tracked by git).
type wtMeta struct {
	Color string `json:"color,omitempty"`
	Name  string `json:"name,omitempty"` // custom display name, independent of branch
}

// metaStore persists per-worktree UI metadata, keyed by worktree path.
type metaStore struct {
	path string
	mu   sync.Mutex
	m    map[string]*wtMeta
}

func newMetaStore(path string) *metaStore {
	s := &metaStore{path: path, m: map[string]*wtMeta{}}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s.m)
	}
	return s
}

func (s *metaStore) get(path string) (color, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.m[path]; m != nil {
		return m.Color, m.Name
	}
	return "", ""
}

func (s *metaStore) setColor(path, color string) { s.update(path, func(m *wtMeta) { m.Color = color }) }
func (s *metaStore) setName(path, name string)   { s.update(path, func(m *wtMeta) { m.Name = name }) }

func (s *metaStore) update(path string, fn func(*wtMeta)) {
	s.mu.Lock()
	m := s.m[path]
	if m == nil {
		m = &wtMeta{}
		s.m[path] = m
	}
	fn(m)
	if m.Color == "" && m.Name == "" {
		delete(s.m, path)
	}
	b, _ := json.MarshalIndent(s.m, "", "  ")
	s.mu.Unlock()
	_ = os.WriteFile(s.path, b, 0o600)
}
