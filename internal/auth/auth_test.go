package auth

import "testing"

func TestTempPasswordUntilSet(t *testing.T) {
	m := New("", func(string) error { return nil }, false)
	if !m.MustSetPassword() {
		t.Fatal("should require a password initially")
	}
	if m.TempPassword() == "" {
		t.Fatal("a one-time password should be minted")
	}
	if !m.checkPassword(m.TempPassword()) {
		t.Fatal("temp password should authenticate")
	}
	if m.checkPassword("wrong") {
		t.Fatal("wrong password must not authenticate")
	}
}

func TestSetPasswordClearsTemp(t *testing.T) {
	var saved string
	m := New("", func(h string) error { saved = h; return nil }, false)
	if err := m.SetPassword("hunter2"); err != nil {
		t.Fatal(err)
	}
	if m.MustSetPassword() {
		t.Fatal("password is set now")
	}
	if m.TempPassword() != "" {
		t.Fatal("temp password should be cleared")
	}
	if saved == "" {
		t.Fatal("hash should have been persisted")
	}
	if !m.checkPassword("hunter2") {
		t.Fatal("new password should authenticate")
	}
	if m.checkPassword("hunter3") {
		t.Fatal("wrong password must fail")
	}
}

func TestShortPasswordRejected(t *testing.T) {
	m := New("", func(string) error { return nil }, false)
	if err := m.SetPassword("abc"); err == nil {
		t.Fatal("short password should be rejected")
	}
}

func TestNoAuthAlwaysAuthenticated(t *testing.T) {
	m := New("", func(string) error { return nil }, true)
	if m.Enabled() {
		t.Fatal("auth should be disabled")
	}
}
