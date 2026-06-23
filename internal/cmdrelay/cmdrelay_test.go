package cmdrelay

import (
	"bytes"
	"net"
	"strings"
	"testing"
)

// TestRoundTrip runs a real command (cat) through the relay: stdin in, stdout
// back, exit code propagated.
func TestRoundTrip(t *testing.T) {
	a, b := net.Pipe()
	go Serve(a, Exposer([]string{"cat"}))

	var out, errb bytes.Buffer
	code := Shim(b, "cat", nil, nil, strings.NewReader("hello relay"), &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d, stderr=%q", code, errb.String())
	}
	if out.String() != "hello relay" {
		t.Fatalf("stdout=%q want %q", out.String(), "hello relay")
	}
}

// TestUnexposedRejected confirms a command the device doesn't expose is refused.
func TestUnexposedRejected(t *testing.T) {
	a, b := net.Pipe()
	go Serve(a, Exposer([]string{"op"})) // cat NOT exposed

	var out, errb bytes.Buffer
	code := Shim(b, "cat", nil, nil, strings.NewReader(""), &out, &errb)
	if code != 126 {
		t.Fatalf("exit %d want 126", code)
	}
	if !strings.Contains(errb.String(), "not exposed") {
		t.Fatalf("stderr=%q", errb.String())
	}
}

func TestOpPolicy(t *testing.T) {
	allow := []string{"read op://vault/item/field", "item get foo", "document get x"}
	for _, a := range allow {
		if err := OpPolicy(strings.Fields(a)); err != nil {
			t.Errorf("OpPolicy(%q) = %v, want allow", a, err)
		}
	}
	deny := []string{"run -- rm -rf /", "inject -i t", "plugin init", "signin", "account list", "read op://x -- bad"}
	for _, d := range deny {
		if err := OpPolicy(strings.Fields(d)); err == nil {
			t.Errorf("OpPolicy(%q) = nil, want deny", d)
		}
	}
	if OpPolicy(nil) == nil {
		t.Error("empty argv should be denied")
	}
}

func TestExposerOpPolicyAttached(t *testing.T) {
	r := Exposer([]string{"op", "gh"})
	if _, pol, ok := r("op"); !ok || pol == nil {
		t.Error("op should resolve with a policy")
	}
	if _, pol, ok := r("gh"); !ok || pol != nil {
		t.Error("gh should resolve with no policy")
	}
	if _, _, ok := r("aws"); ok {
		t.Error("aws should not resolve")
	}
}
