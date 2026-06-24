package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiffAndCommit(t *testing.T) {
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q")
	mustGit(t, dir, "config", "user.email", "t@t")
	mustGit(t, dir, "config", "user.name", "t")
	write(t, dir, "a.txt", "one\n")
	mustGit(t, dir, "add", "-A")
	mustGit(t, dir, "commit", "-qm", "init")

	// An uncommitted change shows in the diff, including a brand-new file.
	write(t, dir, "a.txt", "one\ntwo\n")
	write(t, dir, "b.txt", "new file\n")
	diff, err := Diff(dir)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(diff, "+two") {
		t.Errorf("diff missing the added line:\n%s", diff)
	}
	if !strings.Contains(diff, "b.txt") {
		t.Errorf("diff missing the new untracked file:\n%s", diff)
	}

	// Commit stages everything; the diff is empty afterward.
	hash, err := Commit(dir, "agent step")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if len(hash) < 6 {
		t.Errorf("bad short hash %q", hash)
	}
	after, err := Diff(dir)
	if err != nil {
		t.Fatalf("Diff after commit: %v", err)
	}
	if strings.TrimSpace(after) != "" {
		t.Errorf("expected clean tree after commit, got:\n%s", after)
	}
}

// TestCommitFallbackIdentity confirms a commit works with no configured identity.
func TestCommitFallbackIdentity(t *testing.T) {
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q")
	// deliberately no user.email/name set
	write(t, dir, "x.txt", "hi\n")
	if _, err := Commit(dir, "first"); err != nil {
		t.Fatalf("Commit without identity should succeed via fallback: %v", err)
	}
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if _, err := git(dir, args...); err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
