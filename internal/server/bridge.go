package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
)

// bridgeHub fans out "bridge" messages (open-URL / copy-to-clipboard requested
// from inside the container) to the connected web UIs over the SSE stream.
type bridgeHub struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

func newBridgeHub() *bridgeHub { return &bridgeHub{subs: map[chan []byte]struct{}{}} }

func (h *bridgeHub) sub() (chan []byte, func()) {
	ch := make(chan []byte, 8)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
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

func (h *bridgeHub) emit(b []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- b:
		default:
		}
	}
}

// handleBridge receives an open/copy request from an in-container helper (`open`
// / `sr-copy`) and pushes it to the browser, which does the actual work — so it
// works over the tailnet too (the browser, not the container, opens/copies).
func (s *Server) handleBridge(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string `json:"action"`
		URL    string `json:"url"`
		Text   string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Action != "open" && req.Action != "copy" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("action must be open or copy"))
		return
	}
	b, _ := json.Marshal(map[string]string{"type": "bridge", "action": req.Action, "url": req.URL, "text": req.Text})
	s.bridge.emit(b)
	writeJSON(w, map[string]bool{"ok": true})
}
