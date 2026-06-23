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
	ts      tailnetListener // non-nil when --tailnet is on; also binds the tailnet IP
	mu      sync.Mutex
	clients map[string]*ssh.Client      // workerID → ssh client
	fwds    map[string]map[int]*forward // workerID → containerPort → forward
}

// tailnetListener is the slice of tsnet.Server we use: Listen on the tailnet IP.
type tailnetListener interface {
	Listen(network, addr string) (net.Listener, error)
}

type forward struct {
	lns      []net.Listener // loopback (+ tailnet, when enabled)
	hostPort int
}

func newPortMapper(signer ssh.Signer, ts tailnetListener) *PortMapper {
	pm := &PortMapper{signer: signer, clients: map[string]*ssh.Client{}, fwds: map[string]map[int]*forward{}}
	if ts != nil {
		pm.ts = ts
	}
	return pm
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
func (m *PortMapper) Map(w *Worker, containerPort int) (int, error) {
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

	cl, err := m.client(w)
	if err != nil {
		return 0, err
	}
	// Prefer the same number on the host loopback; fall back to an OS-assigned one.
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(containerPort))
	if err != nil {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return 0, err
		}
	}
	hostPort := ln.Addr().(*net.TCPAddr).Port
	go m.serve(ln, cl, containerPort)

	m.mu.Lock()
	if m.fwds[w.ID] == nil {
		m.fwds[w.ID] = map[int]*forward{}
	}
	m.fwds[w.ID][containerPort] = &forward{lns: []net.Listener{ln}, hostPort: hostPort}
	m.mu.Unlock()
	ui.Info("ports", "mapped %s :%d → 127.0.0.1:%d", w.ID, containerPort, hostPort)

	// Also expose on the tailnet IP (same port number) when --tailnet is on.
	// tsnet.Listen blocks until the node is authenticated, so do it async — the
	// loopback mapping is already live and never waits on the tailnet.
	if m.ts != nil {
		go m.bindTailnet(w.ID, cl, containerPort)
	}
	return hostPort, nil
}

// bindTailnet attaches a tailnet listener for an already-mapped port once the
// tsnet node is up. If the mapping was removed meanwhile, the late listener is
// closed.
func (m *PortMapper) bindTailnet(workerID string, cl *ssh.Client, containerPort int) {
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
	f.lns = append(f.lns, tln)
	m.mu.Unlock()
	ui.Info("ports", "mapped %s :%d on the tailnet too", workerID, containerPort)
	m.serve(tln, cl, containerPort)
}

func (m *PortMapper) serve(ln net.Listener, cl *ssh.Client, containerPort int) {
	for {
		local, err := ln.Accept()
		if err != nil {
			return // listener closed (unmapped)
		}
		go func() {
			defer local.Close()
			remote, err := cl.Dial("tcp", "127.0.0.1:"+strconv.Itoa(containerPort))
			if err != nil {
				return
			}
			defer remote.Close()
			done := make(chan struct{}, 2)
			go func() { io.Copy(remote, local); done <- struct{}{} }()
			go func() { io.Copy(local, remote); done <- struct{}{} }()
			<-done
		}()
	}
}

// Unmap tears down a single forward.
func (m *PortMapper) Unmap(workerID string, containerPort int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if f, ok := m.fwds[workerID][containerPort]; ok {
		for _, ln := range f.lns {
			_ = ln.Close()
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
		for _, ln := range f.lns {
			_ = ln.Close()
		}
	}
	delete(m.fwds, workerID)
	if c, ok := m.clients[workerID]; ok {
		_ = c.Close()
		delete(m.clients, workerID)
	}
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
