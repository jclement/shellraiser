package session

import "sync"

// ringBuffer keeps the most recent N bytes of output for replay on attach.
type ringBuffer struct {
	mu   sync.Mutex
	data []byte
	max  int
}

func newRing(max int) *ringBuffer { return &ringBuffer{max: max} }

func (r *ringBuffer) write(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data = append(r.data, p...)
	if len(r.data) > r.max {
		trimmed := make([]byte, r.max)
		copy(trimmed, r.data[len(r.data)-r.max:])
		r.data = trimmed
	}
}

func (r *ringBuffer) snapshot() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]byte, len(r.data))
	copy(out, r.data)
	return out
}
