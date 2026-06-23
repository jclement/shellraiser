package main

import (
	"sort"
	"strings"
	"sync"
)

// Registry holds the live set of workers. Docker is the source of truth: the
// coordinator reconciles this map from container labels on boot and on a timer,
// so a crash or `brew upgrade` re-adopts running workers with zero data loss.
type Registry struct {
	mu      sync.RWMutex
	workers map[string]*Worker // keyed by id
}

func newRegistry() *Registry { return &Registry{workers: map[string]*Worker{}} }

func (r *Registry) put(w *Worker) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.workers[w.ID] = w
}

func (r *Registry) get(id string) (*Worker, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	w, ok := r.workers[id]
	return w, ok
}

func (r *Registry) remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.workers, id)
}

// list returns workers sorted by name for stable UI ordering.
func (r *Registry) list() []*Worker {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Worker, 0, len(r.workers))
	for _, w := range r.workers {
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// reconcile adopts every managed worker container docker knows about, refreshing
// live ports/state and dropping registry entries whose container is gone.
func (r *Registry) reconcile() {
	out, err := dockerOut("ps", "-a",
		"--filter", "label=slopbox.role=worker",
		"--format", "{{.Label \"slopbox.id\"}}")
	live := map[string]bool{}
	if err == nil {
		for _, id := range strings.Fields(out) {
			live[id] = true
			r.adopt(id)
		}
	}
	for _, w := range r.list() {
		if !live[w.ID] {
			r.remove(w.ID)
		}
	}
}

// adopt (re)reads a single container's facts into the registry.
func (r *Registry) adopt(id string) {
	c := containerName(id)
	project, _ := dockerOut("inspect", "-f", "{{index .Config.Labels \"slopbox.project\"}}", c)
	w := &Worker{
		ID:        id,
		Project:   project,
		Name:      baseName(project),
		Container: c,
		Network:   networkName(id),
		Volume:    volumeName(id),
		Token:     containerEnv(c, "SLOPBOX_WORKER_TOKEN"),
		State:     containerState(c),
	}
	if w.State == "running" {
		w.APIPort, _ = publishedPort(c, "7000")
		w.SSHPort, _ = publishedPort(c, "22")
	}
	// Preserve a richer display name if we already had one.
	if existing, ok := r.get(id); ok && existing.Name != "" {
		w.Name = existing.Name
	}
	r.put(w)
}

func baseName(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 && i+1 < len(path) {
		return path[i+1:]
	}
	if path == "" {
		return "project"
	}
	return path
}
