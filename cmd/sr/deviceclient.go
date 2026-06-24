package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jclement/shellraiser/internal/cmdrelay"
	"github.com/jclement/shellraiser/internal/ui"
	"golang.org/x/crypto/ssh"
)

// runConnect implements `sr connect <backend-url>`: it dials the backend's
// device-link SSH server, announces this device + the capabilities it grants that
// backend (from device.toml), and serves the backend's host-presence requests —
// binding worker ports to this machine's loopback, relaying this machine's SSH
// agent, opening URLs — reconnecting with backoff until interrupted.
//
// The device is authoritative: every inbound request is re-checked against the
// granted capabilities here, regardless of what the backend asks.
func runConnect(backendURL string) error {
	dir, err := globalDir()
	if err != nil {
		return err
	}
	signer, pub, err := deviceSigner(dir)
	if err != nil {
		return err
	}
	cfg, err := loadDeviceConfig(dir)
	if err != nil {
		return err
	}
	name := cfg.Name
	if name == "" {
		if name, err = os.Hostname(); err != nil || name == "" {
			name = "device"
		}
	}
	be, ok := cfg.backend(backendURL)
	if !ok || be.SSHAddr == "" {
		// Not enrolled yet — run the interactive enrollment handshake, then persist.
		be, err = enrollDevice(dir, backendURL, name, signer, pub)
		if err != nil {
			return err
		}
	}
	dc := &deviceClient{signer: signer, name: name, backend: be, dir: dir, listeners: map[string]net.Listener{}}
	dc.runLoop()
	return nil
}

// enrollDevice runs the interactive enrollment handshake: prove key possession,
// have the owner approve in the browser (comparing the printed fingerprint), then
// pin the host key + endpoint delivered over the authenticated HTTPS channel and
// persist the backend to device.toml.
func enrollDevice(dir, backendURL, name string, signer ssh.Signer, pubLine string) (deviceBackend, error) {
	pubLine = strings.TrimSpace(pubLine)
	sig, err := signer.Sign(rand.Reader, []byte(pubLine))
	if err != nil {
		return deviceBackend{}, err
	}
	var start struct {
		Code        string `json:"code"`
		Fingerprint string `json:"fingerprint"`
	}
	if err := postJSON(backendURL+"/enroll/start", map[string]string{
		"pubkey":     pubLine,
		"name":       name,
		"sig_format": sig.Format,
		"sig_blob":   base64.StdEncoding.EncodeToString(sig.Blob),
	}, &start); err != nil {
		return deviceBackend{}, fmt.Errorf("enroll start: %w", err)
	}
	ui.Info("connect", "this device's fingerprint: %s", start.Fingerprint)
	ui.Info("connect", "approve it in your browser (verify the fingerprint matches): %s/enroll?code=%s", backendURL, start.Code)
	openBrowser(backendURL + "/enroll?code=" + start.Code)

	deadline := time.Now().Add(enrollTTL)
	for time.Now().Before(deadline) {
		var st struct {
			Status       string   `json:"status"`
			Name         string   `json:"name"`
			Capabilities []string `json:"capabilities"`
			Commands     []string `json:"commands"`
			HostKey      string   `json:"host_key"`
			SSHAddr      string   `json:"ssh_addr"`
		}
		if err := getJSON(backendURL+"/enroll/status?code="+start.Code, &st); err != nil {
			return deviceBackend{}, fmt.Errorf("enroll status: %w", err)
		}
		switch st.Status {
		case "approved":
			be := deviceBackend{URL: backendURL, SSHAddr: st.SSHAddr, HostKey: st.HostKey, Capabilities: st.Capabilities, Commands: st.Commands}
			if be.SSHAddr == "" {
				return deviceBackend{}, fmt.Errorf("backend did not return an ssh endpoint")
			}
			if err := persistBackend(dir, name, be); err != nil {
				return deviceBackend{}, err
			}
			ui.Info("connect", "enrolled with %s — granting: %s", backendURL, strings.Join(st.Capabilities, ", "))
			return be, nil
		case "denied":
			return deviceBackend{}, fmt.Errorf("enrollment was denied")
		}
		// "pending" → poll again (the server already held the request ~25s)
	}
	return deviceBackend{}, fmt.Errorf("enrollment timed out — re-run `sr connect`")
}

// persistBackend writes (or replaces) a backend entry in device.toml, setting the
// device name if not already set.
func persistBackend(dir, name string, be deviceBackend) error {
	cfg, err := loadDeviceConfig(dir)
	if err != nil {
		return err
	}
	if cfg.Name == "" {
		cfg.Name = name
	}
	replaced := false
	for i := range cfg.Backends {
		if cfg.Backends[i].URL == be.URL {
			cfg.Backends[i] = be
			replaced = true
			break
		}
	}
	if !replaced {
		cfg.Backends = append(cfg.Backends, be)
	}
	return saveDeviceConfig(dir, cfg)
}

