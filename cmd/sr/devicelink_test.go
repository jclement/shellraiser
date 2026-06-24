package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func testSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// echoListener accepts connections and echoes everything back (stands in for a
// worker's container port).
func echoListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()
	return ln
}

// TestDeviceLinkForward exercises the whole bind_port path: device SSH auth
// against the allowlist, hello/capabilities, a coordinator-issued bind-forward,
// and a forward-conn channel bridged to an upstream — proving bytes flow from a
// device-local listener through the link to a "worker".
func TestDeviceLinkForward(t *testing.T) {
	hostSigner := testSigner(t)
	devSigner := testSigner(t)

	hostCfg.AuthorizedDevices = []authorizedDevice{{Name: "test-dev", Key: authorizedLine(devSigner)}}
	t.Cleanup(func() { hostCfg.AuthorizedDevices = nil })

	co := &Coordinator{dev: localDevice{}, reg: newRegistry()}
	co.pm = newPortMapper(nil, localDevice{}, nil)

	srv := newDeviceLinkServer(co, hostSigner)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go srv.Serve(ln)

	dc := &deviceClient{
		signer: devSigner,
		name:   "test-dev",
		dir:    t.TempDir(),
		backend: deviceBackend{
			URL:          "https://backend.example",
			SSHAddr:      ln.Addr().String(),
			HostKey:      fingerprintSHA256(hostSigner.PublicKey()),
			Capabilities: []string{capBindPort},
		},
		listeners: map[string]net.Listener{},
	}
	sig := make(chan os.Signal, 1)
	go func() { _ = dc.connectOnce(sig) }()

	// Wait for the device to register (hello processed → activated).
	var rd *remoteDevice
	for i := 0; i < 100; i++ {
		srv.mu.Lock()
		for _, d := range srv.devices { // keyed by pubkey fingerprint now
			rd = d
		}
		srv.mu.Unlock()
		if rd != nil && rd.Grants(capBindPort) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if rd == nil {
		t.Fatal("device never registered")
	}

	upstream := echoListener(t)
	t.Cleanup(func() { _ = upstream.Close() })
	dial := func() (net.Conn, error) { return net.Dial("tcp", upstream.Addr().String()) }

	port, closer, err := rd.Forward(0, false, dial)
	if err != nil {
		t.Fatalf("Forward: %v", err)
	}
	t.Cleanup(closer)
	if port == 0 {
		t.Fatal("no port bound")
	}

	// Connect to the device-local bound port and round-trip through the link.
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", itoa(port)), 2*time.Second)
	if err != nil {
		t.Fatalf("dial bound port: %v", err)
	}
	defer conn.Close()
	msg := []byte("hello over the device link")
	if _, err := conn.Write(msg); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(msg))
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != string(msg) {
		t.Fatalf("got %q, want %q", buf, msg)
	}
}

// TestDeviceLinkRejectsUnauthorized confirms fail-closed auth: a key absent from
// authorized_devices cannot complete the handshake.
func TestDeviceLinkRejectsUnauthorized(t *testing.T) {
	hostSigner := testSigner(t)
	devSigner := testSigner(t)

	hostCfg.AuthorizedDevices = nil // empty allowlist → reject all
	srv := newDeviceLinkServer(co(t), hostSigner)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go srv.Serve(ln)

	ccfg := &ssh.ClientConfig{
		User:            "device",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(devSigner)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         2 * time.Second,
	}
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, _, _, err := ssh.NewClientConn(c, ln.Addr().String(), ccfg); err == nil {
		t.Fatal("expected auth failure for unlisted key")
	}
}

func co(t *testing.T) *Coordinator {
	t.Helper()
	c := &Coordinator{dev: localDevice{}, reg: newRegistry()}
	c.pm = newPortMapper(nil, localDevice{}, nil)
	return c
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
