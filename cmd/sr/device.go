package main

import (
	"fmt"
	"net"
	"os/exec"
	"strconv"

	"github.com/jclement/shellraiser/internal/cmdrelay"
)

// DialFunc opens a fresh connection to a worker's container port. It always runs
// on the coordinator (it reaches the worker over the coordinator↔worker SSH
// tunnel); only the *listener* that feeds it may live on a remote device. So a
// DialFunc is never sent over the device link — a remote device refers to it by
// an opaque tag and the coordinator invokes it when a forwarded connection
// arrives. See docs/device-link.md ("device-initiated").
type DialFunc func() (net.Conn, error)

// Device is the host-presence layer — the machine where the human sits. The
// backend can only *request* these capabilities; a device honors them per its own
// config (see docs/device-link.md). The default `sr` runs an in-process
// localDevice; `sr connect` supplies a remoteDevice over the SSH link.
//
// The fixed vocabulary: bind a forwarded port (the listener lives on the device),
// relay the device's SSH agent, and open a URL.
type Device interface {
	// Name identifies the device in logs and the UI.
	Name() string
	// Grants reports whether the device permits a capability (cap* in devicecfg.go).
	// The local device grants everything; a remote device honors only its
	// device.toml. Enforcement is also re-checked device-side (it is authoritative).
	Grants(capability string) bool
	// Forward asks the device to bind a loopback listener and pipe every accepted
	// connection through dial (which reaches the worker from the coordinator).
	// want is the preferred host port (0 ⇒ OS-assigned); if strict, a busy want is
	// an error instead of falling back to an OS-assigned port. Returns the bound
	// port and a closer that tears the forward down.
	Forward(want int, strict bool, dial DialFunc) (port int, closer func(), err error)
	// AgentAvailable reports whether the device exposes an SSH agent to relay.
	AgentAvailable() bool
	// DialAgent opens one connection to the device's SSH agent (per relayed
	// request). Only called when AgentAvailable is true.
	DialAgent() (net.Conn, error)
	// CmdAvailable reports whether the device exposes any forwarded CLI commands.
	CmdAvailable() bool
	// DialCmd opens one command-relay connection to the device (per invocation):
	// the returned conn speaks the cmdrelay framing, and the command name rides in
	// its header (the device authorizes it). Only called when CmdAvailable.
	DialCmd() (net.Conn, error)
	// OpenURL opens a URL in the device's browser (best-effort).
	OpenURL(url string) error
}

// localDevice is the in-process device: the coordinator host itself. Its methods
// are exactly what the coordinator did inline before the Device seam existed, so
// the common `sr` (backend + local device on one machine) is unchanged.
type localDevice struct{}

func (localDevice) Name() string { return "local" }

// Grants: the local device is the trusted host itself, so it grants everything.
func (localDevice) Grants(string) bool { return true }

func (localDevice) Forward(want int, strict bool, dial DialFunc) (int, func(), error) {
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(want))
	if err != nil {
		if strict {
			return 0, nil, fmt.Errorf("local port %d is unavailable", want)
		}
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return 0, nil, err
		}
	}
	port := ln.Addr().(*net.TCPAddr).Port
	go serveListener(ln, dial)
	return port, func() { _ = ln.Close() }, nil
}

func (localDevice) AgentAvailable() bool { return hostAgentSocket() != "" }

func (localDevice) DialAgent() (net.Conn, error) {
	return net.Dial("unix", hostAgentSocket())
}

// CmdAvailable: the local host exposes whatever commands it has from the set it
// advertises (default "op" if installed).
func (localDevice) CmdAvailable() bool { return len(localExposedCommands()) > 0 }

// DialCmd serves the command relay in-process over a pipe — the local host IS the
// device, so it runs the tool itself (same path a remote device would, no SSH).
func (localDevice) DialCmd() (net.Conn, error) {
	a, b := net.Pipe()
	go cmdrelay.Serve(a, cmdrelay.Exposer(localExposedCommands()))
	return b, nil
}

// localExposedCommands is the set the local device forwards into workers: the
// configured list, else "op" when present (the common case for desktop 1Password).
func localExposedCommands() []string {
	if len(hostCfg.ExposeCommands) > 0 {
		return hostCfg.ExposeCommands
	}
	if _, err := exec.LookPath("op"); err == nil {
		return []string{"op"}
	}
	return nil
}

func (localDevice) OpenURL(url string) error {
	openBrowser(url)
	return nil
}
