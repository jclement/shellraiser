package main

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/jclement/shellraiser/internal/ui"
	"golang.org/x/crypto/ssh"
)

// reservedPorts are worker-internal services that must never be exposed via the
// port-mapper or /p/ — postgres, pgweb, code-server, the worker API, and sshd.
var reservedPorts = map[int]bool{5432: true, 8081: true, 8082: true, 7000: true, 22: true}

// PortMapper turns a worker's internal TCP port into a host-loopback port via an
// SSH -L tunnel over the worker's published sshd. One ssh.Client per worker; one
// net.Listener (bound to 127.0.0.1) per mapping. This is the only routing that
// reaches arbitrary container TCP identically on macOS, Linux, and WSL2.
type PortMapper struct {
	signer  ssh.Signer
	dev     Device          // where host listeners bind + the SSH agent lives
	ts      tailnetListener // non-nil when --tailnet is on; also binds the tailnet IP
	mu      sync.Mutex
	clients map[string]*ssh.Client      // workerID → ssh client
	fwds    map[string]map[int]*forward // workerID → containerPort → forward
	agents  map[string]net.Listener     // workerID → in-worker agent relay listener
	cmds    map[string]net.Listener     // workerID → in-worker command relay listener
}

// tailnetListener is the slice of tsnet.Server we use: Listen on the tailnet IP.
type tailnetListener interface {
	Listen(network, addr string) (net.Listener, error)
}

type forward struct {
	closers  []func() // device listener (+ tailnet listener, when enabled)
	hostPort int
}

// serveListener accepts on ln and bridges every connection to a fresh dial().
// The listener may be loopback (local device), the tailnet IP (backend), or
// remote (a device opening channels back); dial always reaches the worker from
// the coordinator.
func serveListener(ln net.Listener, dial DialFunc) {
	for {
		local, err := ln.Accept()
		if err != nil {
			return // listener closed (unmapped)
		}
		go func() {
			remote, err := dial()
			if err != nil {
				_ = local.Close()
				return
			}
			bridgeConn(local, remote)
		}()
	}
}

// bridgeConn pipes two connections together until either side closes.
func bridgeConn(a, b net.Conn) {
	defer a.Close()
	defer b.Close()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(a, b); done <- struct{}{} }()
	go func() { _, _ = io.Copy(b, a); done <- struct{}{} }()
	<-done
}

func newPortMapper(signer ssh.Signer, dev Device, ts tailnetListener) *PortMapper {
	pm := &PortMapper{signer: signer, dev: dev, clients: map[string]*ssh.Client{}, fwds: map[string]map[int]*forward{}, agents: map[string]net.Listener{}, cmds: map[string]net.Listener{}}
	if ts != nil {
		pm.ts = ts
	}
	return pm
}

// forget removes a relay listener from its map once its accept loop exits (only
// if it's still the current one), so a dead relay is rebuilt on the next heal.
func (m *PortMapper) forget(set map[string]net.Listener, workerID string, ln net.Listener) {
	m.mu.Lock()
	if set[workerID] == ln {
		delete(set, workerID)
	}
	m.mu.Unlock()
}

// hasAgent / hasCmd report whether a worker's relay is currently established.
func (m *PortMapper) hasAgent(workerID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.agents[workerID]
	return ok
}

func (m *PortMapper) hasCmd(workerID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.cmds[workerID]
	return ok
}

// clientLive reports whether the cached SSH client to a worker is still
// answering — false if there's none or it has dropped (e.g. the worker was
// idle-paused and its sshd froze, timing the client out). Used to detect relays
// that need rebuilding.
func (m *PortMapper) clientLive(workerID string) bool {
	m.mu.Lock()
	c := m.clients[workerID]
	m.mu.Unlock()
	if c == nil {
		return false
	}
	_, _, err := c.SendRequest("keepalive@openssh.com", true, nil)
	return err == nil
}

// setDevice swaps the active host-presence device (local ⇄ a connected remote).
// New forwards bind on the new device; existing forwards are left on whatever
// device bound them (per-device migration is slice 6).
func (m *PortMapper) setDevice(d Device) {
	m.mu.Lock()
	m.dev = d
	m.mu.Unlock()
}