func postJSON(url string, body any, out any) error {
	b, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func getJSON(url string, out any) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type deviceClient struct {
	signer    ssh.Signer
	name      string
	backend   deviceBackend
	dir       string
	mu        sync.Mutex
	listeners map[string]net.Listener
}

func (dc *deviceClient) grants(capability string) bool {
	for _, c := range dc.backend.Capabilities {
		if c == capability {
			return true
		}
	}
	return false
}

// runLoop dials the backend, serving until the link drops, then reconnects with
// exponential backoff + jitter. Ctrl-C exits cleanly.
func (dc *deviceClient) runLoop() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	backoff := time.Second
	for {
		err := dc.connectOnce(sig)
		if err == errInterrupted {
			ui.Info("connect", "disconnected")
			return
		}
		ui.Warn("connect", "link down: %v — retrying in %s", err, backoff.Round(time.Second))
		select {
		case <-sig:
			return
		case <-time.After(backoff):
		}
		// jittered exponential backoff, capped at 30s
		backoff = min(backoff*2, 30*time.Second)
	}
}

var errInterrupted = fmt.Errorf("interrupted")

func (dc *deviceClient) connectOnce(sig chan os.Signal) error {
	netConn, err := net.DialTimeout("tcp", dc.backend.SSHAddr, 10*time.Second)
	if err != nil {
		return err
	}
	ccfg := &ssh.ClientConfig{
		User:            "device",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(dc.signer)},
		HostKeyCallback: dc.hostKeyCallback(),
		Timeout:         10 * time.Second,
	}
	conn, chans, reqs, err := ssh.NewClientConn(netConn, dc.backend.SSHAddr, ccfg)
	if err != nil {
		_ = netConn.Close()
		return err
	}
	defer conn.Close()

	if _, _, err := conn.SendRequest("hello@sr", true, ssh.Marshal(helloMsg{
		Name:         dc.name,
		Capabilities: strings.Join(dc.backend.Capabilities, ","),
		Commands:     strings.Join(dc.backend.Commands, ","),
	})); err != nil {
		return err
	}
	grants := append([]string{}, dc.backend.Capabilities...)
	for _, cmd := range dc.backend.Commands {
		grants = append(grants, "cmd:"+cmd)
	}
	ui.Info("connect", "linked to %s as %q (granting: %s)", dc.backend.URL, dc.name, strings.Join(grants, ", "))

	go dc.serveChannels(chans)
	go dc.keepalive(conn) // detect a dead/powered-off backend fast → reconnect
	done := make(chan error, 1)
	go func() { done <- dc.serveRequests(conn, reqs) }()

	select {
	case <-sig:
		dc.closeAll()
		return errInterrupted
	case err := <-done:
		dc.closeAll()
		if err == nil {
			err = fmt.Errorf("connection closed")
		}
		return err
	}
}

// keepalive pings the backend with app-level keepalive@sr requests and a read
// deadline. A powered-off backend leaves the TCP connection half-open for minutes
// otherwise — hanging every bound listener; here a missed ping closes the conn so
// the reconnect loop (and re-binding) takes over within ~a minute.
func (dc *deviceClient) keepalive(conn ssh.Conn) {
	t := time.NewTicker(50 * time.Second)
	defer t.Stop()
	for range t.C {
		done := make(chan error, 1)
		go func() { _, _, err := conn.SendRequest("keepalive@sr", true, nil); done <- err }()
		select {
		case err := <-done:
			if err != nil {
				_ = conn.Close()
				return
			}
		case <-time.After(10 * time.Second):
			_ = conn.Close()
			return
		}
	}
}

// serveRequests handles backend→device global requests. It returns when the
// request channel closes (the link dropped).
func (dc *deviceClient) serveRequests(conn ssh.Conn, reqs <-chan *ssh.Request) error {
	for req := range reqs {
		switch req.Type {
		case "bind-forward@sr":
			dc.handleBind(conn, req)
		case "unbind-forward@sr":
			var m tagMsg
			_ = ssh.Unmarshal(req.Payload, &m)
			dc.unbind(m.Tag)
			reply(req, true)
		case "open-url@sr":
			dc.handleOpenURL(req)
		case "keepalive@sr":
			reply(req, true)
		default:
			reply(req, false)
		}
	}
	return nil
}

// serveChannels handles backend→device channels: agent relay and command relay.
func (dc *deviceClient) serveChannels(chans <-chan ssh.NewChannel) {
	for nc := range chans {
		switch nc.ChannelType() {
		case "agent@sr":
			dc.serveAgentChannel(nc)
		case "cmd@sr":
			dc.serveCmdChannel(nc)
		default:
			_ = nc.Reject(ssh.UnknownChannelType, "unknown channel")
		}
	}
}

func (dc *deviceClient) serveAgentChannel(nc ssh.NewChannel) {
	if !dc.grants(capSSHAgent) {
		_ = nc.Reject(ssh.Prohibited, "ssh_agent not granted")
		return
	}
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		_ = nc.Reject(ssh.ConnectionFailed, "no ssh agent on device")
		return
	}
	ch, reqs, err := nc.Accept()
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	go func() {
		agent, err := net.Dial("unix", sock)
		if err != nil {
			_ = ch.Close()
			return
		}
		bridgeConn(chanConn{ch}, agent)
	}()
}

