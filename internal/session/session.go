// Package session runs and tracks PTY-backed processes (shells, editors, and
// coding agents) and detects when an agent is actively working vs. idle.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"
)

// Kind classifies a session so the UI can label it and so activity detection
// knows which sessions are agents worth "dinging" when they finish.
type Kind string

const (
	KindShell   Kind = "shell"
	KindClaude  Kind = "claude"
	KindCodex   Kind = "codex"
	KindEditor  Kind = "editor"
	KindCommand Kind = "command"
)

func (k Kind) isAgent() bool { return k == KindClaude || k == KindCodex }

// State is the coarse lifecycle/activity state surfaced to the UI.
type State string

const (
	StateIdle    State = "idle"    // alive, no recent output
	StateRunning State = "running" // producing output right now
	StateExited  State = "exited"  // process gone
)

const (
	activeWindow  = 1 * time.Second // output newer than this ⇒ "running"
	minRunForDing = 2 * time.Second // ignore brief blips when deciding to ding
	ringBytes     = 256 * 1024      // scrollback replayed on (re)attach
)

// Event is emitted on every state transition and broadcast to UI listeners.
type Event struct {
	ID       string `json:"id"`
	Kind     Kind   `json:"kind"`
	Title    string `json:"title"`
	Cwd      string `json:"cwd"`
	State    State  `json:"state"`
	Ding     bool   `json:"ding"`     // agent finished a unit of work
	ExitCode int    `json:"exitCode"` // valid when State == exited
}

// Info is the JSON-serializable snapshot of a session.
type Info struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Kind     Kind      `json:"kind"`
	Cwd      string    `json:"cwd"`
	State    State     `json:"state"`
	ExitCode int       `json:"exitCode"`
	PID      int       `json:"pid"`
	Created  time.Time `json:"created"`
}

type subscriber struct {
	ch   chan []byte
	once sync.Once
}

func (s *subscriber) close() { s.once.Do(func() { close(s.ch) }) }

// Session is one running process attached to a PTY.
type Session struct {
	ID      string
	Title   string
	Kind    Kind
	Cwd     string
	Created time.Time

	mgr  *Manager
	cmd  *exec.Cmd
	ptmx *os.File
	ring *ringBuffer

	mu           sync.Mutex
	subs         map[*subscriber]struct{}
	lastOutput   time.Time
	runningSince time.Time
	state        State
	exitCode     int
}

func (s *Session) Info() Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	pid := 0
	if s.cmd != nil && s.cmd.Process != nil {
		pid = s.cmd.Process.Pid
	}
	return Info{
		ID: s.ID, Title: s.Title, Kind: s.Kind, Cwd: s.Cwd,
		State: s.state, ExitCode: s.exitCode, PID: pid, Created: s.Created,
	}
}

// Snapshot returns the current scrollback for replay on attach.
func (s *Session) Snapshot() []byte { return s.ring.snapshot() }

// Subscribe returns a channel of live output and a cancel func to detach.
func (s *Session) Subscribe() (<-chan []byte, func()) {
	sub := &subscriber{ch: make(chan []byte, 512)}
	s.mu.Lock()
	if s.state == StateExited {
		s.mu.Unlock()
		sub.close()
		return sub.ch, func() {}
	}
	s.subs[sub] = struct{}{}
	s.mu.Unlock()
	return sub.ch, func() {
		s.mu.Lock()
		if _, ok := s.subs[sub]; ok {
			delete(s.subs, sub)
			sub.close()
		}
		s.mu.Unlock()
	}
}

// Write sends input bytes to the process.
func (s *Session) Write(p []byte) {
	s.mu.Lock()
	ptmx := s.ptmx
	exited := s.state == StateExited
	s.mu.Unlock()
	if ptmx != nil && !exited {
		_, _ = ptmx.Write(p)
	}
}

// Resize changes the PTY window size.
func (s *Session) Resize(cols, rows uint16) {
	s.mu.Lock()
	ptmx := s.ptmx
	s.mu.Unlock()
	if ptmx != nil {
		_ = pty.Setsize(ptmx, &pty.Winsize{Cols: cols, Rows: rows})
	}
}

func (s *Session) readLoop() {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			s.ring.write(chunk)
			s.mu.Lock()
			s.lastOutput = time.Now()
			for sub := range s.subs {
				select {
				case sub.ch <- chunk:
				default: // slow consumer; drop rather than stall the process
				}
			}
			s.mu.Unlock()
		}
		if err != nil {
			break
		}
	}
	s.handleExit()
}

// monitor drives the running/idle state machine and emits ding events.
func (s *Session) monitor() {
	t := time.NewTicker(300 * time.Millisecond)
	defer t.Stop()
	for range t.C {
		s.mu.Lock()
		if s.state == StateExited {
			s.mu.Unlock()
			return
		}
		active := time.Since(s.lastOutput) < activeWindow
		var ev *Event
		switch {
		case active && s.state != StateRunning:
			s.state = StateRunning
			s.runningSince = time.Now()
			ev = s.eventLocked(false)
		case !active && s.state == StateRunning:
			dur := time.Since(s.runningSince)
			s.state = StateIdle
			ev = s.eventLocked(s.Kind.isAgent() && dur >= minRunForDing)
		}
		s.mu.Unlock()
		if ev != nil {
			s.mgr.emit(*ev)
		}
	}
}

func (s *Session) eventLocked(ding bool) *Event {
	return &Event{ID: s.ID, Kind: s.Kind, Title: s.Title, Cwd: s.Cwd, State: s.state, Ding: ding, ExitCode: s.exitCode}
}

func (s *Session) handleExit() {
	err := s.cmd.Wait()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	}
	s.mu.Lock()
	s.state = StateExited
	s.exitCode = code
	for sub := range s.subs {
		sub.close()
		delete(s.subs, sub)
	}
	ev := s.eventLocked(false)
	s.mu.Unlock()
	_ = s.ptmx.Close()
	s.mgr.emit(*ev)
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
