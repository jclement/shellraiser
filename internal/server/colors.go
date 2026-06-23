package server

import (
	"encoding/json"
	"os"
	"sync"
)

// wtMeta is per-worktree UI metadata (not tracked by git).
type wtMeta struct {
	Color string `json:"color,omitempty"`
	Name  string `json:"name,omitempty"`  // custom display name, independent of branch
	Order int    `json:"order,omitempty"` // manual sort order (lower = higher in the list)
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

func (s *metaStore) get(path string) (color, name string, order int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.m[path]; m != nil {
		return m.Color, m.Name, m.Order
	}
	return "", "", 0
}

func (s *metaStore) setColor(path, color string) { s.update(path, func(m *wtMeta) { m.Color = color }) }
func (s *metaStore) setName(path, name string)   { s.update(path, func(m *wtMeta) { m.Name = name }) }

// setOrder assigns explicit positions to the given paths (index → order, 1-based
// so 0 means "unset" and sorts after ordered ones).
func (s *metaStore) setOrder(paths []string) {
	for i, p := range paths {
		order := i + 1
		s.update(p, func(m *wtMeta) { m.Order = order })
	}
}

func (s *metaStore) update(path string, fn func(*wtMeta)) {
	s.mu.Lock()
	m := s.m[path]
	if m == nil {
		m = &wtMeta{}
		s.m[path] = m
	}
	fn(m)
	if m.Color == "" && m.Name == "" && m.Order == 0 {
		delete(s.m, path)
	}
	b, _ := json.MarshalIndent(s.m, "", "  ")
	s.mu.Unlock()
	_ = os.WriteFile(s.path, b, 0o600)
}
