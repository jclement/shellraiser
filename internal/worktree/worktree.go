// Package worktree wraps the handful of git-worktree operations slopbox needs.
package worktree

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Worktree is one entry from `git worktree list`, annotated with git stats.
type Worktree struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	Branch   string `json:"branch"`
	Head     string `json:"head"`
	Detached bool   `json:"detached"`
	Bare     bool   `json:"bare"`
	Locked   bool   `json:"locked"`
	IsMain   bool   `json:"isMain"`
	Color    string `json:"color"` // UI color tag (set by the server, not git)

	// Stats (relative to the main worktree's branch, and to the upstream).
	Added        int  `json:"added"`        // lines added (committed vs base + uncommitted)
	Deleted      int  `json:"deleted"`      // lines deleted
	Ahead        int  `json:"ahead"`        // commits ahead of the base branch
	Dirty        bool `json:"dirty"`        // uncommitted changes present
	AheadOrigin  int  `json:"aheadOrigin"`  // commits ahead of upstream (unpushed)
	BehindOrigin int  `json:"behindOrigin"` // commits behind upstream
	HasUpstream  bool `json:"hasUpstream"`  // an upstream tracking branch is set
}

// List returns the worktrees registered for the repo at repoDir.
func List(repoDir string) ([]Worktree, error) {
	out, err := git(repoDir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var trees []Worktree
	var cur Worktree
	flush := func() {
		if cur.Path != "" {
			cur.Name = filepath.Base(cur.Path)
			trees = append(trees, cur)
		}
		cur = Worktree{}
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "worktree "):
			cur.Path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "HEAD "):
			cur.Head = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch "):
			cur.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		case line == "detached":
			cur.Detached = true
		case line == "bare":
			cur.Bare = true
		case strings.HasPrefix(line, "locked"):
			cur.Locked = true
		}
	}
	flush()
	if len(trees) > 0 {
		trees[0].IsMain = true
	}
	annotate(trees)
	return trees, nil
}

// annotate fills in git stats for each worktree, tolerating any git failure
// (stats default to zero). Base for ahead/diff is the main worktree's branch.
func annotate(trees []Worktree) {
	if len(trees) == 0 {
		return
	}
	base := trees[0].Branch
	for i := range trees {
		wt := &trees[i]
		if wt.Bare {
			continue
		}
		// Uncommitted changes vs HEAD (working tree + index).
		wt.Dirty = len(strings.TrimSpace(string(out(wt.Path, "status", "--porcelain")))) > 0
		a, d := diffStat(wt.Path, "HEAD")
		// Committed changes vs the base branch (for non-main branches).
		if base != "" && !wt.Detached && wt.Branch != base {
			ca, cd := diffStat(wt.Path, base+"...HEAD")
			a, d = a+ca, d+cd
			wt.Ahead = count(wt.Path, base+"..HEAD")
		}
		wt.Added, wt.Deleted = a, d
		// Ahead/behind the upstream tracking branch, if any.
		if lr := strings.Fields(string(out(wt.Path, "rev-list", "--count", "--left-right", "@{upstream}...HEAD"))); len(lr) == 2 {
			wt.HasUpstream = true
			wt.BehindOrigin = atoi(lr[0])
			wt.AheadOrigin = atoi(lr[1])
		}
	}
}

// diffStat sums added/deleted lines from `git diff --numstat <rev>`.
func diffStat(dir, rev string) (added, deleted int) {
	sc := bufio.NewScanner(bytes.NewReader(out(dir, "diff", "--numstat", rev)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 2 {
			added += atoi(f[0]) // "-" (binary) parses to 0
			deleted += atoi(f[1])
		}
	}
	return added, deleted
}

func count(dir, rng string) int {
	return atoi(strings.TrimSpace(string(out(dir, "rev-list", "--count", rng))))
}

// out runs git and returns stdout, swallowing errors (stats are best-effort).
func out(dir string, args ...string) []byte {
	b, err := git(dir, args...)
	if err != nil {
		return nil
	}
	return b
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

// Add creates a new worktree at path. When branch is non-empty and newBranch is
// true a new branch is created (off base, or HEAD when base is empty);
// otherwise the existing branch is checked out.
func Add(repoDir, path, branch, base string, newBranch bool) error {
	args := []string{"worktree", "add"}
	if newBranch && branch != "" {
		args = append(args, "-b", branch, path)
		if base != "" {
			args = append(args, base)
		}
	} else if branch != "" {
		args = append(args, path, branch)
	} else {
		args = append(args, path)
	}
	if _, err := git(repoDir, args...); err != nil {
		return err
	}
	return nil
}

// Remove deletes a worktree from disk and prunes its administrative files.
func Remove(repoDir, path string, force bool) error {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, path)
	_, err := git(repoDir, args...)
	return err
}

// IsRepo reports whether dir is inside a git working tree.
func IsRepo(dir string) bool {
	_, err := git(dir, "rev-parse", "--is-inside-work-tree")
	return err == nil
}

func git(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.Bytes(), nil
}
