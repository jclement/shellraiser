package main

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/jclement/shellraiser/internal/ui"
	"golang.org/x/crypto/ssh"
)

// The device link is an SSH connection: the device dials in (client), the backend
// accepts (server). Capabilities are all device-initiated except the coordinator's
// request to *bind* a forward — once bound, the device opens a channel per
// accepted connection back to the coordinator, which bridges it to the worker.
// See docs/device-link.md.
//
// Wire vocabulary (custom names so it never collides with stock SSH):
//   client→server  global request  hello@sr        {Name, Capabilities}
//   client→server  global request  keepalive@sr    -
//   server→client  global request  bind-forward@sr {Tag, Want, Strict} → {Port}
//   server→client  global request  unbind-forward@sr {Tag}
//   server→client  global request  open-url@sr     {URL}
//   client→server  channel         forward-conn@sr ext={Tag}   (a forwarded conn)
//   server→client  channel         agent@sr                    (relay device agent)

type helloMsg struct {
	Name         string
	Capabilities string // comma-joined cap names (ssh.Marshal has no string slice)
	Commands     string // comma-joined exposed CLI command names
}

type bindForwardReq struct {
	Tag    string
	Want   uint32
	Strict bool
}

type bindForwardResp struct {
	Port uint32
}

type tagMsg struct{ Tag string }

type urlMsg struct{ URL string }

// startDeviceLink starts the device-link SSH server if device_link_addr is set
// (otherwise a no-op — today's local-only behavior). Auth is fail-closed, so an
// empty authorized_devices allowlist rejects every connection.
func startDeviceLink(co *Coordinator, dir string) {
	addr := hostCfg.DeviceLinkAddr
	if addr == "" {
		return
	}
	hostSigner, err := backendHostSigner(dir)
	if err != nil {
		ui.Warn("device", "host key: %v", err)
		return
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		ui.Warn("device", "listen %s: %v", addr, err)
		return
	}
	s := newDeviceLinkServer(co, hostSigner)
	co.devlink = s
	ui.Info("device", "device link listening on %s (host key %s)", addr, fingerprintSHA256(hostSigner.PublicKey()))
	ui.Info("device", "attach a device:  sr connect <this-backend-url>   then approve it in the UI")
	go s.Serve(ln)
}

// deviceLinkServer is the backend's SSH server for device links. Auth is the
// authorized_devices allowlist (fail closed: an empty allowlist rejects all).
type deviceLinkServer struct {
	co         *Coordinator
	cfg        *ssh.ServerConfig
	hostSigner ssh.Signer
	mu         sync.Mutex
	devices    map[string]*remoteDevice // by name; last connection wins (slice 6: per-device)
	lockout    *authLockout             // per-key rate-limit / lockout
}

func newDeviceLinkServer(co *Coordinator, hostSigner ssh.Signer) *deviceLinkServer {
	s := &deviceLinkServer{co: co, hostSigner: hostSigner, devices: map[string]*remoteDevice{}, lockout: newAuthLockout()}
	cfg := &ssh.ServerConfig{MaxAuthTries: 3, PublicKeyCallback: s.authDevice}
	cfg.AddHostKey(hostSigner)
	s.cfg = cfg
	return s
}

// authDevice authorizes a device key against config.toml's authorized_devices.
// Fail closed — unknown keys (and an empty allowlist) are rejected.
func (s *deviceLinkServer) authDevice(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
	id := conn.RemoteAddr().String()
	if !s.lockout.allow(id) {
		return nil, fmt.Errorf("too many attempts")
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
	for _, d := range hostCfg.AuthorizedDevices {
		if strings.TrimSpace(d.Key) == line {
			s.lockout.reset(id)
			// Key device identity on the authenticated pubkey fingerprint, not the
			// (user-chosen, collidable) name — two machines sharing a hostname must
			// not stomp each other's session.
			return &ssh.Permissions{Extensions: map[string]string{
				"device-name": d.Name,
				"device-fp":   fingerprintSHA256(key),
			}}, nil
		}
	}
	s.lockout.fail(id)
	return nil, fmt.Errorf("device key not authorized")
}

// Serve accepts device connections on ln until it closes.
func (s *deviceLinkServer) Serve(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go s.handleConn(c)
	}
}

func (s *deviceLinkServer) handleConn(c net.Conn) {
	_ = c.SetDeadline(time.Now().Add(15 * time.Second)) // handshake deadline
	sc, chans, reqs, err := ssh.NewServerConn(c, s.cfg)
	if err != nil {
		_ = c.Close()
		return
	}
	_ = c.SetDeadline(time.Time{}) // clear; keepalive governs liveness from here
	rd := newRemoteDevice(sc, s)
	if n := sc.Permissions.Extensions["device-name"]; n != "" {
		rd.name = n
	}
	rd.fp = sc.Permissions.Extensions["device-fp"]
	go s.handleChannels(rd, chans)
	go s.handleRequests(rd, reqs)
	go rd.keepalive()
	_ = sc.Wait()
	s.deactivate(rd)
}

