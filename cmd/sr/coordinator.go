package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jclement/shellraiser/internal/auth"
	"github.com/jclement/shellraiser/internal/ui"
	"github.com/jclement/shellraiser/web"
)

// Coordinator fronts many workers behind one port. It serves the unified UI,
// owns passkey auth (enforced before every proxy hop), and reverse-proxies each
// worker's data routes under /w/<id>/, injecting the worker token. Docker is the
// source of truth (see Registry).
type Coordinator struct {
	reg     *Registry
	port    string
	auth    *auth.Manager
	act     *activity
	pm      *PortMapper
	ports   *portStore
	mu      sync.Mutex
	proxies map[string]*httputil.ReverseProxy // id@port → cached reverse proxy
}

// dataRoutes are the /w/<id>/ sub-paths proxied to the worker; anything else
// under /w/<id>/ is the (public) SPA shell, served from the coordinator's assets.
var dataPrefixes = []string{"/api/", "/ws/", "/p/", "/db", "/edit"}

func newCoordinator(port string, am *auth.Manager) *Coordinator {
	return &Coordinator{reg: newRegistry(), port: port, auth: am, act: newActivity(), proxies: map[string]*httputil.ReverseProxy{}}
}

func (c *Coordinator) proxyFor(w *Worker) *httputil.ReverseProxy {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := w.ID + "@" + w.APIPort
	if p, ok := c.proxies[key]; ok {
		return p
	}
	target, _ := url.Parse("http://127.0.0.1:" + w.APIPort)
	p := httputil.NewSingleHostReverseProxy(target)
	prefix := "/w/" + w.ID
	token := w.Token
	base := p.Director
	p.Director = func(r *http.Request) {
		base(r)
		r.URL.Path = strings.TrimPrefix(r.URL.Path, prefix)
		if r.URL.Path == "" {
			r.URL.Path = "/"
		}
		if token != "" {
			r.Header.Set("X-Shellraiser-Worker", token)
		}
	}
	p.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, fmt.Sprintf("worker unreachable: %v", err), http.StatusBadGateway)
	}
	// Silence the default "read error during body copy: unexpected EOF" spam — a
	// worker stopping or a stream (SSE/WS) being interrupted is normal; the
	// ErrorHandler above still surfaces genuine dial failures as 502s.
	p.ErrorLog = log.New(io.Discard, "", 0)
	c.proxies[key] = p
	return p
}

// handleWorker routes /w/<id>/... — data routes proxy to the worker; everything
// else is the SPA shell (so deep links like /w/<id>/ render the app).
func (c *Coordinator) handleWorker(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rest := strings.TrimPrefix(r.URL.Path, "/w/"+id)
	if rest == "" {
		rest = "/"
	}
	if !isDataRoute(rest) {
		c.serveShell(w, r) // project deep-link → app shell
		return
	}
	worker, ok := c.reg.get(id)
	if !ok {
		http.Error(w, "no such project: "+id, http.StatusNotFound)
		return
	}
	// Bare-metal: serve the in-process worker directly (no container, no proxy).
	if worker.handler != nil {
		c.act.touch(id)
		http.StripPrefix("/w/"+id, worker.handler).ServeHTTP(w, r)
		return
	}
	// Lazy-resume: a request to an idle-stopped worker transparently wakes it.
	if worker.State != "running" || worker.APIPort == "" {
		if !c.resume(worker) {
			http.Error(w, "project "+id+" could not be started", http.StatusServiceUnavailable)
			return
		}
		worker, _ = c.reg.get(id)
	}
	c.act.touch(id)
	c.proxyFor(worker).ServeHTTP(w, r)
}

func isDataRoute(rest string) bool {
	for _, p := range dataPrefixes {
		if rest == p || strings.HasPrefix(rest, p) {
			return true
		}
	}
	return false
}

// --- control plane (unix socket) ------------------------------------------
//
// The CLI talks to the running daemon over a 0600 unix socket
// (~/.config/shellraiser/sr.sock): filesystem-permission-gated, no extra TCP port,
// immune to browser CSRF/DNS-rebinding. Endpoints: health, register, shutdown.

