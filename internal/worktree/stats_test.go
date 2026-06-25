package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWorktreeStats checks the rail stats: changed-file count, and commits
// ahead/behind the base branch since divergence.
func TestWorktreeStats(t *testing.T) {
	repo := t.TempDir()
	g := func(args ...string) {
		t.Helper()
		if _, err := git(repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	wr := func(name, body string) {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	g("init", "-q", "-b", "main")
	g("config", "user.email", "t@t")
	g("config", "user.name", "t")
	wr("a.txt", "one\n")
	g("add", "-A")
	g("commit", "-qm", "base")
	// main advances after the branch forks (so the branch is "behind").
	wr("a.txt", "one\ntwo\n")
	g("commit", "-qam", "main moves")

	// A feature branch forked from the first commit: 2 commits ahead, 1 behind.
	g("worktree", "add", "-q", "-b", "feat", filepath.Join(repo, ".wt-feat"), "HEAD~1")
	feat := filepath.Join(repo, ".wt-feat")
	gf := func(args ...string) {
		if _, err := git(feat, args...); err != nil {
			t.Fatalf("git(feat) %v: %v", args, err)
		}
	}
	os.WriteFile(filepath.Join(feat, "b.txt"), []byte("b\n"), 0o644)
	gf("add", "-A")
	gf("commit", "-qm", "feat 1")
	os.WriteFile(filepath.Join(feat, "c.txt"), []byte("c\n"), 0o644)
	gf("add", "-A")
	gf("commit", "-qm", "feat 2")
	// 3 uncommitted files in the feature worktree.
	os.WriteFile(filepath.Join(feat, "d.txt"), []byte("d\n"), 0o644)
	os.WriteFile(filepath.Join(feat, "e.txt"), []byte("e\n"), 0o644)
	os.WriteFile(filepath.Join(feat, "b.txt"), []byte("b changed\n"), 0o644)

	trees, err := List(repo)
	if err != nil {
		t.Fatal(err)
	}
	var ft *Worktree
	for i := range trees {
		if trees[i].Branch == "feat" {
			ft = &trees[i]
		}
	}
	if ft == nil {
		t.Fatal("feat worktree not found")
	}
	if ft.Ahead != 2 {
		t.Errorf("ahead = %d, want 2", ft.Ahead)
	}
	if ft.Behind != 1 {
		t.Errorf("behind = %d, want 1", ft.Behind)
	}
	if ft.DirtyFiles != 3 {
		t.Errorf("dirtyFiles = %d, want 3 (d.txt, e.txt, modified b.txt)", ft.DirtyFiles)
	}
	if !ft.Dirty {
		t.Error("dirty should be true")
	}
}
