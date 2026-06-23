// Package server wires the HTTP API, websocket terminal bridge, /db proxy, and
// embedded web UI together.
package server

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jclement/shellraiser/internal/auth"
	"github.com/jclement/shellraiser/internal/config"
	"github.com/jclement/shellraiser/internal/session"
	"github.com/jclement/shellraiser/internal/ui"
	"github.com/jclement/shellraiser/internal/worktree"
	"github.com/jclement/shellraiser/web"
)

// Server is the shellraiser HTTP server.
type Server struct {
	repoDir      string
	worktreesDir string
	cfg          config.Config
	mgr          *session.Manager
	auth         *auth.Manager
	meta         *metaStore
	repoName     string
	commands     map[string]config.Command
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     sameOrigin,
}

// sameOrigin rejects cross-origin websocket upgrades (CSWSH protection). A
// missing Origin header means a non-browser client, which is allowed.
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// New validates the repo and constructs a Server from merged config.
func New(repoDir string, cfg config.Config) (*Server, error) {
	if !worktree.IsRepo(repoDir) {
		return nil, fmt.Errorf("%s is not a git repository", repoDir)
	}
	worktreesDir := cfg.WorktreesDir
	if worktreesDir == "" {
		worktreesDir = filepath.Join(repoDir, ".worktrees")
	}
	// Re-link any worktree folders on disk that lost their git links.
	worktree.Repair(repoDir, worktreesDir)

	commands := map[string]config.Command{}
	for _, c := range cfg.Commands {
		commands[c.Name] = c
	}
	// In v2 the worker is a backend behind the coordinator (which owns auth); it
	// runs with auth disabled and is fenced by the per-worker token instead. The
	// manager is kept (it serves /api/auth/status) but is always in no-auth mode.
	am := auth.New("", nil, true)
	am.Logf = func(format string, a ...any) { ui.Info("auth", format, a...) }
	// Worktree colors/names persist in the home mount.
	home := os.Getenv("HOME")
	if home == "" {
		home = repoDir
	}
	stateDir := filepath.Join(home, ".local", "share", "shellraiser")
	_ = os.MkdirAll(stateDir, 0o700)
	// Repo display name: config override → git remote → mount-path basename.
	repoName := cfg.Name
	if repoName == "" {
		repoName = worktree.RemoteName(repoDir)
	}
	if repoName == "" {
		repoName = filepath.Base(repoDir)
	}

	return &Server{
		repoDir:      repoDir,
		worktreesDir: worktreesDir,
		cfg:          cfg,
		auth:         am,
		meta:         newMetaStore(filepath.Join(stateDir, "worktree-meta.json")),
		repoName:     repoName,
		commands:     commands,
		mgr: session.NewManager(session.Commands{
			Shell: cfg.Shell, Editor: cfg.Editor, Claude: cfg.Claude, Codex: cfg.Codex,
		}),
	}, nil
}

// Run starts serving and blocks.
func (s *Server) Run() error {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/info", s.handleInfo)
	mux.HandleFunc("GET /api/commands", s.handleCommands)
	mux.HandleFunc("GET /api/worktrees", s.handleListWorktrees)
	mux.HandleFunc("GET /api/branches", s.handleListBranches)
	mux.HandleFunc("POST /api/worktrees", s.handleCreateWorktree)
	mux.HandleFunc("POST /api/worktrees/color", s.handleSetWorktreeColor)
	mux.HandleFunc("POST /api/worktrees/rename", s.handleRenameWorktree)
	mux.HandleFunc("POST /api/worktrees/reorder", s.handleReorderWorktrees)
	mux.HandleFunc("DELETE /api/worktrees", s.handleRemoveWorktree)
	mux.HandleFunc("GET /api/sessions", s.handleListSessions)
	mux.HandleFunc("POST /api/sessions", s.handleCreateSession)
	mux.HandleFunc("DELETE /api/sessions/{id}", s.handleKillSession)
	mux.HandleFunc("GET /api/ports", s.handlePorts)
	mux.HandleFunc("POST /api/ssh/command", s.handleSSHCommand)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("GET /ws/term/{id}", s.handleTermWS)

	// pgweb reverse proxy (runs with --prefix=db, so paths pass through as-is).
	if s.cfg.PostgresEnabled() {
		target, _ := url.Parse("http://127.0.0.1:8081")
		proxy := httputil.NewSingleHostReverseProxy(target)
		mux.Handle("/db/", proxy)
		mux.HandleFunc("/db", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/db/", http.StatusFound)
		})
	}

	// code-server reverse proxy at /edit (its assets are relative, so we strip
	// the /edit prefix before forwarding; websockets pass through).
	if s.cfg.CodeServerEnabled() {
		target, _ := url.Parse("http://127.0.0.1:8082")
		proxy := httputil.NewSingleHostReverseProxy(target)
		base := proxy.Director
		proxy.Director = func(r *http.Request) {
			base(r)
			r.URL.Path = strings.TrimPrefix(r.URL.Path, "/edit")
			if r.URL.Path == "" {
				r.URL.Path = "/"
			}
		}
		mux.Handle("/edit/", proxy)
		mux.HandleFunc("/edit", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/edit/", http.StatusFound)
		})
	}

	// Reach an internal dev-server port through shellraiser at /p/<port>/ — proxies
	// to 127.0.0.1:<port> inside the container (so it works even for loopback-
	// bound servers), behind the same auth, no docker -p needed. Subpath caveat:
	// SPAs with absolute asset paths need their base set to /p/<port>/.
	mux.HandleFunc("/p/{port}/", s.handlePortProxy)
	mux.HandleFunc("/p/{port}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/p/"+r.PathValue("port")+"/", http.StatusFound)
	})

	s.auth.Mount(mux)
	mux.HandleFunc("/", s.handleStatic)

	ui.Banner()
	ui.Boot("shellraiser",
		"repo", filepath.Base(s.repoDir),
		"worktrees", s.worktreesDir,
		"postgres", fmt.Sprintf("%v", s.cfg.PostgresEnabled()),
		"addr", s.cfg.Addr,
	)
	go s.logEvents()
	s.printAccess()

	return http.ListenAndServe(s.cfg.Addr, s.gate(mux))
}

