package server

import "testing"

func TestPortFromAddr(t *testing.T) {
	cases := map[string]int{
		"0.0.0.0:7000":     7000,
		"127.0.0.1:5432":   5432,
		"[::]:8081":        8081,
		"*:443":            443,
		"garbage":          0,
		"1.2.3.4:notaport": 0,
	}
	for in, want := range cases {
		if got := portFromAddr(in); got != want {
			t.Errorf("portFromAddr(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestProcFromSS(t *testing.T) {
	line := `LISTEN 0 4096 0.0.0.0:7000 0.0.0.0:* users:(("slopbox",pid=42,fd=7))`
	name, pid := procFromSS(line)
	if name != "slopbox" || pid != 42 {
		t.Errorf("procFromSS = (%q, %d), want (slopbox, 42)", name, pid)
	}
	if n, p := procFromSS("LISTEN 0 128 *:22 *:*"); n != "" || p != 0 {
		t.Errorf("procFromSS(no proc) = (%q, %d), want empty", n, p)
	}
}
