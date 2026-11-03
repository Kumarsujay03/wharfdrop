// Package identity manages the device's long-lived key pair and self-signed
// TLS certificate. The SHA-256 fingerprint of the certificate is the device's
// identity; peers pin it after pairing.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

type Identity struct {
	Cert        tls.Certificate
	Fingerprint string // hex sha256 of the DER certificate
}

// Dir returns the per-user config directory (created if missing).
// WHARFDROP_DIR overrides it (useful for running several nodes on one machine).
func Dir() (string, error) {
	if d := os.Getenv("WHARFDROP_DIR"); d != "" {
		return d, os.MkdirAll(d, 0o700)
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	d := filepath.Join(base, "wharfdrop")
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	return d, nil
}

// FingerprintOf returns the hex SHA-256 of a DER certificate.
func FingerprintOf(c *x509.Certificate) string {
	s := sha256.Sum256(c.Raw)
	return hex.EncodeToString(s[:])
}

// Short returns a human-friendly abbreviation of a fingerprint.
func Short(fp string) string {
	if len(fp) < 12 {
		return fp
	}
	return fp[:4] + "-" + fp[4:8] + "-" + fp[8:12]
}

// Load loads the identity from dir, generating a new one on first run.
func Load(dir, name string) (*Identity, error) {
	certPath := filepath.Join(dir, "device.crt")
	keyPath := filepath.Join(dir, "device.key")
	if _, err := os.Stat(keyPath); errors.Is(err, os.ErrNotExist) {
		if err := generate(certPath, keyPath, name); err != nil {
			return nil, fmt.Errorf("generate identity: %w", err)
		}
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, err
	}
	cert.Leaf = leaf
	return &Identity{Cert: cert, Fingerprint: FingerprintOf(leaf)}, nil
}

func generate(certPath, keyPath, name string) error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: name, Organization: []string{"Wharfdrop"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(20, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return err
	}
	return os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}

// SASVersion names the pairing protocol. Both devices must speak the same one.
const SASVersion = "wharfdrop-sas-v2"

// NonceSize is the length in bytes of each side's pairing nonce.
const NonceSize = 32

// NewNonce returns a fresh random pairing nonce.
func NewNonce() ([]byte, error) {
	n := make([]byte, NonceSize)
	_, err := rand.Read(n)
	return n, err
}

// Commit returns the hash the responding device sends before revealing its
// nonce. It binds the committer's fingerprint, the fingerprint of the device
// it is talking to, and the nonce, so none of them can be changed after the
// other side has sent its own nonce.
func Commit(ownFP, peerFP string, nonce []byte) string {
	h := sha256.Sum256([]byte(SASVersion + "-commit|" + ownFP + "|" + peerFP + "|" + hex.EncodeToString(nonce)))
	return hex.EncodeToString(h[:])
}

// VerifyCommit checks a commitment received from peerFP against the nonce it
// later revealed. ownFP is the fingerprint of the device checking.
func VerifyCommit(commit, peerFP, ownFP string, nonce []byte) bool {
	want := Commit(peerFP, ownFP, nonce)
	return len(nonce) == NonceSize && subtle.ConstantTimeCompare([]byte(commit), []byte(want)) == 1
}

// SAS derives the 6-digit short authentication string both devices display
// during pairing, from the two certificate fingerprints each side saw in the
// TLS handshake and the two nonces. The exchange runs in the order of
// Bluetooth's numeric comparison:
//
//  1. the responder sends Commit(respFP, initFP, respNonce);
//  2. the initiator sends its nonce in the clear;
//  3. the responder reveals its nonce and the initiator checks the commitment.
//
// Each side's nonce is fixed before it sees the other's, so a
// man-in-the-middle cannot grind keys or nonces until the codes on the two
// screens agree: every attempt gives an unpredictable code, a match only one
// time in a million, and shows a pairing prompt to a user.
func SAS(initFP, respFP string, initNonce, respNonce []byte) string {
	h := sha256.Sum256([]byte(SASVersion + "|" + initFP + "|" + respFP + "|" +
		hex.EncodeToString(initNonce) + "|" + hex.EncodeToString(respNonce)))
	n := (uint32(h[0])<<24 | uint32(h[1])<<16 | uint32(h[2])<<8 | uint32(h[3])) % 1000000
	return fmt.Sprintf("%06d", n)
}
