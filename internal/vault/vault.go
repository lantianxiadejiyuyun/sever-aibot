// Package vault implements envelope encryption for credential secrets.
//
// Each record is sealed with an AEAD cipher using a 32-byte master key. The
// stored blob is nonce(24) || ciphertext || tag. The additional authenticated
// data binds a ciphertext to its credential ID and kind, so a blob cannot be
// copied to another record without decryption failing.
package vault

import (
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	// KeySize is the required master-key length in bytes.
	KeySize = 32
	// NonceSize is the nonce length used by the extended-nonce AEAD.
	NonceSize = chacha20poly1305.NonceSizeX
	// KeyIDSize is the number of SHA-256 bytes exposed as the key fingerprint.
	KeyIDSize = 8
)

const aadSeparator = 0x00

// Vault seals and opens credential secrets with one master key.
type Vault struct {
	aead  cipher.AEAD
	keyID string
}

// New builds a vault. key must be exactly KeySize bytes.
func New(key []byte) (*Vault, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("vault: master key must be %d bytes, got %d", KeySize, len(key))
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("vault: initialise cipher: %w", err)
	}
	sum := sha256.Sum256(key)
	return &Vault{
		aead:  aead,
		keyID: hex.EncodeToString(sum[:KeyIDSize]),
	}, nil
}

// KeyID returns the stable fingerprint of the master key.
func (v *Vault) KeyID() string { return v.keyID }

// Seal encrypts plaintext for the given credential ID and kind. It returns the
// stored blob (nonce || ciphertext+tag) and the key fingerprint.
func (v *Vault) Seal(id, kind string, plaintext []byte) ([]byte, string, error) {
	if len(plaintext) == 0 {
		return nil, "", errors.New("vault: plaintext is empty")
	}
	nonce := make([]byte, NonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, "", fmt.Errorf("vault: generate nonce: %w", err)
	}
	out := make([]byte, NonceSize, NonceSize+len(plaintext)+v.aead.Overhead())
	copy(out, nonce)
	out = v.aead.Seal(out, nonce, plaintext, associatedData(id, kind))
	return out, v.keyID, nil
}

// Open decrypts a stored blob. keyID must match this vault's fingerprint,
// otherwise the error says which key is required so rotation is diagnosable.
func (v *Vault) Open(id, kind string, sealed []byte, keyID string) ([]byte, error) {
	if subtle.ConstantTimeCompare([]byte(keyID), []byte(v.keyID)) != 1 {
		return nil, fmt.Errorf("vault: key mismatch: ciphertext needs key %q, vault holds key %q", keyID, v.keyID)
	}
	if len(sealed) < NonceSize+v.aead.Overhead() {
		return nil, errors.New("vault: sealed data is too short")
	}
	nonce := sealed[:NonceSize]
	ciphertext := sealed[NonceSize:]
	plaintext, err := v.aead.Open(nil, nonce, ciphertext, associatedData(id, kind))
	if err != nil {
		return nil, errors.New("vault: decrypt failed: ciphertext is tampered or was sealed for another record")
	}
	return plaintext, nil
}

func associatedData(id, kind string) []byte {
	aad := make([]byte, 0, len(id)+1+len(kind))
	aad = append(aad, id...)
	aad = append(aad, aadSeparator)
	aad = append(aad, kind...)
	return aad
}