// gate enforces auth on data/proxy routes while leaving the static UI and the
// /api/auth/* endpoints public (the SPA gates itself via /api/auth/status).
//
// In v2 the worker is an untrusted backend reached only through the coordinator.
// When SHELLRAISER_WORKER_TOKEN is set, EVERY request (static included) must carry
// the matching X-Shellraiser-Worker header — loopback binding is not authentication
// on a shared host, so this fences out any other local process. The coordinator
// injects the header on every proxied hop.
func (s *Server) gate(next http.Handler) http.Handler {
	workerToken := os.Getenv("SHELLRAISER_WORKER_TOKEN")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if workerToken != "" &&
			subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Shellraiser-Worker")), []byte(workerToken)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if publicPath(r.URL.Path) || s.auth.Authenticated(r) {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

// publicPath is a strict allowlist (default-deny): only the login SPA shell and
// the auth endpoints are reachable before you've passkey'd in. Everything else
// — all data APIs, the terminal websocket, and the /db (pgweb) and /edit
// (code-server) proxies — requires a valid session.
func publicPath(p string) bool {
	switch p {
	case "/", "/index.html", "/app.js", "/favicon.ico":
		return true
	}
	return strings.HasPrefix(p, "/api/auth/")
}

// logEvents pretty-prints agent "done" and session exit events to the console.
func (s *Server) logEvents() {
	ch, _ := s.mgr.Events()
	for ev := range ch {
		switch {
		case ev.State == session.StateExited:
			ui.Exit(string(ev.Kind), ev.Title, ev.ExitCode)
		case ev.Ding:
			ui.Done(string(ev.Kind), ev.Title, filepath.Base(ev.Cwd))
		}
	}
}

func (s *Server) printAccess() {
	host := s.cfg.Addr
	if strings.HasPrefix(host, ":") {
		host = "localhost" + host
	}
	url := fmt.Sprintf("http://%s/", host)
	ui.Ready(url)
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	sub, _ := fs.Sub(web.Assets, ".")
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	if _, err := fs.Stat(sub, path); err != nil {
		path = "index.html" // SPA fallback
	}
	http.ServeFileFS(w, r, sub, path)
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"repo":         s.repoName,
		"repoDir":      s.repoDir,
		"worktreesDir": s.worktreesDir,
		"postgres":     s.cfg.PostgresEnabled(),
		"editor":       s.cfg.CodeServerEnabled(),
		"ssh":          os.Getenv("SHELLRAISER_SSH") == "1",
	})
}

func (s *Server) handleListBranches(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, worktree.Branches(s.repoDir))
}

