package vault

import (
	"bytes"
	"crypto/rand"
	"testing"
)

// randomBytes returns disposable test material; no real secret is stored here.
func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return b
}

func newVault(t *testing.T) (*Vault, []byte) {
	t.Helper()
	key := randomBytes(t, KeySize)
	v, err := New(key)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return v, key
}

func TestRoundTrip(t *testing.T) {
	v, _ := newVault(t)
	plaintext := randomBytes(t, 48)
	sealed, keyID, err := v.Seal("cred_1", "ssh_password", plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if keyID != v.KeyID() {
		t.Fatalf("keyID = %q, want %q", keyID, v.KeyID())
	}
	if len(sealed) != NonceSize+len(plaintext)+16 {
		t.Fatalf("sealed length = %d, want %d", len(sealed), NonceSize+len(plaintext)+16)
	}
	got, err := v.Open("cred_1", "ssh_password", sealed, keyID)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatal("round-trip plaintext mismatch")
	}
}

func TestSealRejectsEmptyPlaintext(t *testing.T) {
	v, _ := newVault(t)
	if _, _, err := v.Seal("cred_1", "generic", nil); err == nil {
		t.Fatal("expected empty plaintext to be rejected")
	}
	if _, _, err := v.Seal("cred_1", "generic", []byte{}); err == nil {
		t.Fatal("expected empty plaintext to be rejected")
	}
}

func TestWrongKeyFails(t *testing.T) {
	v1, _ := newVault(t)
	v2, _ := newVault(t)
	sealed, keyID, err := v1.Seal("cred_1", "generic", randomBytes(t, 16))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := v2.Open("cred_1", "generic", sealed, keyID); err == nil {
		t.Fatal("expected key mismatch error")
	}
	// Even if the caller lies about the fingerprint, decryption must fail.
	if _, err := v2.Open("cred_1", "generic", sealed, v2.KeyID()); err == nil {
		t.Fatal("expected decryption failure with a different key")
	}
}

func TestTamperedCiphertextFails(t *testing.T) {
	v, _ := newVault(t)
	sealed, keyID, err := v.Seal("cred_1", "generic", randomBytes(t, 16))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	sealed[len(sealed)-1] ^= 0xff
	if _, err := v.Open("cred_1", "generic", sealed, keyID); err == nil {
		t.Fatal("expected tampered ciphertext to fail authentication")
	}

	sealed2, keyID2, err := v.Seal("cred_1", "generic", randomBytes(t, 16))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	sealed2[0] ^= 0xff // corrupt the nonce
	if _, err := v.Open("cred_1", "generic", sealed2, keyID2); err == nil {
		t.Fatal("expected tampered nonce to fail authentication")
	}
}

func TestMovedCiphertextFails(t *testing.T) {
	v, _ := newVault(t)
	plaintext := randomBytes(t, 16)
	sealed, keyID, err := v.Seal("cred_1", "ssh_password", plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := v.Open("cred_2", "ssh_password", sealed, keyID); err == nil {
		t.Fatal("expected ciphertext moved to another credential ID to fail")
	}
	if _, err := v.Open("cred_1", "api_token", sealed, keyID); err == nil {
		t.Fatal("expected ciphertext moved to another kind to fail")
	}
}

func TestUniqueNonces(t *testing.T) {
	v, _ := newVault(t)
	plaintext := []byte("same plaintext every time")
	seen := make(map[string]bool)
	for i := 0; i < 32; i++ {
		sealed, _, err := v.Seal("cred_1", "generic", plaintext)
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
		nonce := string(sealed[:NonceSize])
		if seen[nonce] {
			t.Fatal("nonce reused across Seal calls")
		}
		seen[nonce] = true
	}
}

func TestKeyIDStability(t *testing.T) {
	key := randomBytes(t, KeySize)
	v1, err := New(key)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	v2, err := New(key)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if v1.KeyID() != v2.KeyID() {
		t.Fatalf("key ID not stable: %q vs %q", v1.KeyID(), v2.KeyID())
	}
	if len(v1.KeyID()) != KeyIDSize*2 {
		t.Fatalf("key ID length = %d, want %d", len(v1.KeyID()), KeyIDSize*2)
	}
	other, _ := newVault(t)
	if other.KeyID() == v1.KeyID() {
		t.Fatal("different keys produced the same key ID")
	}
}

func TestNewRejectsBadKeySize(t *testing.T) {
	for _, n := range []int{0, 16, 31, 33} {
		if _, err := New(make([]byte, n)); err == nil {
			t.Fatalf("expected key size %d to be rejected", n)
		}
	}
}
