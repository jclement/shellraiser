package session

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"
)

// Commands resolves the actual argv for each agent/editor/shell kind. Anything
// empty falls back to a sensible auto-detected default.
type Commands struct {
	Shell  []string // default login shell
	Editor []string // terminal editor
	Claude []string // claude in danger mode (container is the sandbox)
	Codex  []string // codex in danger mode
	Run    []string // the project's `run` command (header Run button)
}

// Manager owns the live sessions and the status event fan-out.
type Manager struct {
	cmds Commands
	env  []string

	mu       sync.Mutex
	sessions map[string]*Session
	order    []string
	subs     map[chan Event]struct{}
}

// CreateOpts describes a session to start.
type CreateOpts struct {
	Kind   Kind
	Cwd    string
	Title  string
	Args   []string // overrides the kind's default command when set
	Prompt string   // starting prompt for an agent (appended as its positional arg)
	Cols   uint16
	Rows   uint16
}

// NewManager builds a Manager, auto-detecting defaults for anything not set.
func NewManager(cmds Commands) *Manager {
	if len(cmds.Shell) == 0 {
		cmds.Shell = []string{defaultShell()}
	}
	if len(cmds.Editor) == 0 {
		cmds.Editor = []string{firstAvailable([]string{"hx", "fresh", "vim", "vi"}, "vi")}
	}
	if len(cmds.Claude) == 0 {
		cmds.Claude = []string{"claude", "--dangerously-skip-permissions"}
	}
	if len(cmds.Codex) == 0 {
		cmds.Codex = []string{"codex", "--dangerously-bypass-approvals-and-sandbox"}
	}
	return &Manager{
		cmds:     cmds,
		env:      os.Environ(),
		sessions: map[string]*Session{},
		subs:     map[chan Event]struct{}{},
	}
}

func (m *Manager) argvFor(o CreateOpts) []string {
	if len(o.Args) > 0 {
		return o.Args
	}
	var base []string
	switch o.Kind {
	case KindClaude:
		base = m.cmds.Claude
	case KindCodex:
		base = m.cmds.Codex
	case KindEditor:
		base = m.cmds.Editor
	case KindRun:
		base = m.cmds.Run
	default:
		base = m.cmds.Shell
	}
	// A starting prompt is passed as the agent's positional argument (claude/codex
	// both accept one), so a new session can kick off with work already queued.
	if o.Prompt != "" && o.Kind.isAgent() {
		return append(append([]string{}, base...), o.Prompt)
	}
	return base
}

// HasAgent reports whether the resolved launcher binary for an agent kind is on
// PATH — used to offer only the agents actually installed in this worker.
func (m *Manager) HasAgent(k Kind) bool {
	argv := m.argvFor(CreateOpts{Kind: k})
	if len(argv) == 0 {
		return false
	}
	_, err := exec.LookPath(argv[0])
	return err == nil
}

// Create starts a new session and begins streaming its output.
func (m *Manager) Create(o CreateOpts) (*Session, error) {
	argv := m.argvFor(o)
	if len(argv) == 0 {
		return nil, fmt.Errorf("no command for kind %q", o.Kind)
	}
	if o.Cols == 0 {
		o.Cols = 120
	}
	if o.Rows == 0 {
		o.Rows = 32
	}
	title := o.Title
	if title == "" {
		title = string(o.Kind)
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	// Only set the working dir if it actually exists. A stale/auto-discovered
	// worktree whose path isn't present here would otherwise make fork/exec fail
	// with a cryptic "no such file or directory" (Go reports the failed chdir as
	// the exec path). Falling back to the worker's cwd keeps the shell usable.
	if o.Cwd != "" {
		if fi, err := os.Stat(o.Cwd); err == nil && fi.IsDir() {
			cmd.Dir = o.Cwd
		}
	}
	cmd.Env = append(append([]string{}, m.env...), "TERM=xterm-256color", "SHELLRAISER=1")

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: o.Cols, Rows: o.Rows})
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", argv[0], err)
	}

	now := time.Now()
	s := &Session{
		ID: newID(), Title: title, Kind: o.Kind, Cwd: o.Cwd, Created: now,
		mgr: m, cmd: cmd, ptmx: ptmx, ring: newRing(ringBytes),
		subs: map[*subscriber]struct{}{}, lastOutput: now, state: StateIdle,
	}

	m.mu.Lock()
	m.sessions[s.ID] = s
	m.order = append(m.order, s.ID)
	m.mu.Unlock()

	go s.readLoop()
	go s.monitor()
	return s, nil
}

// Get returns a session by id.
func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	return s, ok
}

// List returns sessions in creation order.
func (m *Manager) List() []Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Info, 0, len(m.order))
	for _, id := range m.order {
		if s, ok := m.sessions[id]; ok {
			out = append(out, s.Info())
		}
	}
	return out
}

// Roots returns the root PID → Info for every live session, used to attribute
// listening ports (whose owning PID is a descendant) back to a worktree.
func (m *Manager) Roots() map[int]Info {
	m.mu.Lock()
	roots := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		if s.cmd.Process != nil {
			roots = append(roots, s)
		}
	}
	m.mu.Unlock()
	out := make(map[int]Info, len(roots))
	for _, s := range roots {
		out[s.cmd.Process.Pid] = s.Info()
	}
	return out
}

// Kill terminates and forgets a session.
// KillAll terminates every session (used when a bare-metal worker is removed).
func (m *Manager) KillAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		_ = m.Kill(id)
	}
}

func (m *Manager) Kill(id string) error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
		for i, oid := range m.order {
			if oid == id {
				m.order = append(m.order[:i], m.order[i+1:]...)
				break
			}
		}
	}
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("no session %q", id)
	}
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	return nil
}

// Events subscribes to status events; call cancel to stop.
func (m *Manager) Events() (<-chan Event, func()) {
	ch := make(chan Event, 64)
	m.mu.Lock()
	m.subs[ch] = struct{}{}
	m.mu.Unlock()
	return ch, func() {
		m.mu.Lock()
		if _, ok := m.subs[ch]; ok {
			delete(m.subs, ch)
			close(ch)
		}
		m.mu.Unlock()
	}
}

func (m *Manager) emit(ev Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for ch := range m.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func firstAvailable(candidates []string, fallback string) string {
	for _, c := range candidates {
		if _, err := exec.LookPath(c); err == nil {
			return c
		}
	}
	return fallback
}

// defaultShell resolves a login shell to an ABSOLUTE path that actually exists —
// preferring $SHELL — so exec never tries a phantom like /usr/bin/zsh that the
// PATH claims but isn't there.
func defaultShell() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		if fi, err := os.Stat(sh); err == nil && !fi.IsDir() {
			return sh
		}
	}
	for _, c := range []string{"zsh", "bash", "sh"} {
		if p, err := exec.LookPath(c); err == nil {
			if fi, e := os.Stat(p); e == nil && !fi.IsDir() {
				return p
			}
		}
	}
	return "/bin/sh"
}