func (m *PortMapper) client(w *Worker) (*ssh.Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.clients[w.ID]; ok {
		return c, nil
	}
	if w.SSHPort == "" {
		return nil, fmt.Errorf("worker %s has no sshd port", w.ID)
	}
	cfg := &ssh.ClientConfig{
		User:            "ubuntu",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(m.signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // loopback to our own container
		Timeout:         8 * time.Second,
	}
	c, err := ssh.Dial("tcp", "127.0.0.1:"+w.SSHPort, cfg)
	if err != nil {
		return nil, fmt.Errorf("ssh dial worker %s: %w", w.ID, err)
	}
	m.clients[w.ID] = c
	return c, nil
}

// Map forwards 127.0.0.1:<hostPort> on the host to 127.0.0.1:<containerPort>
// inside the worker, preferring the same port number when free. Returns the
// chosen host port.
// localPort is the desired host port: 0 ⇒ prefer the same number as the
// container port, falling back to an OS-assigned one.
func (m *PortMapper) Map(w *Worker, containerPort, localPort int) (int, error) {
	if reservedPorts[containerPort] {
		return 0, fmt.Errorf("port %d is reserved (internal service)", containerPort)
	}
	if containerPort < 1 || containerPort > 65535 {
		return 0, fmt.Errorf("invalid port %d", containerPort)
	}
	// Already mapped?
	m.mu.Lock()
	if f, ok := m.fwds[w.ID][containerPort]; ok {
		m.mu.Unlock()
		return f.hostPort, nil
	}
	m.mu.Unlock()

	// dial reaches the worker's container port from the coordinator; the device
	// supplies the listener that feeds it (loopback locally, or remote).
	dial := m.workerDialer(w, containerPort)
	want := localPort
	if want == 0 {
		want = containerPort
	}
	hostPort, closer, err := m.dev.Forward(want, localPort != 0, dial)
	if err != nil {
		return 0, err
	}

	m.mu.Lock()
	if m.fwds[w.ID] == nil {
		m.fwds[w.ID] = map[int]*forward{}
	}
	m.fwds[w.ID][containerPort] = &forward{closers: []func(){closer}, hostPort: hostPort}
	m.mu.Unlock()
	ui.Info("ports", "mapped %s :%d → %s:%d", w.ID, containerPort, m.dev.Name(), hostPort)

	// Also expose on the tailnet IP (same port number) when --tailnet is on — this
	// is the backend's own tailnet bind, additive to (and independent of) the
	// device bind. tsnet.Listen blocks until the node is authenticated, so do it
	// async — the device mapping is already live and never waits on the tailnet.
	if m.ts != nil {
		go m.bindTailnet(w.ID, containerPort, dial)
	}
	return hostPort, nil
}

// workerDialer returns a DialFunc that opens the worker's container port over the
// coordinator↔worker SSH tunnel.
func (m *PortMapper) workerDialer(w *Worker, containerPort int) DialFunc {
	return func() (net.Conn, error) {
		cl, err := m.client(w)
		if err != nil {
			return nil, err
		}
		return cl.Dial("tcp", "127.0.0.1:"+strconv.Itoa(containerPort))
	}
}

// bindTailnet attaches a tailnet listener for an already-mapped port once the
// tsnet node is up. If the mapping was removed meanwhile, the late listener is
// closed.
func (m *PortMapper) bindTailnet(workerID string, containerPort int, dial DialFunc) {
	tln, err := m.ts.Listen("tcp", ":"+strconv.Itoa(containerPort))
	if err != nil {
		ui.Warn("ports", "tailnet bind :%d failed: %v", containerPort, err)
		return
	}
	m.mu.Lock()
	f, ok := m.fwds[workerID][containerPort]
	if !ok {
		m.mu.Unlock()
		_ = tln.Close()
		return
	}
	f.closers = append(f.closers, func() { _ = tln.Close() })
	m.mu.Unlock()
	ui.Info("ports", "mapped %s :%d on the tailnet too", workerID, containerPort)
	serveListener(tln, dial)
}

// unmapPorts tears down all of a worker's port forwards (but not its ssh client,
// agent, or command relay), so they can be rebound onto a newly-active device.
// The agent/cmd relays need no rebind — their in-worker accept loops dial the
// current device fresh on each connection, so they follow the active device.
func (m *PortMapper) unmapPorts(workerID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, f := range m.fwds[workerID] {
		for _, c := range f.closers {
			c()
		}
	}
	delete(m.fwds, workerID)
}

// Unmap tears down a single forward.
func (m *PortMapper) Unmap(workerID string, containerPort int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if f, ok := m.fwds[workerID][containerPort]; ok {
		for _, c := range f.closers {
			c()
		}
		delete(m.fwds[workerID], containerPort)
	}
}

// CloseWorker tears down every forward + the ssh client for a worker (on
// stop/nuke).
func (m *PortMapper) CloseWorker(workerID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, f := range m.fwds[workerID] {
		for _, c := range f.closers {
			c()
		}
	}
	delete(m.fwds, workerID)
	if ln, ok := m.agents[workerID]; ok {
		_ = ln.Close()
		delete(m.agents, workerID)
	}
	if ln, ok := m.cmds[workerID]; ok {
		_ = ln.Close()
		delete(m.cmds, workerID)
	}
	if c, ok := m.clients[workerID]; ok {
		_ = c.Close()
		delete(m.clients, workerID)
	}
}

// ForwardAgent relays the host SSH agent (e.g. gpg-agent/YubiKey, 1Password) into
// the worker over the SSH tunnel: it opens a unix listener INSIDE the worker at
// agentRelaySock and pipes each connection to the host agent socket. Unlike a
// bind-mounted socket this crosses the docker VM boundary, so it works on Colima,
// Docker Desktop, OrbStack, and native Linux alike. Idempotent per worker.
func (m *PortMapper) ForwardAgent(w *Worker) error {
	if !m.dev.AgentAvailable() {
		return nil
	}
	m.mu.Lock()
	if _, ok := m.agents[w.ID]; ok {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	cl, err := m.client(w)
	if err != nil {
		return err
	}
	ln, err := cl.ListenUnix(agentRelaySock)
	if err != nil {
		return fmt.Errorf("agent relay listen in %s: %w", w.ID, err)
	}
	m.mu.Lock()
	if _, ok := m.agents[w.ID]; ok { // lost a race — keep the first
		m.mu.Unlock()
		_ = ln.Close()
		return nil
	}
	m.agents[w.ID] = ln
	m.mu.Unlock()

	go func() {
		defer m.forget(m.agents, w.ID, ln) // relay died → drop it so healWiring rebuilds
		for {
			worker, err := ln.Accept()
			if err != nil {
				return // forward closed (client dropped, worker gone, etc.)
			}
			go func() {
				defer worker.Close()
				host, err := m.dev.DialAgent()
				if err != nil {
					ui.Info("ports", "agent relay dial host: %v", err)
					return
				}
				defer host.Close()
				done := make(chan struct{}, 2)
				go func() { _, _ = io.Copy(host, worker); done <- struct{}{} }()
				go func() { _, _ = io.Copy(worker, host); done <- struct{}{} }()
				<-done
			}()
		}
	}()
	ui.Info("ports", "ssh agent relayed into %s", w.ID)
	return nil
}

// ForwardCmd exposes a unix socket inside the worker (cmdRelaySock) that the
// container's CLI shims connect to; each connection is bridged to the active
// device, which runs the requested (exposed) command. Mirrors ForwardAgent. The
// command name + argv ride the cmdrelay header, so this stays a dumb pipe and the
// device authorizes; it works for whichever device is active at accept time.
// Idempotent per worker.
func (m *PortMapper) ForwardCmd(w *Worker) error {
	m.mu.Lock()
	if _, ok := m.cmds[w.ID]; ok {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()
	cl, err := m.client(w)
	if err != nil {
		return err
	}
	ln, err := cl.ListenUnix(cmdRelaySock)
	if err != nil {
		return fmt.Errorf("cmd relay listen in %s: %w", w.ID, err)
	}
	m.mu.Lock()
	if _, ok := m.cmds[w.ID]; ok {
		m.mu.Unlock()
		_ = ln.Close()
		return nil
	}
	m.cmds[w.ID] = ln
	m.mu.Unlock()
	go func() {
		defer m.forget(m.cmds, w.ID, ln) // relay died → drop it so healWiring rebuilds
		for {
			worker, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer worker.Close()
				dev, err := m.dev.DialCmd()
				if err != nil {
					return
				}
				bridgeConn(worker, dev)
			}()
		}
	}()
	ui.Info("ports", "command relay ready in %s", w.ID)
	return nil
}

// List returns the worker's active mappings as containerPort → hostPort.
func (m *PortMapper) List(workerID string) map[int]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[int]int{}
	for cp, f := range m.fwds[workerID] {
		out[cp] = f.hostPort
	}
	return out
}
