package daemon

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Kumarsujay03/wharfdrop/internal/identity"
)

// pairStep posts one raw pairing message as node from and returns the status and body.
func pairStep(t *testing.T, from *Daemon, addr, p string, body any) (int, []byte) {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := from.client("", nil).Post(from.peerURL(addr, p, nil), "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// TestPairingNonceIsOneShotAndBound plays mallory against bob over the wire:
// bob commits before seeing her nonce, reveals once, and only to her key.
func TestPairingNonceIsOneShotAndBound(t *testing.T) {
	b, bAddr := startNode(t, "bob")
	m, _ := startNode(t, "mallory")
	e, _ := startNode(t, "eve")

	st, raw := pairStep(t, m, bAddr, "/pair/request", map[string]any{"id": "r1", "name": "m", "sas": identity.SASVersion})
	var c struct{ SAS, Commit string }
	if st != 200 || json.Unmarshal(raw, &c) != nil || c.SAS != identity.SASVersion || c.Commit == "" {
		t.Fatalf("request: %d %s", st, raw)
	}
	b.mu.Lock()
	in := *b.pendingIn["r1"]
	b.mu.Unlock()
	if in.Status != "committed" || in.Code != "" {
		t.Fatalf("request must stay hidden until the nonce arrives: %+v", in)
	}
	if err := b.RespondIncoming("r1", true); err == nil {
		t.Fatal("a request without a code must not be accepted")
	}

	mine := bytes.Repeat([]byte{9}, identity.NonceSize)
	nonceMsg := map[string]any{"id": "r1", "nonce": hex.EncodeToString(mine)}
	if st, _ := pairStep(t, e, bAddr, "/pair/nonce", nonceMsg); st != http.StatusNotFound {
		t.Fatalf("another device answered mallory's request: %d", st)
	}

	st, raw = pairStep(t, m, bAddr, "/pair/nonce", nonceMsg)
	var rv struct{ Nonce, Code string }
	if st != 200 || json.Unmarshal(raw, &rv) != nil {
		t.Fatalf("nonce: %d %s", st, raw)
	}
	theirs, _ := hex.DecodeString(rv.Nonce)
	if !identity.VerifyCommit(c.Commit, b.ID.Fingerprint, m.ID.Fingerprint, theirs) {
		t.Fatal("bob's reveal does not match his commitment")
	}
	if want := identity.SAS(m.ID.Fingerprint, b.ID.Fingerprint, mine, theirs); rv.Code != want {
		t.Fatalf("code %s, want %s", rv.Code, want)
	}
	b.mu.Lock()
	in = *b.pendingIn["r1"]
	b.mu.Unlock()
	if in.Status != "pending" || in.Code != rv.Code {
		t.Fatalf("bob shows %+v", in)
	}

	// Now that she knows bob's nonce, mallory would like to try other nonces.
	nonceMsg["nonce"] = hex.EncodeToString(bytes.Repeat([]byte{10}, identity.NonceSize))
	if st, _ := pairStep(t, m, bAddr, "/pair/nonce", nonceMsg); st != http.StatusNotFound {
		t.Fatalf("second nonce accepted: %d", st)
	}
}

func TestPairingRefusesV1Peers(t *testing.T) {
	b, bAddr := startNode(t, "new")
	old, _ := startNode(t, "old")

	// An old device asks a new one: refused with a reason.
	st, raw := pairStep(t, old, bAddr, "/pair/request", map[string]any{"id": "r1", "name": "old"})
	if st != http.StatusUpgradeRequired || !strings.Contains(string(raw), "newer Wharfdrop") {
		t.Fatalf("v1 request: %d %s", st, raw)
	}
	b.mu.Lock()
	n := len(b.pendingIn)
	b.mu.Unlock()
	if n != 0 {
		t.Fatal("v1 request was queued")
	}

	// A new device asks an old one, which answers with a v1 code: refused.
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"code": "123456", "name": "oldbox"})
	}))
	srv.StartTLS()
	defer srv.Close()
	_, err := b.StartPairing(srv.Listener.Addr().String())
	if err == nil || !strings.Contains(err.Error(), "older Wharfdrop") {
		t.Fatalf("pairing with a v1 device: %v", err)
	}
}