func (s *Server) handlePortProxy(w http.ResponseWriter, r *http.Request) {
	portStr := r.PathValue("port")
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		http.Error(w, "invalid port", http.StatusBadRequest)
		return
	}
	target := &url.URL{Scheme: "http", Host: "127.0.0.1:" + portStr}
	proxy := httputil.NewSingleHostReverseProxy(target)
	prefix := "/p/" + portStr
	base := proxy.Director
	proxy.Director = func(req *http.Request) {
		base(req)
		req.URL.Path = strings.TrimPrefix(req.URL.Path, prefix)
		if !strings.HasPrefix(req.URL.Path, "/") {
			req.URL.Path = "/" + req.URL.Path
		}
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, fmt.Sprintf("nothing reachable on 127.0.0.1:%s (%v)", portStr, err), http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

func (s *Server) handleCommands(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Commands == nil {
		writeJSON(w, []config.Command{})
		return
	}
	writeJSON(w, s.cfg.Commands)
}

func (s *Server) handleListWorktrees(w http.ResponseWriter, r *http.Request) {
	trees, err := worktree.List(s.repoDir)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	for i := range trees {
		trees[i].Color, trees[i].DisplayName, trees[i].Order = s.meta.get(trees[i].Path)
	}
	// Manual order first (1-based; 0 = unset sorts last), stable within each group.
	sort.SliceStable(trees, func(i, j int) bool {
		oi, oj := trees[i].Order, trees[j].Order
		if (oi == 0) != (oj == 0) {
			return oj == 0 // ordered entries before unordered
		}
		return oi < oj
	})
	writeJSON(w, trees)
}

func (s *Server) handleReorderWorktrees(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Paths []string `json:"paths"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.meta.setOrder(req.Paths)
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleSetWorktreeColor(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path  string `json:"path"`
		Color string `json:"color"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.meta.setColor(req.Path, req.Color)
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleRenameWorktree(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.meta.setName(req.Path, strings.TrimSpace(req.Name))
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleCreateWorktree(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string `json:"name"`
		Branch    string `json:"branch"`
		Base      string `json:"base"`
		NewBranch bool   `json:"newBranch"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	name := sanitize(req.Name)
	if name == "" {
		name = sanitize(req.Branch)
	}
	if name == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("name or branch required"))
		return
	}
	if err := os.MkdirAll(s.worktreesDir, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	path := filepath.Join(s.worktreesDir, name)
	if err := worktree.Add(s.repoDir, path, req.Branch, req.Base, req.NewBranch); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]string{"path": path, "name": name})
}

func (s *Server) handleRemoveWorktree(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path  string `json:"path"`
		Force bool   `json:"force"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// Defense in depth: only remove worktrees that live under the worktrees dir.
	if !underDir(filepath.Clean(req.Path), s.worktreesDir) {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("worktree path must be under %s", s.worktreesDir))
		return
	}
	if err := worktree.Remove(s.repoDir, req.Path, req.Force); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// underDir reports whether path is dir or strictly inside it.
func underDir(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.mgr.List())
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind    string   `json:"kind"`
		Cwd     string   `json:"cwd"`
		Title   string   `json:"title"`
		Args    []string `json:"args"`
		Command string   `json:"command"` // name of a custom command from .shellraiser.toml
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	kind := session.Kind(req.Kind)
	title := req.Title
	args := req.Args
	if req.Command != "" {
		cmd, ok := s.commands[req.Command]
		if !ok {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("unknown command %q", req.Command))
			return
		}
		args = cmd.Args
		if title == "" {
			title = cmd.Name
		}
		kind = session.Kind(cmd.Kind)
		if kind == "" {
			kind = session.KindCommand
		}
	}
	cwd := req.Cwd
	if cwd == "" {
		cwd = s.repoDir
	}
	sess, err := s.mgr.Create(session.CreateOpts{Kind: kind, Cwd: cwd, Title: title, Args: args})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	info := sess.Info()
	ui.Session("start", string(info.Kind), info.Title, filepath.Base(info.Cwd))
	writeJSON(w, info)
}

func (s *Server) handleKillSession(w http.ResponseWriter, r *http.Request) {
	if err := s.mgr.Kill(r.PathValue("id")); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handlePorts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, attribute(listeningPorts(), s.mgr.Roots()))
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, cancel := s.mgr.Events()
	defer cancel()

	// Flush headers immediately so the client knows the stream is open.
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			b, _ := json.Marshal(ev)
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

func (s *Server) handleTermWS(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.mgr.Get(r.PathValue("id"))
	if !ok {
		http.Error(w, "no such session", http.StatusNotFound)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// Keepalive: a peer that vanishes without a TCP FIN (sleep, mobile handoff,
	// dead proxy) is detected via missed pongs, so the read loop unblocks, the
	// subscriber is cancelled, and no goroutine/subscriber leaks.
	const (
		writeWait  = 10 * time.Second
		pongWait   = 60 * time.Second
		pingPeriod = 50 * time.Second
	)
	conn.SetReadLimit(1 << 20)
	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(pongWait)) })

	out, cancel := sess.Subscribe()
	defer cancel()

	go func() {
		ping := time.NewTicker(pingPeriod)
		defer ping.Stop()
		_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
		if conn.WriteMessage(websocket.BinaryMessage, sess.Snapshot()) != nil {
			conn.Close()
			return
		}
		for {
			select {
			case data, ok := <-out:
				if !ok {
					conn.Close()
					return
				}
				_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
				if conn.WriteMessage(websocket.BinaryMessage, data) != nil {
					conn.Close()
					return
				}
			case <-ping.C:
				_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
				if conn.WriteMessage(websocket.PingMessage, nil) != nil {
					conn.Close()
					return
				}
			}
		}
	}()

	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		switch mt {
		case websocket.BinaryMessage:
			sess.Write(data)
		case websocket.TextMessage:
			var msg struct {
				Type string `json:"type"`
				Data string `json:"data"`
				Cols uint16 `json:"cols"`
				Rows uint16 `json:"rows"`
			}
			if json.Unmarshal(data, &msg) == nil {
				switch msg.Type {
				case "data":
					sess.Write([]byte(msg.Data))
				case "resize":
					sess.Resize(msg.Cols, msg.Rows)
				}
			}
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// sanitize keeps worktree directory names to a safe, flat slug.
func sanitize(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "/", "-")
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		default:
			return -1
		}
	}, s)
}