func (c *Coordinator) controlMux() *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"port": c.port, "version": version})
	})
	m.HandleFunc("POST /register", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Project, Image string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		id := boxID(req.Project)
		worker, err := provisionWorker(id, req.Project, req.Image)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !worker.BareMetal {
			waitReady(worker) // don't hand back a URL until the container serves
			c.onWorkerUp(worker)
		}
		c.reg.put(worker)
		c.act.touch(id)
		resp := map[string]any{
			"id": id, "port": c.port,
			"authEnabled": c.auth.Enabled(),
			"hasPassword": c.auth.HasPassword(),
		}
		if pw := c.auth.TempPassword(); pw != "" {
			resp["tempPassword"] = pw
		}
		writeJSON(w, resp)
	})
	m.HandleFunc("POST /shutdown", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]bool{"ok": true})
		go func() { time.Sleep(100 * time.Millisecond); os.Exit(0) }()
	})
	return m
}

// handleAPIWorkers lists registered projects for the rail.
func (c *Coordinator) handleAPIWorkers(w http.ResponseWriter, r *http.Request) {
	c.reg.reconcile()
	out := []map[string]string{}
	for _, wk := range c.reg.list() {
		out = append(out, map[string]string{
			"id": wk.ID, "name": wk.Name, "project": wk.Project, "state": wk.State,
		})
	}
	writeJSON(w, out)
}

// handleStats aggregates live stats across all projects for the About page.
func (c *Coordinator) handleStats(w http.ResponseWriter, r *http.Request) {
	c.reg.reconcile()
	workers := c.reg.list()
	projRunning, sessTotal, sessRunning := 0, 0, 0
	for _, wk := range workers {
		if wk.State != "running" {
			continue
		}
		projRunning++
		t, run := workerSessions(wk)
		sessTotal += t
		sessRunning += run
	}
	writeJSON(w, map[string]any{
		"version":  version,
		"projects": map[string]int{"total": len(workers), "running": projRunning},
		"sessions": map[string]int{"total": sessTotal, "running": sessRunning},
	})
}

