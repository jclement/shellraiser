package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// portStore persists per-project port mappings (container port → host port) so
// they survive worker restarts — re-applied on register, like worktree colors.
type portStore struct {
	mu   sync.Mutex
	path string
	m    map[string]map[int]int // id → containerPort → hostPort
}

func newPortStore(dir string) *portStore {
	s := &portStore{path: filepath.Join(dir, "portmaps.json"), m: map[string]map[int]int{}}
	if b, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(b, &s.m)
	}
	return s
}

func (s *portStore) get(id string) map[int]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[int]int{}
	for k, v := range s.m[id] {
		out[k] = v
	}
	return out
}

func (s *portStore) set(id string, container, host int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m[id] == nil {
		s.m[id] = map[int]int{}
	}
	s.m[id][container] = host
	s.saveLocked()
}

func (s *portStore) del(id string, container int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m[id] != nil {
		delete(s.m[id], container)
		if len(s.m[id]) == 0 {
			delete(s.m, id)
		}
	}
	s.saveLocked()
}

// delWorker drops every remembered mapping for a worker (on nuke), so a future
// same-id project never re-binds stale ports.
func (s *portStore) delWorker(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[id]; ok {
		delete(s.m, id)
		s.saveLocked()
	}
}

func (s *portStore) saveLocked() {
	b, _ := json.MarshalIndent(s.m, "", "  ")
	_ = os.WriteFile(s.path, b, 0o600)
}
