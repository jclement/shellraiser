package main

import "testing"

func TestParsePublished(t *testing.T) {
	cases := []struct {
		ports, cport, want string
	}{
		{"127.0.0.1:32835->7000/tcp, 127.0.0.1:32931->22/tcp", "7000", "32835"},
		{"127.0.0.1:32835->7000/tcp, 127.0.0.1:32931->22/tcp", "22", "32931"},
		{"[::]:49160->22/tcp", "22", "49160"},
		{"0.0.0.0:5432->5432/tcp", "5432", "5432"},
		{"127.0.0.1:32835->7000/tcp", "22", ""}, // not published
		{"", "7000", ""},                        // stopped container, no ports
		{"127.0.0.1:1->70/tcp", "7", ""},        // prefix must be exact (70 != 7)
	}
	for _, c := range cases {
		if got := parsePublished(c.ports, c.cport); got != c.want {
			t.Errorf("parsePublished(%q, %q) = %q, want %q", c.ports, c.cport, got, c.want)
		}
	}
}
