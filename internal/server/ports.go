package server

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jclement/slopbox/internal/session"
)

// Port is a TCP port something inside the box is listening on, optionally
// attributed to the worktree whose session spawned it.
type Port struct {
	Port      int    `json:"port"`
	Process   string `json:"process,omitempty"`
	PID       int    `json:"pid,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	Worktree  string `json:"worktree,omitempty"`
}

// listeningPorts returns LISTEN-ing TCP ports, best-effort and cross-platform.
// Linux (the container) uses `ss`; macOS dev hosts fall back to `lsof`.
func listeningPorts() []Port {
	if ports := viaSS(); ports != nil {
		return ports
	}
	if ports := viaLsof(); ports != nil {
		return ports
	}
	return []Port{}
}

// attribute maps each port's owning PID to the session (and worktree) that owns
// it, by climbing the process tree until it reaches a known session root PID.
func attribute(ports []Port, roots map[int]session.Info) []Port {
	if len(roots) == 0 {
		return ports
	}
	parents := procParents()
	for i := range ports {
		cur := ports[i].PID
		for hops := 0; cur > 1 && hops < 64; hops++ {
			if info, ok := roots[cur]; ok {
				ports[i].SessionID = info.ID
				ports[i].Worktree = info.Cwd
				break
			}
			next, ok := parents[cur]
			if !ok {
				break
			}
			cur = next
		}
	}
	return ports
}

func viaSS() []Port {
	out, err := exec.Command("ss", "-H", "-tlnp").Output()
	if err != nil {
		return nil
	}
	seen := map[int]Port{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		port := portFromAddr(fields[3])
		if port <= 0 {
			continue
		}
		if _, ok := seen[port]; !ok {
			proc, pid := procFromSS(line)
			seen[port] = Port{Port: port, Process: proc, PID: pid}
		}
	}
	return collect(seen)
}

func viaLsof() []Port {
	out, err := exec.Command("lsof", "-nP", "-iTCP", "-sTCP:LISTEN").Output()
	if err != nil {
		return nil
	}
	seen := map[int]Port{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 9 || fields[0] == "COMMAND" {
			continue
		}
		port := portFromAddr(fields[8])
		if port <= 0 {
			continue
		}
		pid, _ := strconv.Atoi(fields[1])
		if _, ok := seen[port]; !ok {
			seen[port] = Port{Port: port, Process: fields[0], PID: pid}
		}
	}
	return collect(seen)
}

func portFromAddr(addr string) int {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return 0
	}
	p, err := strconv.Atoi(addr[i+1:])
	if err != nil {
		return 0
	}
	return p
}

// procFromSS parses `users:(("proc",pid=1234,fd=5))` into name + pid.
func procFromSS(line string) (string, int) {
	i := strings.Index(line, `(("`)
	if i < 0 {
		return "", 0
	}
	rest := line[i+3:]
	name := rest
	if j := strings.Index(rest, `"`); j >= 0 {
		name = rest[:j]
	}
	pid := 0
	if k := strings.Index(rest, "pid="); k >= 0 {
		num := rest[k+4:]
		end := strings.IndexAny(num, ",)")
		if end >= 0 {
			pid, _ = strconv.Atoi(num[:end])
		}
	}
	return name, pid
}

// procParents builds pid→ppid from /proc (Linux only; empty elsewhere).
func procParents() map[int]int {
	parents := map[int]int{}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return parents
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue
		}
		// Format: "pid (comm) state ppid ..." — comm may contain spaces/parens,
		// so split after the last ')'.
		s := string(data)
		close := strings.LastIndex(s, ")")
		if close < 0 {
			continue
		}
		rest := strings.Fields(s[close+1:])
		if len(rest) < 2 {
			continue
		}
		if ppid, err := strconv.Atoi(rest[1]); err == nil {
			parents[pid] = ppid
		}
	}
	return parents
}

func collect(seen map[int]Port) []Port {
	ports := make([]Port, 0, len(seen))
	for _, p := range seen {
		ports = append(ports, p)
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i].Port < ports[j].Port })
	return ports
}
