package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// coordEventHub multiplexes every running worker's /api/events SSE into one
// coordinator stream, tagging each event with its worker id — the cross-project
// "fan-in" the per-worker EventSource lacks. It also remembers the latest event
// per session so a freshly-connected browser gets an immediate snapshot (so an
// agent already sitting idle-awaiting-input in another project shows up at once,
// not only on its next transition).
type coordEventHub struct {
	co      *Coordinator
	mu      sync.Mutex
	subs    map[chan []byte]struct{}
	readers map[string]context.CancelFunc    // workerID → cancel its upstream reader
	last    map[string]map[string]coordEvent // workerID → sessionID → latest event
}

// coordEvent is a worker session event tagged with its project. The session
// fields are flattened in (embedded), so the wire shape is
// {worker, id, cwd, state, needsInput, ding, exitCode, title, kind}.
type coordEvent struct {
	Worker string `json:"worker"`
	Gone   bool   `json:"gone,omitempty"` // the worker stopped — clear its state
	sessEvent
}

type sessEvent struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Kind       string `json:"kind"`
	Cwd        string `json:"cwd"`
	State      string `json:"state"`
	Ding       bool   `json:"ding"`
	NeedsInput bool   `json:"needsInput"`
	ExitCode   int    `json:"exitCode"`
}

func newCoordEventHub(co *Coordinator) *coordEventHub {
	return &coordEventHub{
		co:      co,
		subs:    map[chan []byte]struct{}{},
		readers: map[string]context.CancelFunc{},
		last:    map[string]map[string]coordEvent{},
	}
}

// run keeps an upstream reader alive for every running worker.
func (h *coordEventHub) run() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		h.syncReaders()
		<-t.C
	}
}

func (h *coordEventHub) syncReaders() {
	running := map[string]*Worker{}
	for _, w := range h.co.reg.list() {
		if w.State == "running" && w.APIPort != "" {
			running[w.ID] = w
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, w := range running { // start readers for newly-running workers
		if _, ok := h.readers[id]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		h.readers[id] = cancel
		go h.readWorker(ctx, w)
	}
	for id, cancel := range h.readers { // stop readers for gone workers
		if _, ok := running[id]; !ok {
			cancel()
			delete(h.readers, id)
			delete(h.last, id)
			h.broadcastLocked(coordEvent{Worker: id, Gone: true})
		}
	}
}

func (h *coordEventHub) readWorker(ctx context.Context, w *Worker) {
	id, port, token := w.ID, w.APIPort, w.Token
	// Seed from a one-shot session snapshot so current state (incl. an agent
	// already awaiting input) is reflected immediately.
	if snap := h.fetchSessions(ctx, port, token); snap != nil {
		for _, e := range snap {
			h.record(id, e)
		}
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:"+port+"/api/events", nil)
	if token != "" {
		req.Header.Set("X-Shellraiser-Worker", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return // syncReaders will retry next tick if still running
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var e sessEvent
		if json.Unmarshal([]byte(payload), &e) != nil || e.ID == "" {
			continue // bridge events / pings have no session id — skip
		}
		h.record(id, e)
	}
}

// record updates the per-session snapshot and broadcasts the tagged event.
func (h *coordEventHub) record(workerID string, e sessEvent) {
	ce := coordEvent{Worker: workerID, sessEvent: e}
	h.mu.Lock()
	if e.State == "exited" {
		if h.last[workerID] != nil {
			delete(h.last[workerID], e.ID)
		}
	} else {
		if h.last[workerID] == nil {
			h.last[workerID] = map[string]coordEvent{}
		}
		h.last[workerID][e.ID] = ce
	}
	h.broadcastLocked(ce)
	h.mu.Unlock()
}

func (h *coordEventHub) fetchSessions(ctx context.Context, port, token string) []sessEvent {
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:"+port+"/api/sessions", nil)
	if token != "" {
		req.Header.Set("X-Shellraiser-Worker", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var out []sessEvent
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out
}

func (h *coordEventHub) broadcastLocked(ce coordEvent) {
	b, _ := json.Marshal(ce)
	for ch := range h.subs {
		select {
		case ch <- b:
		default: // slow consumer; drop
		}
	}
}

// sub registers a browser client and replays the current snapshot to it.
func (h *coordEventHub) sub() (chan []byte, func()) {
	ch := make(chan []byte, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	for _, sessions := range h.last { // snapshot of every project's live sessions
		for _, ce := range sessions {
			if b, err := json.Marshal(ce); err == nil {
				select {
				case ch <- b:
				default:
				}
			}
		}
	}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
		h.mu.Unlock()
	}
}

// handleEvents streams the aggregated cross-project event feed to the browser.
func (c *Coordinator) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	ch, cancel := c.events.sub()
	defer cancel()
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case b := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}
