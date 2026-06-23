// Package cmdrelay forwards a single CLI invocation from a container to the
// device that holds the real tool. A CLI app is just argv + env + stdin →
// stdout/stderr/exit; the container has no 1Password desktop integration / cloud
// creds / browser, the device does. The container ships the call to the device,
// which runs the named tool locally — if its config exposes that command — and
// streams the result back. `op` is the motivating case, but nothing here is
// op-specific: a device can expose `gh`, `aws`, `kubectl`, … the same way.
//
// The protocol is fully framed in both directions so it rides any duplex stream —
// an SSH channel (remote device), a unix socket (the in-worker relay), or an
// in-memory pipe (local device) — with no reliance on half-close.
//
//	shim → serve : [u32 hdrLen][hdr JSON]  then frames STDIN / STDIN_EOF
//	serve → shim : frames STDOUT / STDERR / EXIT
package cmdrelay

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// Header is the request preamble the shim sends first.
type Header struct {
	Name string            `json:"name"` // which exposed command (e.g. "op")
	Argv []string          `json:"argv"`
	Env  map[string]string `json:"env"`
}

const (
	fStdin    byte = 0
	fStdout   byte = 1
	fStderr   byte = 2
	fExit     byte = 3
	fStdinEOF byte = 9

	maxFrame = 1 << 20 // 1 MiB per frame
)

// Resolver decides whether a command name is exposed by the device and returns
// the binary to run plus an optional per-command argv policy. ok=false ⇒ the
// device does not expose that command (the device is authoritative).
type Resolver func(name string) (binary string, policy func(argv []string) error, ok bool)

// Exposer builds a Resolver from a list of exposed command names, attaching the
// strict op policy to "op" and a permissive default to the rest. The exposure
// itself is the trust gate; per-command policy is defense in depth.
func Exposer(names []string) Resolver {
	set := map[string]bool{}
	for _, n := range names {
		set[strings.TrimSpace(n)] = true
	}
	return func(name string) (string, func([]string) error, bool) {
		if !set[name] {
			return "", nil, false
		}
		if name == "op" {
			return "op", OpPolicy, true
		}
		return name, nil, true
	}
}

// EnvAllow is the whitelist of env var names forwarded to the tool (account /
// session / format selectors only — never arbitrary container env, which could
// smuggle behavior into the device-side process).
var EnvAllow = map[string]bool{
	"OP_ACCOUNT": true, "OP_CONNECT_HOST": true, "OP_CONNECT_TOKEN": true,
	"OP_FORMAT": true, "OP_ISO_TIMESTAMPS": true, "OP_CACHE": true,
	"GH_REPO": true, "AWS_PROFILE": true, "AWS_REGION": true,
}

var opDenied = map[string]bool{
	"run": true, "inject": true, "plugin": true, "signin": true, "signout": true,
	"account": true, "user": true, "group": true, "service-account": true,
	"connect": true, "vault": true,
}

// OpPolicy rejects op subcommands that turn `op` into arbitrary local exec or
// broad account control, plus the `--` separator (the gateway to `op run`).
func OpPolicy(argv []string) error {
	sub := ""
	for _, a := range argv {
		if a == "--" {
			return fmt.Errorf("op: `--` argument separator is not permitted over the device link")
		}
		if sub == "" && !strings.HasPrefix(a, "-") {
			sub = a
		}
	}
	if sub == "" {
		return fmt.Errorf("op: a subcommand is required")
	}
	if opDenied[sub] {
		return fmt.Errorf("op %s is not permitted over the device link", sub)
	}
	return nil
}

