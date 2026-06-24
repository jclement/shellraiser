package session

import (
	"testing"
	"time"
)

func TestJournalTailAndFilter(t *testing.T) {
	j := newJournal(t.TempDir())
	code := 0
	j.write(JournalEntry{TS: time.Now(), Event: "start", ID: "a", Kind: KindClaude, Cwd: "/w/one", Argv: []string{"claude"}})
	j.write(JournalEntry{TS: time.Now(), Event: "start", ID: "b", Kind: KindShell, Cwd: "/w/two"})
	j.write(JournalEntry{TS: time.Now(), Event: "exit", ID: "a", Cwd: "/w/one", Exit: &code})

	all := j.tail(100, "")
	if len(all) != 3 {
		t.Fatalf("tail all = %d, want 3", len(all))
	}
	if all[0].ID != "a" || all[0].Event != "start" || len(all[0].Argv) != 1 {
		t.Fatalf("first entry wrong: %+v", all[0])
	}

	one := j.tail(100, "/w/one")
	if len(one) != 2 {
		t.Fatalf("tail for /w/one = %d, want 2 (start+exit)", len(one))
	}
	if one[1].Event != "exit" || one[1].Exit == nil || *one[1].Exit != 0 {
		t.Fatalf("exit entry wrong: %+v", one[1])
	}

	if last := j.tail(1, ""); len(last) != 1 || last[0].ID != "a" || last[0].Event != "exit" {
		t.Fatalf("tail(1) should be the most recent (a/exit): %+v", last)
	}
}