// serveCmdChannel runs an exposed CLI command on the device. The command name +
// argv arrive in the cmdrelay header; the device authorizes against its exposed
// list (device.toml commands) — op gets the extra subcommand policy.
func (dc *deviceClient) serveCmdChannel(nc ssh.NewChannel) {
	if len(dc.backend.Commands) == 0 {
		_ = nc.Reject(ssh.Prohibited, "no commands exposed")
		return
	}
	ch, reqs, err := nc.Accept()
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	go cmdrelay.Serve(chanConn{ch}, cmdrelay.Exposer(dc.backend.Commands))
}

// handleBind honors a bind-forward@sr request: listen locally (device-side
// capability re-check), and on each accept open a forward-conn@sr channel back so
// the coordinator bridges it to the worker.
func (dc *deviceClient) handleBind(conn ssh.Conn, req *ssh.Request) {
	if !dc.grants(capBindPort) {
		reply(req, false)
		return
	}
	var rq bindForwardReq
	if err := ssh.Unmarshal(req.Payload, &rq); err != nil {
		reply(req, false)
		return
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(int(rq.Want)))
	if err != nil {
		if rq.Strict {
			reply(req, false)
			return
		}
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			reply(req, false)
			return
		}
	}
	port := ln.Addr().(*net.TCPAddr).Port
	dc.mu.Lock()
	dc.listeners[rq.Tag] = ln
	dc.mu.Unlock()
	go dc.acceptForward(conn, ln, rq.Tag)
	_ = req.Reply(true, ssh.Marshal(bindForwardResp{Port: uint32(port)}))
	ui.Info("connect", "bound 127.0.0.1:%d (worker port forwarded here)", port)
}

func (dc *deviceClient) acceptForward(conn ssh.Conn, ln net.Listener, tag string) {
	for {
		local, err := ln.Accept()
		if err != nil {
			return // closed via unbind / shutdown
		}
		go func() {
			ch, reqs, err := conn.OpenChannel("forward-conn@sr", ssh.Marshal(tagMsg{Tag: tag}))
			if err != nil {
				_ = local.Close()
				return
			}
			go ssh.DiscardRequests(reqs)
			bridgeConn(chanConn{ch}, local)
		}()
	}
}

func (dc *deviceClient) unbind(tag string) {
	dc.mu.Lock()
	ln := dc.listeners[tag]
	delete(dc.listeners, tag)
	dc.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
}

func (dc *deviceClient) closeAll() {
	dc.mu.Lock()
	for tag, ln := range dc.listeners {
		_ = ln.Close()
		delete(dc.listeners, tag)
	}
	dc.mu.Unlock()
}

// handleOpenURL opens a URL from the backend — bounded to the backend's own
// origin so a compromised backend can't navigate the device's browser anywhere
// (open-URL for worker pages is same-origin; arbitrary navigation belongs to the
// in-container browser bridge, not the device link).
func (dc *deviceClient) handleOpenURL(req *ssh.Request) {
	var m urlMsg
	if err := ssh.Unmarshal(req.Payload, &m); err != nil {
		reply(req, false)
		return
	}
	if !sameOrigin(m.URL, dc.backend.URL) {
		ui.Warn("connect", "ignored open-url for foreign origin: %s", m.URL)
		reply(req, false)
		return
	}
	openBrowser(m.URL)
	reply(req, true)
}

func sameOrigin(a, b string) bool {
	ua, err1 := url.Parse(a)
	ub, err2 := url.Parse(b)
	return err1 == nil && err2 == nil && ua.Scheme == ub.Scheme && ua.Host == ub.Host
}

// hostKeyCallback pins the backend host key. With a pinned fingerprint it verifies
// strictly; with none it TOFU-pins (slice 3 replaces this with the fingerprint
// delivered over the authenticated HTTPS enrollment channel).
func (dc *deviceClient) hostKeyCallback() ssh.HostKeyCallback {
	pinned := dc.backend.HostKey
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		fp := fingerprintSHA256(key)
		if pinned == "" {
			ui.Warn("connect", "pinning backend host key %s (trust-on-first-use)", fp)
			dc.pinHostKey(fp)
			return nil
		}
		if fp != pinned {
			return fmt.Errorf("backend host key mismatch: presented %s, pinned %s (re-enroll if the backend was legitimately reset)", fp, pinned)
		}
		return nil
	}
}

func (dc *deviceClient) pinHostKey(fp string) {
	cfg, err := loadDeviceConfig(dc.dir)
	if err != nil {
		return
	}
	for i := range cfg.Backends {
		if cfg.Backends[i].URL == dc.backend.URL {
			cfg.Backends[i].HostKey = fp
			dc.backend.HostKey = fp
			_ = saveDeviceConfig(dc.dir, cfg)
			return
		}
	}
}
