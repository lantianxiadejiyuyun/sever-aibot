package store

import (
	"context"
	"path/filepath"
	"server-aibot/internal/vault"
	"testing"
)

func TestKeyRotationIsAtomicAndIncludesWebhooks(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "rotation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	old, _ := vault.New(make([]byte, 32))
	key := make([]byte, 32)
	key[0] = 1
	next, _ := vault.New(key)
	wrongKey := make([]byte, 32)
	wrongKey[0] = 2
	wrong, _ := vault.New(wrongKey)
	ctx := context.Background()
	device, deviceKey, err := s.CreateDevice(ctx, "encrypted-device", "", "", old)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.CreateDevice(ctx, "legacy-device", "", "")
	if err != nil {
		t.Fatal(err)
	}
	cred, err := s.CreateCredential(ctx, old, CredentialInput{Name: "fixture", Kind: "api_token", Secret: "rotation-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	channel, err := s.SaveNotificationChannel(ctx, old, NotificationChannel{Name: "fixture", Kind: "webhook", Enabled: false}, "https://example.com/test", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RotateCredentialKey(ctx, wrong, next); err == nil {
		t.Fatal("wrong old key accepted")
	}
	if _, err = s.RevealCredential(ctx, old, cred.ID); err != nil {
		t.Fatal("failed rotation altered credential")
	}
	badDevice, _, err := s.CreateDevice(ctx, "wrong-master", "", "", wrong)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RotateCredentialKey(ctx, old, next); err == nil {
		t.Fatal("undecryptable device accepted")
	}
	if _, err = s.RevealCredential(ctx, old, cred.ID); err != nil {
		t.Fatal("device failure did not roll back credentials")
	}
	if _, _, err = s.NotificationChannelSecret(ctx, old, channel.ID); err != nil {
		t.Fatal("device failure did not roll back webhook")
	}
	if plain, e := s.RevealDeviceKey(ctx, old, device.ID); e != nil || string(plain) != deviceKey {
		t.Fatal("device failure did not roll back device ciphertext")
	}
	if err = s.DeleteDevice(ctx, badDevice.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.EnsureAdmin(ctx, "rotation-admin", "unused-test-hash"); err != nil {
		t.Fatal(err)
	}
	var adminID string
	if err = s.db.QueryRowContext(ctx, `SELECT id FROM admins WHERE username=?`, "rotation-admin").Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	seed, seedKey, err := old.Seal(adminID, "admin_totp", []byte("rotation-seed"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO admin_security(admin_id,totp_seed_ct,totp_key_id,pending_seed_ct,pending_key_id) VALUES(?,?,?,?,?)`, adminID, seed, seedKey, []byte("corrupt"), seedKey); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RotateCredentialKey(ctx, old, next); err == nil {
		t.Fatal("corrupt pending seed accepted")
	}
	if _, err = s.RevealCredential(ctx, old, cred.ID); err != nil {
		t.Fatal("late failure did not roll back credential")
	}
	if plain, e := s.RevealDeviceKey(ctx, old, device.ID); e != nil || string(plain) != deviceKey {
		t.Fatal("late failure did not roll back device key", e)
	}
	if _, _, err = s.NotificationChannelSecret(ctx, old, channel.ID); err != nil {
		t.Fatal("late failure did not roll back webhook")
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE admin_security SET pending_seed_ct=? WHERE admin_id=?`, seed, adminID); err != nil {
		t.Fatal(err)
	}
	n, err := s.RotateCredentialKey(ctx, old, next)
	if err != nil || n != 5 {
		t.Fatalf("rotation count=%d err=%v", n, err)
	}
	if plain, e := s.RevealDeviceKey(ctx, next, device.ID); e != nil || string(plain) != deviceKey {
		t.Fatal("new master key cannot decrypt device key", e)
	}
	if _, e := s.RevealDeviceKey(ctx, old, device.ID); e == nil {
		t.Fatal("old master key still decrypts device key")
	}
	v, err := s.RevealCredential(ctx, next, cred.ID)
	if err != nil || string(v.Plaintext) != "rotation-fixture" {
		t.Fatal("new key cannot decrypt credential")
	}
	_, url, err := s.NotificationChannelSecret(ctx, next, channel.ID)
	if err != nil || url != "https://example.com/test" {
		t.Fatal("new key cannot decrypt webhook")
	}
	for _, columns := range [][2]string{{"totp_seed_ct", "totp_key_id"}, {"pending_seed_ct", "pending_key_id"}} {
		var ciphertext []byte
		var keyID string
		if err = s.db.QueryRowContext(ctx, `SELECT `+columns[0]+`,`+columns[1]+` FROM admin_security WHERE admin_id=?`, adminID).Scan(&ciphertext, &keyID); err != nil {
			t.Fatal(err)
		}
		plain, openErr := next.Open(adminID, "admin_totp", ciphertext, keyID)
		if openErr != nil || string(plain) != "rotation-seed" {
			t.Fatal("new key cannot decrypt authentication seed")
		}
		clear(plain)
		if _, openErr = old.Open(adminID, "admin_totp", ciphertext, keyID); openErr == nil {
			t.Fatal("old key still decrypts authentication seed")
		}
	}
	if _, err = s.RevealCredential(ctx, old, cred.ID); err == nil {
		t.Fatal("old key still decrypts")
	}
}
