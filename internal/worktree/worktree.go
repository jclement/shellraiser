// Package worktree wraps the handful of git-worktree operations shellraiser needs.
package worktree

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Worktree is one entry from `git worktree list`, annotated with git stats.
type Worktree struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	Branch      string `json:"branch"`
	Head        string `json:"head"`
	Detached    bool   `json:"detached"`
	Bare        bool   `json:"bare"`
	Locked      bool   `json:"locked"`
	IsMain      bool   `json:"isMain"`
	Color       string `json:"color"`       // UI color tag (set by the server, not git)
	DisplayName string `json:"displayName"` // custom label, independent of branch
	Order       int    `json:"order"`       // manual sort order (set by the server, not git)

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
// accessibleDir reports whether path exists and is a directory we can reach.
func accessibleDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

func List(repoDir string) ([]Worktree, error) {
	out, err := git(repoDir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var trees []Worktree
	var cur Worktree
	flush := func() {
		// Only surface worktrees whose path is actually reachable here. git may
		// list worktrees created elsewhere (e.g. on the host, outside this
		// worker's mounts); we can't open a shell/editor in those, so hide them.
		if cur.Path != "" && accessibleDir(cur.Path) {
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

// Branches lists local branch names (newest-committed first) for the new-worktree
// picker.
func Branches(repoDir string) []string {
	out, err := git(repoDir, "for-each-ref", "--sort=-committerdate", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return nil
	}
	var names []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if b := strings.TrimSpace(sc.Text()); b != "" {
			names = append(names, b)
		}
	}
	return names
}

// RemoteName returns a human repo name from the origin remote URL (e.g.
// "git@host:org/shellraiser.git" -> "shellraiser"), or "" if there's no remote. Used so
// the header shows the project, not the mount path basename ("work").
func RemoteName(repoDir string) string {
	out, err := git(repoDir, "remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	u := strings.TrimSpace(string(out))
	u = strings.TrimSuffix(u, ".git")
	u = strings.TrimRight(u, "/")
	if i := strings.LastIndexAny(u, "/:"); i >= 0 {
		u = u[i+1:]
	}
	return u
}

// Repair re-links any git worktrees found as folders under worktreesDir so they
// show up in List even if their admin files went stale (e.g. the repo path
// moved between container runs). Best-effort; errors are ignored.
func Repair(repoDir, worktreesDir string) {
	entries, err := os.ReadDir(worktreesDir)
	if err != nil {
		return
	}
	paths := []string{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(worktreesDir, e.Name())
		// A worktree checkout has a `.git` file (not dir) pointing at the repo.
		if fi, err := os.Stat(filepath.Join(p, ".git")); err == nil && !fi.IsDir() {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return
	}
	_, _ = git(repoDir, append([]string{"worktree", "repair"}, paths...)...)
}

// Diff returns the unified diff for a worktree. With staged=false it's the full
// working-tree-vs-HEAD diff (what the agent changed but hasn't committed),
// including untracked files (via --intent-to-add semantics emulated by listing
// them). Renames are detected; binary files are summarized by git.
func Diff(dir string) (string, error) {
	// Stage intent for untracked files so they appear in the diff, then diff the
	// index+worktree against HEAD without actually committing anything.
	_, _ = git(dir, "add", "-N", ".")
	out, err := git(dir, "diff", "HEAD")
	if err != nil {
		// New repo with no commits yet: diff against the empty tree.
		if out2, err2 := git(dir, "diff"); err2 == nil {
			return string(out2), nil
		}
		return "", err
	}
	return string(out), nil
}

// Commit stages everything in the worktree and commits with message. Returns the
// new commit's short hash. Ensures a fallback identity so commits never fail for
// lack of user.email in a fresh container.
func Commit(dir, message string) (string, error) {
	ensureIdentity(dir)
	if _, err := git(dir, "add", "-A"); err != nil {
		return "", err
	}
	if _, err := git(dir, "commit", "-m", message); err != nil {
		return "", err
	}
	hash, err := git(dir, "rev-parse", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(hash)), nil
}

// ensureIdentity sets a local fallback git identity if none is configured, so a
// commit in a fresh worker (no mounted ~/.gitconfig) still succeeds.
func ensureIdentity(dir string) {
	if _, err := git(dir, "config", "user.email"); err == nil {
		return // already set (globally or locally, e.g. mounted ~/.gitconfig)
	}
	_, _ = git(dir, "config", "user.email", "agent@shellraiser.local")
	_, _ = git(dir, "config", "user.name", "shellraiser agent")
}

func git(dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
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
