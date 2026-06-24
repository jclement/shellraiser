package main

import (
	"encoding/json"
	"testing"
)

func drain(ch chan []byte) []coordEvent {
	var out []coordEvent
	for {
		select {
		case b := <-ch:
			var ce coordEvent
			if json.Unmarshal(b, &ce) == nil {
				out = append(out, ce)
			}
		default:
			return out
		}
	}
}

// TestCoordEventHub covers the cross-project state tracking: live broadcast,
// snapshot replay to a late subscriber, exited/gone clearing.
func TestCoordEventHub(t *testing.T) {
	h := newCoordEventHub(nil)

	// A live subscriber sees broadcasts.
	live, cancelLive := h.sub()
	defer cancelLive()

	h.record("projA", sessEvent{ID: "s1", Kind: "claude", State: "idle", NeedsInput: true})
	h.record("projB", sessEvent{ID: "s2", Kind: "shell", State: "running"})

	got := drain(live)
	if len(got) != 2 {
		t.Fatalf("live subscriber saw %d events, want 2", len(got))
	}

	// A late subscriber gets a snapshot of current (non-exited) sessions.
	late, cancelLate := h.sub()
	defer cancelLate()
	snap := drain(late)
	byWorker := map[string]coordEvent{}
	for _, ce := range snap {
		byWorker[ce.Worker] = ce
	}
	if len(snap) != 2 || !byWorker["projA"].NeedsInput || byWorker["projA"].ID != "s1" {
		t.Fatalf("snapshot wrong: %+v", snap)
	}

	// Exit clears the session from the snapshot.
	h.record("projA", sessEvent{ID: "s1", Kind: "claude", State: "exited"})
	_ = drain(live)
	late2, cancel2 := h.sub()
	defer cancel2()
	if s := drain(late2); len(s) != 1 || s[0].Worker != "projB" {
		t.Fatalf("after exit, snapshot should be just projB, got %+v", s)
	}

	// Worker gone clears all its sessions.
	h.mu.Lock()
	h.broadcastLocked(coordEvent{Worker: "projB", Gone: true})
	delete(h.last, "projB")
	h.mu.Unlock()
	late3, cancel3 := h.sub()
	defer cancel3()
	if s := drain(late3); len(s) != 0 {
		t.Fatalf("after gone, snapshot should be empty, got %+v", s)
	}
}
