package store

import (
	"context"
	"errors"
	"path/filepath"
	"server-aibot/internal/vault"
	"testing"
)

func TestDeviceKeyRetentionAndRotation(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "device.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	v, _ := vault.New(make([]byte, 32))
	d, key, err := s.CreateDevice(ctx, "legacy", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if d.KeyRetrievable {
		t.Fatal("legacy key marked retrievable")
	}
	if _, err = s.RevealDeviceKey(ctx, v, d.ID); !errors.Is(err, ErrDeviceKeyUnavailable) {
		t.Fatal(err)
	}
	if err = s.RetainDeviceKey(ctx, v, d.ID, "wrong"); !errors.Is(err, ErrDeviceKeyMismatch) {
		t.Fatal(err)
	}
	if _, err = s.RevealDeviceKey(ctx, v, d.ID); !errors.Is(err, ErrDeviceKeyUnavailable) {
		t.Fatal("failed retention saved data")
	}
	if err = s.SetDeviceOnline(ctx, d.ID, "test", "test", "test"); err != nil {
		t.Fatal(err)
	}
	before, _ := s.GetDevice(ctx, d.ID)
	if err = s.RetainDeviceKey(ctx, v, d.ID, key); err != nil {
		t.Fatal(err)
	}
	after, _ := s.GetDevice(ctx, d.ID)
	if !after.KeyRetrievable || after.Status != "online" || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatal("retention altered device state")
	}
	plain, err := s.RevealDeviceKey(ctx, v, d.ID)
	if err != nil || string(plain) != key {
		t.Fatal("retained key mismatch", err)
	}
	next, err := s.RotateDeviceKey(ctx, d.ID, v)
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := s.DeviceKeyHash(ctx, d.ID)
	if hash != tokenHash(next) || hash == tokenHash(key) {
		t.Fatal("rotation hash mismatch")
	}
	if err = s.RetainDeviceKey(ctx, v, d.ID, key); !errors.Is(err, ErrDeviceKeyMismatch) {
		t.Fatal("old key retained")
	}
	plain, err = s.RevealDeviceKey(ctx, v, d.ID)
	if err != nil || string(plain) != next {
		t.Fatal("rotated key mismatch", err)
	}
	// Explicit nil vault must fail without mutating authentication or ciphertext.
	if _, err = s.RotateDeviceKey(ctx, d.ID, nil); err == nil {
		t.Fatal("nil vault accepted")
	}
	plain, err = s.RevealDeviceKey(ctx, v, d.ID)
	if err != nil || string(plain) != next {
		t.Fatal("failed rotation mutated row")
	}
	other, _, err := s.CreateDevice(ctx, "other", "", "", v)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE devices SET key_ct=(SELECT key_ct FROM devices WHERE id=?),key_id=(SELECT key_id FROM devices WHERE id=?) WHERE id=?`, d.ID, d.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RevealDeviceKey(ctx, v, other.ID); err == nil {
		t.Fatal("AAD allowed cross-device ciphertext")
	}
	// Race retention with rotation. Regardless of ordering, the final ciphertext
	// must authenticate as the new key; retaining a stale key may only fail.
	for i := 0; i < 20; i++ {
		oldKey := next
		start := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			<-start
			done <- s.RetainDeviceKey(ctx, v, d.ID, oldKey)
		}()
		close(start)
		next, err = s.RotateDeviceKey(ctx, d.ID, v)
		if err != nil {
			t.Fatal(err)
		}
		if e := <-done; e != nil && !errors.Is(e, ErrDeviceKeyMismatch) {
			t.Fatal(e)
		}
		plain, err = s.RevealDeviceKey(ctx, v, d.ID)
		if err != nil || string(plain) != next {
			t.Fatal("concurrent retention overwrote rotated key", err)
		}
	}
	// Hash-only compatibility rotation must clear any previously retained key.
	if _, err = s.RotateDeviceKey(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RevealDeviceKey(ctx, v, d.ID); !errors.Is(err, ErrDeviceKeyUnavailable) {
		t.Fatal("stale ciphertext survived rotation")
	}
}
