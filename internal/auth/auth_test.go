package auth

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRPIDStripsPort(t *testing.T) {
	cases := map[string]string{
		"localhost:7000":  "localhost",
		"box.example.com": "box.example.com",
		"1.2.3.4:443":     "1.2.3.4",
	}
	for host, want := range cases {
		r := httptest.NewRequest("GET", "http://"+host+"/", nil)
		r.Host = host
		if got := rpID(r); got != want {
			t.Errorf("rpID(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestBootstrapCodeFormat(t *testing.T) {
	c := randomCode()
	if len(c) != 14 { // 12 chars + 2 dashes
		t.Errorf("code %q len %d, want 14", c, len(c))
	}
	if strings.Count(c, "-") != 2 {
		t.Errorf("code %q should have 2 dashes", c)
	}
}

func TestSameOriginUserScopedByRP(t *testing.T) {
	m := &Manager{data: store{UserID: []byte("u")}}
	m.data.Creds = []storedCred{{RPID: "localhost"}, {RPID: "box.example.com"}, {RPID: "localhost"}}
	if n := m.credCount("localhost"); n != 2 {
		t.Errorf("credCount(localhost) = %d, want 2", n)
	}
	if got := len(m.user("box.example.com").creds); got != 1 {
		t.Errorf("user(box.example.com).creds = %d, want 1", got)
	}
}
