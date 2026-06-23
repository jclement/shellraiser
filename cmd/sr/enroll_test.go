package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// TestEnrollFlow drives the HTTP enrollment: possession-proven start, owner
// approve, and the long-poll status that hands the device its host key + caps —
// and confirms the device key lands on authorized_devices.
func TestEnrollFlow(t *testing.T) {
	configDir = t.TempDir()
	hostCfg = hostConfig{DeviceLinkAddr: ":7722"}
	t.Cleanup(func() { hostCfg = hostConfig{} })

	c := co(t)
	c.enroll = newEnrollStore()
	c.devlink = newDeviceLinkServer(c, testSigner(t))

	dev := testSigner(t)
	pubLine := authorizedLine(dev)
	sig, err := dev.Sign(rand.Reader, []byte(pubLine))
	if err != nil {
		t.Fatal(err)
	}

	// start
	startBody, _ := json.Marshal(map[string]string{
		"pubkey": pubLine, "name": "jeff-mac",
		"sig_format": sig.Format, "sig_blob": base64.StdEncoding.EncodeToString(sig.Blob),
	})
	rec := httptest.NewRecorder()
	c.handleEnrollStart(rec, httptest.NewRequest("POST", "/enroll/start", bytes.NewReader(startBody)))
	if rec.Code != 200 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	var start struct{ Code, Fingerprint string }
	_ = json.Unmarshal(rec.Body.Bytes(), &start)
	if start.Code == "" || !strings.HasPrefix(start.Fingerprint, "SHA256:") {
		t.Fatalf("bad start response: %s", rec.Body)
	}

	// approve (owner)
	appBody, _ := json.Marshal(map[string]any{
		"code": start.Code, "name": "jeff-mac", "approve": true,
		"capabilities": []string{capBindPort, capSSHAgent, "bogus"},
	})
	rec = httptest.NewRecorder()
	c.handleEnrollApprove(rec, httptest.NewRequest("POST", "/enroll/approve", bytes.NewReader(appBody)))
	if rec.Code != 200 {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body)
	}
	if len(hostCfg.AuthorizedDevices) != 1 || hostCfg.AuthorizedDevices[0].Key != strings.TrimSpace(pubLine) {
		t.Fatalf("device not added to allowlist: %+v", hostCfg.AuthorizedDevices)
	}

	// status (device long-poll → returns immediately, already decided)
	rec = httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/enroll/status?code="+start.Code, nil)
	req.Host = "backend.tail.ts.net:443"
	c.handleEnrollStatus(rec, req)
	var st struct {
		Status       string   `json:"status"`
		Capabilities []string `json:"capabilities"`
		HostKey      string   `json:"host_key"`
		SSHAddr      string   `json:"ssh_addr"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if st.Status != "approved" {
		t.Fatalf("status: %s", rec.Body)
	}
	if got := strings.Join(st.Capabilities, ","); got != capBindPort+","+capSSHAgent {
		t.Fatalf("caps filtered wrong: %q", got) // "bogus" dropped
	}
	if !strings.HasPrefix(st.HostKey, "SHA256:") {
		t.Fatalf("no host key fp: %q", st.HostKey)
	}
	if st.SSHAddr != "backend.tail.ts.net:7722" {
		t.Fatalf("ssh_addr: %q", st.SSHAddr)
	}
}

// TestEnrollRejectsBadPossession confirms a signature that doesn't match the
// submitted key is refused (substitution attack).
func TestEnrollRejectsBadPossession(t *testing.T) {
	c := co(t)
	c.enroll = newEnrollStore()

	dev := testSigner(t)
	other := testSigner(t)
	pubLine := authorizedLine(dev)
	// sign with the WRONG key
	sig, _ := other.Sign(rand.Reader, []byte(pubLine))
	body, _ := json.Marshal(map[string]string{
		"pubkey": pubLine, "name": "x",
		"sig_format": sig.Format, "sig_blob": base64.StdEncoding.EncodeToString(sig.Blob),
	})
	rec := httptest.NewRecorder()
	c.handleEnrollStart(rec, httptest.NewRequest("POST", "/enroll/start", bytes.NewReader(body)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

var _ = ssh.MarshalAuthorizedKey
