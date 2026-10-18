package daemon

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"golang.org/x/crypto/chacha20poly1305"
)

// Vectors produced by the vendored @noble/ciphers xchacha20poly1305 (the code
// the phone page runs). Go must open them, so both sides interoperate.
func TestNobleVectorsOpenInGo(t *testing.T) {
	raw, err := os.ReadFile("testdata/noble_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Key     string
		Vectors []struct {
			AAD     string `json:"aad"`
			MsgLen  int    `json:"msg_len"`
			MsgHead string `json:"msg_head"`
			Sealed  string `json:"sealed"`
		}
	}
	json.Unmarshal(raw, &v)
	key, _ := hex.DecodeString(v.Key)
	a, _ := chacha20poly1305.NewX(key)
	for _, tv := range v.Vectors {
		sealed, _ := hex.DecodeString(tv.Sealed)
		plain, err := open(a, tv.AAD, sealed)
		if err != nil {
			t.Fatalf("%s: Go could not open noble ciphertext: %v", tv.AAD, err)
		}
		if len(plain) != tv.MsgLen || !strings.HasPrefix(string(plain), tv.MsgHead) {
			t.Fatalf("%s: wrong plaintext", tv.AAD)
		}
		if _, err := open(a, tv.AAD+"x", sealed); err == nil {
			t.Fatalf("%s: wrong associated data must be rejected", tv.AAD)
		}
		sealed[len(sealed)-1] ^= 1
		if _, err := open(a, tv.AAD, sealed); err == nil {
			t.Fatalf("%s: tampered ciphertext must be rejected", tv.AAD)
		}
	}
}