// Serve runs the device side: read the request, resolve+authorize the command,
// run it, and stream stdout/stderr/exit back. resolve is authoritative.
func Serve(conn io.ReadWriteCloser, resolve Resolver) {
	defer conn.Close()
	hdr, err := readHeader(conn)
	if err != nil {
		return
	}
	fw := &frameWriter{w: conn}
	binary, policy, ok := resolve(hdr.Name)
	if !ok {
		_ = fw.frame(fStderr, []byte("shellraiser: command "+hdr.Name+" is not exposed by this device\n"))
		_ = writeExit(fw, 126)
		return
	}
	if policy != nil {
		if err := policy(hdr.Argv); err != nil {
			_ = fw.frame(fStderr, []byte("shellraiser: "+err.Error()+"\n"))
			_ = writeExit(fw, 126)
			return
		}
	}
	cmd := exec.Command(binary, hdr.Argv...)
	cmd.Env = curatedEnv(hdr.Env)
	stdinPipe, _ := cmd.StdinPipe()
	cmd.Stdout = tagWriter{fw, fStdout}
	cmd.Stderr = tagWriter{fw, fStderr}
	if err := cmd.Start(); err != nil {
		_ = fw.frame(fStderr, []byte("shellraiser: "+hdr.Name+": "+err.Error()+"\n"))
		_ = writeExit(fw, 127)
		return
	}
	go func() {
		defer stdinPipe.Close()
		for {
			tag, p, err := readFrame(conn)
			if err != nil {
				return
			}
			switch tag {
			case fStdin:
				if _, err := stdinPipe.Write(p); err != nil {
					return
				}
			case fStdinEOF:
				return
			}
		}
	}()
	code := 0
	if err := cmd.Wait(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			code = 1
		}
	}
	_ = writeExit(fw, code)
}

// Shim runs the container side: send the request, pump stdin, render
// stdout/stderr, and return the tool's exit code.
func Shim(conn io.ReadWriteCloser, name string, argv []string, env map[string]string, in io.Reader, out, errw io.Writer) int {
	defer conn.Close()
	if err := writeHeader(conn, Header{Name: name, Argv: argv, Env: env}); err != nil {
		fmt.Fprintln(errw, "shellraiser: command relay not available:", err)
		return 127
	}
	fw := &frameWriter{w: conn}
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				if ferr := fw.frame(fStdin, buf[:n]); ferr != nil {
					return
				}
			}
			if err != nil {
				_ = fw.frame(fStdinEOF, nil)
				return
			}
		}
	}()
	for {
		tag, p, err := readFrame(conn)
		if err != nil {
			fmt.Fprintln(errw, "shellraiser: command link closed:", err)
			return 1
		}
		switch tag {
		case fStdout:
			_, _ = out.Write(p)
		case fStderr:
			_, _ = errw.Write(p)
		case fExit:
			if len(p) >= 4 {
				return int(binary.BigEndian.Uint32(p))
			}
			return 0
		}
	}
}

type frameWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (fw *frameWriter) frame(tag byte, p []byte) error {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	var h [5]byte
	h[0] = tag
	binary.BigEndian.PutUint32(h[1:], uint32(len(p)))
	if _, err := fw.w.Write(h[:]); err != nil {
		return err
	}
	if len(p) > 0 {
		_, err := fw.w.Write(p)
		return err
	}
	return nil
}

func readFrame(r io.Reader) (byte, []byte, error) {
	var h [5]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(h[1:])
	if n > maxFrame {
		return 0, nil, fmt.Errorf("frame too large: %d", n)
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(r, p); err != nil {
		return 0, nil, err
	}
	return h[0], p, nil
}

type tagWriter struct {
	fw  *frameWriter
	tag byte
}

func (tw tagWriter) Write(p []byte) (int, error) {
	if err := tw.fw.frame(tw.tag, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func writeExit(fw *frameWriter, code int) error {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(code))
	return fw.frame(fExit, b[:])
}

func writeHeader(w io.Writer, h Header) error {
	b, err := json.Marshal(h)
	if err != nil {
		return err
	}
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(b)))
	if _, err := w.Write(n[:]); err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

func readHeader(r io.Reader) (Header, error) {
	var n [4]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return Header{}, err
	}
	size := binary.BigEndian.Uint32(n[:])
	if size > maxFrame {
		return Header{}, fmt.Errorf("header too large")
	}
	b := make([]byte, size)
	if _, err := io.ReadFull(r, b); err != nil {
		return Header{}, err
	}
	var h Header
	return h, json.Unmarshal(b, &h)
}

func curatedEnv(env map[string]string) []string {
	var out []string
	for k, v := range env {
		if EnvAllow[k] {
			out = append(out, k+"="+v)
		}
	}
	return out
}