func (s *deviceLinkServer) handleRequests(rd *remoteDevice, reqs <-chan *ssh.Request) {
	for req := range reqs {
		switch req.Type {
		case "hello@sr":
			var h helloMsg
			if ssh.Unmarshal(req.Payload, &h) == nil {
				rd.setHello(h)
				s.activate(rd)
			}
			reply(req, true)
		case "keepalive@sr":
			reply(req, true)
		default:
			reply(req, false)
		}
	}
}

func (s *deviceLinkServer) handleChannels(rd *remoteDevice, chans <-chan ssh.NewChannel) {
	for nc := range chans {
		if nc.ChannelType() != "forward-conn@sr" {
			_ = nc.Reject(ssh.UnknownChannelType, "unknown channel")
			continue
		}
		var ex tagMsg
		_ = ssh.Unmarshal(nc.ExtraData(), &ex)
		dial := rd.dialer(ex.Tag)
		if dial == nil {
			_ = nc.Reject(ssh.Prohibited, "unknown forward")
			continue
		}
		ch, chReqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go ssh.DiscardRequests(chReqs)
		go func() {
			remote, err := dial()
			if err != nil {
				_ = ch.Close()
				return
			}
			bridgeConn(chanConn{ch}, remote)
		}()
	}
}

// activate makes a freshly-helloed device the coordinator's active host-presence
// (last-connected-wins until per-device routing lands).
func (s *deviceLinkServer) activate(rd *remoteDevice) {
	s.mu.Lock()
	s.devices[rd.key()] = rd
	s.mu.Unlock()
	s.co.setDevice(rd)
	ui.Info("device", "%q connected (caps: %s)", rd.name, rd.capList())
}

// deviceInfo is the UI-facing view of a connected device.
type deviceInfo struct {
	Name         string   `json:"name"`
	Fingerprint  string   `json:"fingerprint"`
	Capabilities []string `json:"capabilities"`
	Commands     []string `json:"commands"`
}

// list returns every currently-connected device (for the UI / `sr devices`).
func (s *deviceLinkServer) list() []deviceInfo {
	s.mu.Lock()
	devs := make([]*remoteDevice, 0, len(s.devices))
	for _, d := range s.devices {
		devs = append(devs, d)
	}
	s.mu.Unlock()
	out := make([]deviceInfo, 0, len(devs))
	for _, d := range devs {
		d.mu.Lock()
		caps := make([]string, 0, len(d.caps))
		for c := range d.caps {
			caps = append(caps, c)
		}
		out = append(out, deviceInfo{
			Name: d.name, Fingerprint: d.fp,
			Capabilities: caps, Commands: append([]string(nil), d.cmdList...),
		})
		d.mu.Unlock()
	}
	return out
}

func (s *deviceLinkServer) deactivate(rd *remoteDevice) {
	s.mu.Lock()
	if s.devices[rd.key()] == rd {
		delete(s.devices, rd.key())
	}
	s.mu.Unlock()
	s.co.clearDevice(rd)
	ui.Info("device", "%q disconnected", rd.name)
}

// remoteDevice is the coordinator-side handle to a connected device: it satisfies
// the Device interface by translating Forward/DialAgent/OpenURL into requests and
// channels over the SSH link.
type remoteDevice struct {
	conn    *ssh.ServerConn
	server  *deviceLinkServer
	mu      sync.Mutex
	name    string
	fp      string // authenticated pubkey fingerprint — the stable identity key
	caps    map[string]bool
	cmdList []string // forwarded CLI commands this device exposes (op, gh, …)
	tags    map[string]DialFunc
	seq     int
}

// key is the stable identity for the connected-devices map: the pubkey
// fingerprint, falling back to name if (somehow) absent.
func (d *remoteDevice) key() string {
	if d.fp != "" {
		return d.fp
	}
	return d.name
}

func newRemoteDevice(conn *ssh.ServerConn, s *deviceLinkServer) *remoteDevice {
	return &remoteDevice{conn: conn, server: s, name: "device", caps: map[string]bool{}, tags: map[string]DialFunc{}}
}

func (d *remoteDevice) Name() string { return d.name }

