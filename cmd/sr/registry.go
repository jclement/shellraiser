package main

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// Registry holds the live set of workers. Docker is the source of truth: the
// coordinator reconciles this map from container labels on boot and on a timer,
// so a crash or `brew upgrade` re-adopts running workers with zero data loss.
type Registry struct {
	mu      sync.RWMutex
	workers map[string]*Worker // keyed by id

	throttle sync.Mutex
	lastSync time.Time
}

// reconcileMinInterval coalesces the per-request reconciles many browser tabs
// trigger into at most one docker call per window.
const reconcileMinInterval = 1500 * time.Millisecond

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

// reconcile refreshes the registry from docker, throttled so the per-request
// calls from many browser tabs coalesce. Use reconcileNow for boot/mutations that
// must reflect immediately.
func (r *Registry) reconcile() {
	r.throttle.Lock()
	if time.Since(r.lastSync) < reconcileMinInterval {
		r.throttle.Unlock()
		return
	}
	r.lastSync = time.Now()
	r.throttle.Unlock()
	r.reconcileNow()
}

// fieldSep is an ASCII unit separator — safe inside a docker --format line since
// labels/ports never contain it.
const fieldSep = "\x1f"

// reconcileNow adopts every managed worker container in ONE `docker ps` call —
// id, project, state, and published ports come from the format string, so there
// are no per-worker `docker inspect` round-trips. The worker token (which never
// changes) is fetched once on first adoption and cached thereafter.
func (r *Registry) reconcileNow() {
	r.throttle.Lock()
	r.lastSync = time.Now()
	r.throttle.Unlock()

	out, err := dockerOut("ps", "-a",
		"--filter", "label=shellraiser.role=worker",
		"--format", `{{.Label "shellraiser.id"}}`+fieldSep+`{{.Label "shellraiser.project"}}`+fieldSep+`{{.State}}`+fieldSep+`{{.Ports}}`)
	if err != nil {
		return // docker down — keep the last-known registry rather than wiping it
	}
	live := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, fieldSep)
		if len(f) < 4 || f[0] == "" {
			continue
		}
		live[f[0]] = true
		r.adopt(f[0], f[1], f[2], f[3])
	}
	for _, w := range r.list() {
		if !live[w.ID] {
			r.remove(w.ID)
		}
	}
}

// adopt (re)reads one container's facts from the `docker ps` row. The token is
// the only field that needs an inspect; it's immutable, so we reuse a cached one.
func (r *Registry) adopt(id, project, state, ports string) {
	c := containerName(id)
	w := &Worker{
		ID:        id,
		Project:   project,
		Name:      baseName(project),
		Container: c,
		Network:   networkName(id),
		Volume:    volumeName(id),
		State:     state,
	}
	if existing, ok := r.get(id); ok {
		w.Token = existing.Token
		if existing.Name != "" {
			w.Name = existing.Name // preserve a richer display name
		}
	}
	if w.Token == "" {
		w.Token = containerEnv(c, "SHELLRAISER_WORKER_TOKEN") // one inspect, first adoption only
	}
	if state == "running" {
		w.APIPort = parsePublished(ports, "7000")
		w.SSHPort = parsePublished(ports, "22")
	}
	r.put(w)
}

// parsePublished pulls the host port for a container port out of docker ps's
// Ports column, e.g. "127.0.0.1:32835->7000/tcp, 127.0.0.1:32931->22/tcp".
func parsePublished(ports, containerPort string) string {
	for _, seg := range strings.Split(ports, ",") {
		seg = strings.TrimSpace(seg)
		arrow := strings.Index(seg, "->")
		if arrow < 0 || !strings.HasPrefix(seg[arrow+2:], containerPort+"/") {
			continue
		}
		host := seg[:arrow] // "127.0.0.1:32835" or "[::]:32835"
		if i := strings.LastIndexByte(host, ':'); i >= 0 {
			return host[i+1:]
		}
	}
	return ""
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