// workerSessions returns a worker's (total, running) session counts.
func workerSessions(w *Worker) (total, running int) {
	if w.BareMetal {
		if w.srv != nil {
			return w.srv.SessionStats()
		}
		return 0, 0
	}
	if w.APIPort == "" {
		return 0, 0
	}
	req, _ := http.NewRequest("GET", "http://127.0.0.1:"+w.APIPort+"/api/sessions", nil)
	if w.Token != "" {
		req.Header.Set("X-Shellraiser-Worker", w.Token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, 0
	}
	defer resp.Body.Close()
	var ss []struct {
		State string `json:"state"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&ss)
	for _, s := range ss {
		total++
		if s.State == "running" {
			running++
		}
	}
	return total, running
}

// handleWorkerAction performs a lifecycle action on a worker (stop/start/nuke).
func (c *Coordinator) handleWorkerAction(w http.ResponseWriter, r *http.Request) {
	id, action := r.PathValue("id"), r.PathValue("action")
	worker, ok := c.reg.get(id)
	if !ok {
		http.Error(w, "no such project", http.StatusNotFound)
		return
	}
	// Bare-metal: no container — stop/nuke just kill the in-process sessions and
	// drop it from the registry (re-register with `sr`).
	if worker.BareMetal {
		if action == "stop" || action == "nuke" {
			runTeardown(worker)
			if worker.srv != nil {
				worker.srv.Shutdown()
			}
			c.reg.remove(id)
		}
		writeJSON(w, map[string]bool{"ok": true})
		return
	}
	var err error
	switch action {
	case "stop":
		runTeardown(worker)
		c.pm.CloseWorker(id)
		_, err = dockerRun("stop", worker.Container)
	case "start":
		if _, err = dockerRun("start", worker.Container); err == nil {
			c.reg.adopt(id)
		}
	case "nuke":
		runTeardown(worker)
		c.pm.CloseWorker(id)
		_, _ = dockerRun("rm", "-f", worker.Container)
		_, _ = dockerRun("volume", "rm", worker.Volume)
		_ = removeNetwork(worker.Network)
		c.reg.remove(id)
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	c.reg.reconcile()
	writeJSON(w, map[string]bool{"ok": true})
}

// handleConfig gets/sets the global passthrough toggles from the UI settings.
func (c *Coordinator) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		var req struct {
			SSHPassthrough *bool `json:"sshPassthrough"`
			GitPassthrough *bool `json:"gitPassthrough"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.SSHPassthrough != nil {
			hostCfg.SSHPassthrough = *req.SSHPassthrough
		}
		if req.GitPassthrough != nil {
			hostCfg.GitPassthrough = *req.GitPassthrough
		}
		if err := saveHostConfig(configDir, hostCfg); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, map[string]any{
		"sshPassthrough": hostCfg.SSHPassthrough,
		"gitPassthrough": hostCfg.GitPassthrough,
	})
}

// handlePortMap maps/unmaps a worker port to a host-loopback port via SSH -L.
func (c *Coordinator) handlePortMap(w http.ResponseWriter, r *http.Request) {
	id, action := r.PathValue("id"), r.PathValue("action")
	port, err := strconv.Atoi(r.PathValue("port"))
	if err != nil {
		http.Error(w, "invalid port", http.StatusBadRequest)
		return
	}
	worker, ok := c.reg.get(id)
	if !ok {
		http.Error(w, "no such project", http.StatusNotFound)
		return
	}
	if worker.BareMetal {
		// On bare metal the dev server already binds host localhost — nothing to tunnel.
		writeJSON(w, map[string]any{"container": port, "host": port, "addr": "127.0.0.1:" + strconv.Itoa(port)})
		return
	}
	switch action {
	case "map":
		var req struct {
			Local int `json:"local"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		c.pm.Unmap(id, port) // re-map cleanly so a changed local port takes effect
		hostPort, err := c.pm.Map(worker, port, req.Local)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		c.ports.set(id, port, hostPort) // remember it across restarts
		writeJSON(w, map[string]any{"container": port, "host": hostPort, "addr": "127.0.0.1:" + strconv.Itoa(hostPort)})
	case "unmap":
		c.pm.Unmap(id, port)
		c.ports.del(id, port)
		writeJSON(w, map[string]bool{"ok": true})
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

// handlePortList returns a worker's active host-port mappings.
func (c *Coordinator) handlePortList(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	out := []map[string]int{}
	for cp, hp := range c.pm.List(id) {
		out = append(out, map[string]int{"container": cp, "host": hp})
	}
	writeJSON(w, out)
}

// onWorkerUp wires a freshly-running container worker into the routing layer:
// forwards its declared ports and relays the host SSH agent (when enabled).
// Bare-metal workers need neither (host processes inherit ports and the agent).
func (c *Coordinator) onWorkerUp(w *Worker) {
	if w.BareMetal {
		return
	}
	go c.autoMap(w)
	if hostCfg.SSHPassthrough {
		go func() {
			if err := c.pm.ForwardAgent(w, hostAgentSocket()); err != nil {
				ui.Info("sr", "agent relay %s: %v", w.ID, err)
			}
		}()
	}
}

// autoMap forwards the project's declared [[ports]] (from→to) plus any
// runtime-remembered mappings on registration. A stored override wins over the
// config `to` (conflict resolution); if a requested host port is busy at startup
// we fall back to an OS-assigned one so the service is still reachable.
func (c *Coordinator) autoMap(w *Worker) {
	stored := c.ports.get(w.ID)
	applied := map[int]bool{}
	apply := func(from, host int) {
		if _, err := c.pm.Map(w, from, host); err != nil && host != 0 {
			if _, err2 := c.pm.Map(w, from, 0); err2 != nil { // desired busy → OS-assign
				ui.Warn("ports", "map %s :%d: %v", w.ID, from, err)
			}
		}
		applied[from] = true
	}
	for _, p := range declaredPorts(w.Project) {
		host := p.To
		if host == 0 {
			host = p.From
		}
		if h, ok := stored[p.From]; ok {
			host = h // runtime override
		}
		apply(p.From, host)
	}
	for from, host := range stored { // ports the user mapped that aren't declared
		if !applied[from] {
			apply(from, host)
		}
	}
}

// serveShell serves the embedded SPA: real asset paths (/app.js) resolve to the
// asset; everything else (/, /w/<id>/…, unknown routes) falls back to the shell.
func (c *Coordinator) serveShell(w http.ResponseWriter, r *http.Request) {
	sub, _ := fs.Sub(web.Assets, ".")
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" || strings.HasPrefix(r.URL.Path, "/w/") {
		path = "index.html"
	}
	if _, err := fs.Stat(sub, path); err != nil {
		path = "index.html" // SPA fallback
	}
	http.ServeFileFS(w, r, sub, path)
}

// httpHandler builds the full gated HTTP handler (UI + proxy + auth), shared by
// the local TCP listener and the optional tailnet (tsnet) listener.
func (c *Coordinator) httpHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/workers", c.handleAPIWorkers)
	mux.HandleFunc("GET /api/stats", c.handleStats)
	mux.HandleFunc("GET /api/config", c.handleConfig)
	mux.HandleFunc("POST /api/config", c.handleConfig)
	mux.HandleFunc("GET /api/workers/{id}/ports", c.handlePortList)
	mux.HandleFunc("POST /api/workers/{id}/ports/{port}/{action}", c.handlePortMap)
	mux.HandleFunc("POST /api/workers/{id}/{action}", c.handleWorkerAction)
	mux.HandleFunc("/w/{id}/", c.handleWorker)
	mux.HandleFunc("/w/{id}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/w/"+r.PathValue("id")+"/", http.StatusFound)
	})
	c.auth.Mount(mux)
	mux.HandleFunc("/", c.serveShell)
	return c.gate(mux)
}

// Run serves the UI (TCP) and, if sockPath is set, the control plane (unix
// socket) until the process exits.
func (c *Coordinator) Run(sockPath string) error {
	handler := c.httpHandler()

	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for range t.C {
			c.reg.reconcile()
		}
	}()
	go func() {
		every := 60 * time.Second
		if g := idleGrace(); g > 0 && g < every {
			every = g // short grace (tests) → check promptly
		}
		t := time.NewTicker(every)
		defer t.Stop()
		for range t.C {
			c.reapIdle()
		}
	}()

	// Control plane on the unix socket (CLI ⇄ daemon).
	if sockPath != "" {
		_ = os.Remove(sockPath)
		ln, err := net.Listen("unix", sockPath)
		if err != nil {
			return fmt.Errorf("control socket: %w", err)
		}
		_ = os.Chmod(sockPath, 0o600)
		go func() { _ = http.Serve(ln, c.controlMux()) }()
	}

	addr := "127.0.0.1:" + c.port
	if !c.auth.Enabled() {
		ui.Warn("auth", "DISABLED — anyone who can reach %s controls every project", addr)
	} else if c.auth.HasPassword() {
		ui.Info("auth", "password sign-in required")
	} else {
		ui.Info("auth", "first run — sign in with one-time password: %s", c.auth.TempPassword())
	}
	ui.Ready("http://" + addr + "/")
	return http.ListenAndServe(addr, handler)
}

// gate enforces passkey auth before proxying. The SPA shell and /api/auth/* are
// public (the app gates itself via /api/auth/status); all data and control
// routes require a session.
func (c *Coordinator) gate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if coordPublic(r.URL.Path) || c.auth.Authenticated(r) {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

func coordPublic(p string) bool {
	switch p {
	case "/", "/index.html", "/app.js", "/favicon.ico", "/logo.png":
		return true
	}
	if strings.HasPrefix(p, "/api/auth/") {
		return true
	}
	// The project shell (deep link) is the public SPA; its data routes are gated.
	if strings.HasPrefix(p, "/w/") {
		parts := strings.SplitN(strings.TrimPrefix(p, "/w/"), "/", 2)
		rest := "/"
		if len(parts) == 2 {
			rest = "/" + parts[1]
		}
		return !isDataRoute(rest)
	}
	return false
}

// writeJSON mirrors the worker's helper for the coordinator's small API.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
