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

// idleGrace is the wall-clock idle window before a worker is PAUSED (default 30m;
// SBOX_IDLE_GRACE overrides, in seconds; 0 disables reaping).
func idleGrace() time.Duration {
	if v := os.Getenv("SBOX_IDLE_GRACE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return time.Duration(n) * time.Second
		}
	}
	return 30 * time.Minute
}

// idleDeepGrace is the longer idle window after which a PAUSED worker is fully
// stopped to reclaim its memory (default 8h; SBOX_IDLE_STOP overrides, seconds;
// 0 disables deep-stop so idle workers stay paused/resumable indefinitely).
func idleDeepGrace() time.Duration {
	if v := os.Getenv("SBOX_IDLE_STOP"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return time.Duration(n) * time.Second
		}
	}
	return 8 * time.Hour
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

// reapIdle is the two-tier density reaper. An idle worker is first PAUSED (its
// processes — including a half-finished agent run holding conversation context —
// freeze in memory and thaw perfectly on resume, so nothing is forgotten). Only
// after a much longer deep-idle window is it fully STOPPED to reclaim memory
// (its work survives on the worktree + journal, but in-memory agent state is
// lost). Never touches a busy worker (running session) or one used within grace.
func (c *Coordinator) reapIdle() {
	grace := idleGrace()
	if grace <= 0 {
		return
	}
	deep := idleDeepGrace()
	for _, w := range c.reg.list() {
		idle := time.Since(c.act.last(w.ID))
		switch w.State {
		case "running":
			if idle < grace {
				continue
			}
			if workerBusy(w) {
				c.act.touch(w.ID) // reset the clock while work is in flight
				continue
			}
			if _, err := dockerRun("pause", w.Container); err == nil {
				ui.Info("sr", "idle-paused %s after %s (resumes instantly, agent state kept)", w.ID, grace)
				c.pm.CloseWorker(w.ID) // tunnels are dead while frozen; re-armed on resume
				c.reg.reconcileNow()
			}
		case "paused":
			if deep > 0 && idle >= deep {
				if _, err := dockerRun("stop", w.Container); err == nil {
					ui.Info("sr", "deep-stopped %s after %s idle (memory reclaimed)", w.ID, deep)
					c.reg.reconcileNow()
				}
			}
		}
	}
}

// resume wakes a reaped worker for the next request: unpause a frozen one (its
// agent picks up exactly where it left off), or start a deep-stopped one.
func (c *Coordinator) resume(w *Worker) bool {
	var err error
	if w.State == "paused" {
		_, err = dockerRun("unpause", w.Container)
	} else {
		_, err = dockerRun("start", w.Container)
	}
	if err != nil {
		return false
	}
	c.reg.reconcileNow()
	if nw, ok := c.reg.get(w.ID); ok {
		waitReady(nw)
		if nw.State == "running" && nw.APIPort != "" {
			// The pre-reap ssh client + agent relay died with the freeze/stop; drop
			// them and re-forward ports/agent against the live container.
			c.pm.CloseWorker(nw.ID)
			c.onWorkerUp(nw)
			return true
		}
	}
	return false
}
