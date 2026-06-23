package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jclement/slopbox/internal/ui"
)

// Coordinator fronts many workers behind one port. It serves the unified UI and
// reverse-proxies each worker under /w/<id>/, injecting the worker's token so the
// worker's gate accepts the hop. Docker is the source of truth (see Registry).
type Coordinator struct {
	reg     *Registry
	port    string
	mu      sync.Mutex
	proxies map[string]*httputil.ReverseProxy // id → cached reverse proxy
}

func newCoordinator(port string) *Coordinator {
	return &Coordinator{reg: newRegistry(), port: port, proxies: map[string]*httputil.ReverseProxy{}}
}

// proxyFor returns a reverse proxy to the worker's loopback API port, stripping
// the /w/<id> prefix and injecting the worker token on every hop (HTTP + WS
// upgrade alike — httputil handles the Upgrade since Go 1.20).
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
			r.Header.Set("X-Slopbox-Worker", token)
		}
	}
	p.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, fmt.Sprintf("worker unreachable: %v", err), http.StatusBadGateway)
	}
	c.proxies[key] = p
	return p
}

// handleWorker routes /w/<id>/... to the right worker.
func (c *Coordinator) handleWorker(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	worker, ok := c.reg.get(id)
	if !ok {
		http.Error(w, "no such project: "+id, http.StatusNotFound)
		return
	}
	if worker.State != "running" || worker.APIPort == "" {
		http.Error(w, "project "+id+" is not running", http.StatusServiceUnavailable)
		return
	}
	c.proxyFor(worker).ServeHTTP(w, r)
}

// handleAPIWorkers lists registered projects for the coordinator shell.
func (c *Coordinator) handleAPIWorkers(w http.ResponseWriter, r *http.Request) {
	c.reg.reconcile()
	var out []map[string]string
	for _, wk := range c.reg.list() {
		out = append(out, map[string]string{
			"id": wk.ID, "name": wk.Name, "project": wk.Project, "state": wk.State,
		})
	}
	writeJSON(w, out)
}

// handleRoot is a minimal coordinator landing until the unified shell lands: it
// lists projects and links into each worker's UI at /w/<id>/.
func (c *Coordinator) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	c.reg.reconcile()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!doctype html><meta charset=utf-8><title>slopbox</title>`+
		`<style>body{font:15px system-ui;background:#0f172a;color:#e2e8f0;margin:0;padding:2rem}`+
		`a{color:#93c5fd;text-decoration:none}h1{font-size:1.1rem;color:#cbd5e1}`+
		`.p{display:block;padding:.75rem 1rem;margin:.4rem 0;background:#1e293b;border-radius:.5rem}`+
		`.s{color:#64748b;font-size:.8rem}</style>`)
	fmt.Fprintf(w, `<h1>slopbox coordinator</h1>`)
	workers := c.reg.list()
	if len(workers) == 0 {
		fmt.Fprint(w, `<p class=s>No projects registered. Run <code>sb</code> in a git repo.</p>`)
		return
	}
	for _, wk := range workers {
		fmt.Fprintf(w, `<a class=p href="/w/%s/"><b>%s</b> <span class=s>%s — %s</span></a>`,
			wk.ID, htmlesc(wk.Name), wk.State, htmlesc(wk.Project))
	}
}

// Run serves until the process exits.
func (c *Coordinator) Run() error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/workers", c.handleAPIWorkers)
	mux.HandleFunc("/w/{id}/", c.handleWorker)
	mux.HandleFunc("/w/{id}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/w/"+r.PathValue("id")+"/", http.StatusFound)
	})
	mux.HandleFunc("/", c.handleRoot)

	// Background reconcile so the registry tracks docker reality.
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for range t.C {
			c.reg.reconcile()
		}
	}()

	addr := "127.0.0.1:" + c.port
	ui.Ready("http://" + addr + "/")
	return http.ListenAndServe(addr, mux)
}

func htmlesc(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

// writeJSON mirrors the worker's helper for the coordinator's small API.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