func (d *remoteDevice) setHello(h helloMsg) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if h.Name != "" {
		d.name = h.Name
	}
	for _, c := range strings.Split(h.Capabilities, ",") {
		if c = strings.TrimSpace(c); c != "" {
			d.caps[c] = true
		}
	}
	d.cmdList = d.cmdList[:0]
	for _, c := range strings.Split(h.Commands, ",") {
		if c = strings.TrimSpace(c); c != "" {
			d.cmdList = append(d.cmdList, c)
		}
	}
}

func (d *remoteDevice) Grants(cap string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.caps[cap]
}

func (d *remoteDevice) capList() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var cs []string
	for c := range d.caps {
		cs = append(cs, c)
	}
	return strings.Join(cs, ",")
}

func (d *remoteDevice) dialer(tag string) DialFunc {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.tags[tag]
}

func (d *remoteDevice) Forward(want int, strict bool, dial DialFunc) (int, func(), error) {
	if !d.Grants(capBindPort) {
		return 0, nil, fmt.Errorf("device %q has not granted bind_port", d.name)
	}
	d.mu.Lock()
	d.seq++
	tag := fmt.Sprintf("f%d", d.seq)
	d.tags[tag] = dial
	d.mu.Unlock()

	ok, replyBytes, err := d.conn.SendRequest("bind-forward@sr", true, ssh.Marshal(bindForwardReq{Tag: tag, Want: uint32(want), Strict: strict}))
	if err != nil || !ok {
		d.mu.Lock()
		delete(d.tags, tag)
		d.mu.Unlock()
		if err == nil {
			err = fmt.Errorf("device refused the bind")
		}
		return 0, nil, err
	}
	var resp bindForwardResp
	if err := ssh.Unmarshal(replyBytes, &resp); err != nil {
		return 0, nil, fmt.Errorf("bad bind reply: %w", err)
	}
	closer := func() {
		_, _, _ = d.conn.SendRequest("unbind-forward@sr", false, ssh.Marshal(tagMsg{Tag: tag}))
		d.mu.Lock()
		delete(d.tags, tag)
		d.mu.Unlock()
	}
	return int(resp.Port), closer, nil
}

func (d *remoteDevice) AgentAvailable() bool { return d.Grants(capSSHAgent) }

func (d *remoteDevice) DialAgent() (net.Conn, error) {
	if !d.Grants(capSSHAgent) {
		return nil, fmt.Errorf("device %q has not granted ssh_agent", d.name)
	}
	ch, reqs, err := d.conn.OpenChannel("agent@sr", nil)
	if err != nil {
		return nil, err
	}
	go ssh.DiscardRequests(reqs)
	return chanConn{ch}, nil
}

func (d *remoteDevice) CmdAvailable() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.cmdList) > 0
}

func (d *remoteDevice) DialCmd() (net.Conn, error) {
	if !d.CmdAvailable() {
		return nil, fmt.Errorf("device %q exposes no commands", d.name)
	}
	ch, reqs, err := d.conn.OpenChannel("cmd@sr", nil)
	if err != nil {
		return nil, err
	}
	go ssh.DiscardRequests(reqs)
	return chanConn{ch}, nil
}

func (d *remoteDevice) OpenURL(url string) error {
	_, _, err := d.conn.SendRequest("open-url@sr", false, ssh.Marshal(urlMsg{URL: url}))
	return err
}

// keepalive pings the device with app-level keepalive@sr requests; a failed ping
// (or one that hangs past the deadline) tears the connection down so the device's
// reconnect loop takes over.
func (d *remoteDevice) keepalive() {
	t := time.NewTicker(50 * time.Second)
	defer t.Stop()
	for range t.C {
		done := make(chan error, 1)
		go func() {
			_, _, err := d.conn.SendRequest("keepalive@sr", true, nil)
			done <- err
		}()
		select {
		case err := <-done:
			if err != nil {
				_ = d.conn.Close()
				return
			}
		case <-time.After(10 * time.Second):
			_ = d.conn.Close()
			return
		}
	}
}

func reply(req *ssh.Request, ok bool) {
	if req.WantReply {
		_ = req.Reply(ok, nil)
	}
}

// chanConn adapts an ssh.Channel to net.Conn so bridgeConn can pipe it.
type chanConn struct{ ssh.Channel }

func (chanConn) LocalAddr() net.Addr              { return chanAddr{} }
func (chanConn) RemoteAddr() net.Addr             { return chanAddr{} }
func (chanConn) SetDeadline(time.Time) error      { return nil }
func (chanConn) SetReadDeadline(time.Time) error  { return nil }
func (chanConn) SetWriteDeadline(time.Time) error { return nil }

type chanAddr struct{}

func (chanAddr) Network() string { return "ssh-chan" }
func (chanAddr) String() string  { return "ssh-chan" }
