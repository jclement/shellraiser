package main

import (
	"sync"
	"time"
)

// authLockout is a simple per-identity failure throttle for the device-link SSH
// server (decision #1: the listener may be reachable on any network, so auth
// brute-forcing must be rate-limited). After maxFails failures an identity is
// locked out for lockWindow; any success resets it.
type authLockout struct {
	mu    sync.Mutex
	fails map[string]*lockState
}

type lockState struct {
	n    int
	till time.Time
}

const (
	maxAuthFails = 5
	lockWindow   = 1 * time.Minute
)

func newAuthLockout() *authLockout { return &authLockout{fails: map[string]*lockState{}} }

// allow reports whether id may attempt auth right now.
func (l *authLockout) allow(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.fails[id]
	if st == nil {
		return true
	}
	if st.n >= maxAuthFails && time.Now().Before(st.till) {
		return false
	}
	return true
}

func (l *authLockout) fail(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.fails[id]
	if st == nil {
		st = &lockState{}
		l.fails[id] = st
	}
	st.n++
	st.till = time.Now().Add(lockWindow)
}

func (l *authLockout) reset(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, id)
}
