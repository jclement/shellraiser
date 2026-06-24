package session

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The journal is an append-only record of what agents actually did in this
// worker: every session start (with its argv) and exit (with its code), as JSONL
// on the worker volume. It survives restarts, so after a crash/reap you can see —
// and trust — what ran, rather than what an agent claims it ran.

// JournalEntry is one line of the journal.
type JournalEntry struct {
	TS    time.Time `json:"ts"`
	Event string    `json:"event"` // "start" | "exit"
	ID    string    `json:"id"`
	Kind  Kind      `json:"kind,omitempty"`
	Title string    `json:"title,omitempty"`
	Cwd   string    `json:"cwd,omitempty"`
	Argv  []string  `json:"argv,omitempty"`
	Exit  *int      `json:"exit,omitempty"`
}

type journal struct {
	mu   sync.Mutex
	path string
}

func newJournal(dir string) *journal {
	_ = os.MkdirAll(dir, 0o755)
	return &journal{path: filepath.Join(dir, "journal.jsonl")}
}

func (j *journal) write(e JournalEntry) {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	f, err := os.OpenFile(j.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	if b, err := json.Marshal(e); err == nil {
		_, _ = f.Write(append(b, '\n'))
	}
}

// tail returns up to n of the most recent entries (oldest first), optionally
// filtered to a single worktree path.
func (j *journal) tail(n int, cwd string) []JournalEntry {
	if j == nil || n <= 0 {
		return nil
	}
	f, err := os.Open(j.path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var all []JournalEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var e JournalEntry
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		if cwd != "" && e.Cwd != cwd {
			continue
		}
		all = append(all, e)
	}
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return all
}
