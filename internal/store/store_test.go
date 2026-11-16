package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestMigrationsAreIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.sqlite3")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()
}

func TestAdminPasswordAndSession(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	password := "Test-Admin-Pass-2026"
	created, err := st.EnsureAdmin(ctx, "admin", password)
	if err != nil || !created {
		t.Fatalf("EnsureAdmin created=%v err=%v", created, err)
	}
	// A second call must not overwrite the existing administrator.
	created, err = st.EnsureAdmin(ctx, "admin", "another-long-password")
	if err != nil || created {
		t.Fatalf("second EnsureAdmin created=%v err=%v", created, err)
	}
	if _, err := st.Authenticate(ctx, "admin", "wrong-password"); err == nil {
		t.Fatal("wrong password was accepted")
	}
	admin, err := st.Authenticate(ctx, "admin", password)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	sess, token, err := st.CreateSession(ctx, admin.ID, "127.0.0.1", "test", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if sess.CSRF == "" || token == "" {
		t.Fatal("session secrets were empty")
	}
	got, err := st.SessionByToken(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if got.Username != "admin" {
		t.Fatalf("username = %q", got.Username)
	}
	if _, err := st.SessionByToken(ctx, token+"x"); err == nil {
		t.Fatal("tampered token was accepted")
	}
	if err := st.DeleteSession(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SessionByToken(ctx, token); err == nil {
		t.Fatal("deleted session still resolves")
	}
}

func TestPasswordHashing(t *testing.T) {
	password := "Test-Hash-Pass-2026"
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, password) {
		t.Fatal("correct password did not verify")
	}
	if VerifyPassword(hash, password+"x") {
		t.Fatal("wrong password verified")
	}
	other, _ := HashPassword(password)
	if hash == other {
		t.Fatal("hashes should use unique salts")
	}
}

func TestDeviceKeyRotation(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	device, key, err := st.CreateDevice(ctx, "lan-1", "", "test")
	if err != nil {
		t.Fatal(err)
	}
	if key == "" || device.KeyPrefix == "" {
		t.Fatal("device key was not issued")
	}
	if TokenDigest(key) == "" {
		t.Fatal("empty digest")
	}
	newKey, err := st.RotateDeviceKey(ctx, device.ID)
	if err != nil {
		t.Fatal(err)
	}
	if newKey == key {
		t.Fatal("rotated key equals old key")
	}
	hash, err := st.DeviceKeyHash(ctx, device.ID)
	if err != nil {
		t.Fatal(err)
	}
	if hash == TokenDigest(key) {
		t.Fatal("old key still matches after rotation")
	}
	if hash != TokenDigest(newKey) {
		t.Fatal("new key does not match stored hash")
	}
}
