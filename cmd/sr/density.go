package main

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/jclement/shellraiser/internal/ui"
)

// activity tracks the last time each worker was touched (a proxied request or a
// running session), so the idle reaper can stop genuinely-idle workers without
// killing one that's mid-task. An idle worker drops to ~nothing; a stopped one is
// transparently resumed on the next request (lazy-resume).
type activity struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func newActivity() *activity { return &activity{seen: map[string]time.Time{}} }

func (a *activity) touch(id string) {
	a.mu.Lock()
	a.seen[id] = time.Now()
	a.mu.Unlock()
}

func (a *activity) last(id string) time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.seen[id]
}

// idleGrace is the wall-clock idle window before a worker is stopped (default
// 30m; SBOX_IDLE_GRACE overrides, in seconds; 0 disables).
func idleGrace() time.Duration {
	if v := os.Getenv("SBOX_IDLE_GRACE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return time.Duration(n) * time.Second
		}
	}
	return 30 * time.Minute
}

// workerBusy reports whether the worker has any running (agent/shell) session —
// the authoritative "don't reap me" signal, computed by the worker itself.
func workerBusy(w *Worker) bool {
	if w.APIPort == "" {
		return false
	}
	req, _ := http.NewRequest("GET", "http://127.0.0.1:"+w.APIPort+"/api/sessions", nil)
	if w.Token != "" {
		req.Header.Set("X-Shellraiser-Worker", w.Token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return true // can't tell → assume busy (never reap on uncertainty)
	}
	defer resp.Body.Close()
	var sessions []struct {
		State string `json:"state"`
	}
	if json.NewDecoder(resp.Body).Decode(&sessions) != nil {
		return true
	}
	for _, s := range sessions {
		if s.State == "running" {
			return true
		}
	}
	return false
}

// reapIdle stops workers that have been idle past the grace window. Never stops a
// busy worker (running session) or one touched within the window.
func (c *Coordinator) reapIdle() {
	grace := idleGrace()
	if grace <= 0 {
		return
	}
	for _, w := range c.reg.list() {
		if w.State != "running" {
			continue
		}
		if time.Since(c.act.last(w.ID)) < grace {
			continue
		}
		if workerBusy(w) {
			c.act.touch(w.ID) // reset the clock while work is in flight
			continue
		}
		if _, err := dockerRun("stop", w.Container); err == nil {
			ui.Info("sr", "idle-stopped %s after %s", w.ID, grace)
			c.reg.adopt(w.ID)
		}
	}
}

// resume starts a stopped worker and waits for it to serve, so a request to a
// reaped project transparently wakes it.
func (c *Coordinator) resume(w *Worker) bool {
	if _, err := dockerRun("start", w.Container); err != nil {
		return false
	}
	c.reg.adopt(w.ID)
	if nw, ok := c.reg.get(w.ID); ok {
		waitReady(nw)
		return nw.State == "running" && nw.APIPort != ""
	}
	return false
}
