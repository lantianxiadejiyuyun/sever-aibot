package tunnel

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strconv"
	"time"
)

// DirectCandidates are exchanged only inside the authenticated node tunnel.
// TCP candidates support LAN connectivity and explicit public port mappings;
// they do not implement automatic NAT hole punching.
type DirectCandidates struct {
	Addresses   []string `json:"addresses"`
	Fingerprint string   `json:"fingerprint"`
}

func ValidateDirectAddress(address string, allowLoopback bool) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("direct candidate must be IP:port")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return errors.New("invalid direct candidate port")
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || (ip.IsLoopback() && !allowLoopback) {
		return errors.New("direct candidate IP denied")
	}
	if ip.Equal(net.ParseIP("fd00:ec2::254")) {
		return errors.New("metadata endpoint denied")
	}
	if !ip.IsGlobalUnicast() && !ip.IsLoopback() {
		return errors.New("direct candidate is not unicast")
	}
	return nil
}
func DirectCertificate() (tls.Certificate, string, error) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, "", err
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "aibot-direct"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	cert, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}))
	digest := sha256.Sum256(der)
	return cert, hex.EncodeToString(digest[:]), err
}
func PinnedTLS(fingerprint string) *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true, VerifyConnection: func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) != 1 {
			return errors.New("unexpected direct certificate chain")
		}
		digest := sha256.Sum256(cs.PeerCertificates[0].Raw)
		if !hmac.Equal([]byte(hex.EncodeToString(digest[:])), []byte(fingerprint)) {
			return errors.New("direct certificate pin mismatch")
		}
		if time.Now().After(cs.PeerCertificates[0].NotAfter) {
			return errors.New("direct certificate expired")
		}
		return nil
	}}
}
func directMAC(key, forward string, revision int64, nonce []byte) []byte {
	mac := hmac.New(sha256.New, []byte(key))
	fmt.Fprintf(mac, "aibot-direct-v1\x00%s\x00%d\x00", forward, revision)
	mac.Write(nonce)
	return mac.Sum(nil)
}
func AcceptDirect(conn net.Conn, key, forward string, revision int64) error {
	if key == "" {
		return errors.New("direct credential missing")
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	if _, err := conn.Write(nonce[:]); err != nil {
		return err
	}
	var proof [32]byte
	if _, err := io.ReadFull(conn, proof[:]); err != nil {
		return err
	}
	if !hmac.Equal(proof[:], directMAC(key, forward, revision, nonce[:])) {
		return errors.New("direct credential rejected")
	}
	return nil
}
func ProveDirect(conn net.Conn, key, forward string, revision int64) error {
	if key == "" {
		return errors.New("direct credential missing")
	}
	var nonce [32]byte
	if _, err := io.ReadFull(conn, nonce[:]); err != nil {
		return err
	}
	_, err := conn.Write(directMAC(key, forward, revision, nonce[:]))
	return err
}
