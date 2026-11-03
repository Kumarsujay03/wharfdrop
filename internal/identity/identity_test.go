package identity

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"testing"
)

func fakeFP(seed string) string {
	h := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(h[:])
}

func nonce(b byte) []byte { return bytes.Repeat([]byte{b}, NonceSize) }

func TestSASDependsOnEveryInput(t *testing.T) {
	a, b, na, nb := fakeFP("a"), fakeFP("b"), nonce(1), nonce(2)
	code := SAS(a, b, na, nb)
	if !regexp.MustCompile(`^\d{6}$`).MatchString(code) {
		t.Fatalf("SAS %q is not six digits", code)
	}
	for name, other := range map[string]string{
		"roles swapped":   SAS(b, a, nb, na),
		"other key":       SAS(a, fakeFP("c"), na, nb),
		"other nonce":     SAS(a, b, na, nonce(3)),
		"other own nonce": SAS(a, b, nonce(3), nb),
	} {
		if other == code {
			t.Fatalf("%s gives the same code", name)
		}
	}
}

func TestCommitBindsKeysAndNonce(t *testing.T) {
	resp, init, n := fakeFP("resp"), fakeFP("init"), nonce(7)
	c := Commit(resp, init, n)
	if !VerifyCommit(c, resp, init, n) {
		t.Fatal("honest reveal rejected")
	}
	for name, ok := range map[string]bool{
		"other nonce":         VerifyCommit(c, resp, init, nonce(8)),
		"other committer key": VerifyCommit(c, fakeFP("mallory"), init, n),
		"other receiver key":  VerifyCommit(c, resp, fakeFP("mallory"), n),
		"short nonce":         VerifyCommit(c, resp, init, n[:16]),
	} {
		if ok {
			t.Fatalf("commitment accepted with %s", name)
		}
	}
}

// sasV1 is the old code, kept here to show the attack it allowed.
func sasV1(fpA, fpB string) string {
	lo, hi := fpA, fpB
	if lo > hi {
		lo, hi = hi, lo
	}
	h := sha256.Sum256([]byte("wharfdrop-sas-v1|" + lo + "|" + hi))
	n := (uint32(h[0])<<24 | uint32(h[1])<<16 | uint32(h[2])<<8 | uint32(h[3])) % 1000000
	return fmt.Sprintf("%06d", n)
}

// grind tries up to limit candidates and returns the first one that makes
// code(candidate) equal target.
func grind(limit int, target string, code func(i int) string) (int, bool) {
	for i := 0; i < limit; i++ {
		if code(i) == target {
			return i, true
		}
	}
	return 0, false
}

// TestGrindingAttack: a man-in-the-middle between alice and bob knows both
// fingerprints (they are announced on the LAN). With v1 it picks any key for
// bob's side and grinds keys for alice's side until the codes agree, in about
// a million hashes. With v2 the same search finds matching values, but every
// one of them breaks the commitment that was sent before the honest nonce, so
// the victim aborts.
func TestGrindingAttack(t *testing.T) {
	if testing.Short() {
		t.Skip("grinds ~10^6 hashes")
	}
	alice, bob := fakeFP("alice"), fakeFP("bob")
	const limit = 20_000_000

	// v1: the codes can be forged.
	target := sasV1(fakeFP("mallory-to-bob"), bob)
	i, ok := grind(limit, target, func(i int) string { return sasV1(alice, fakeFP(fmt.Sprint("m", i))) })
	if !ok || sasV1(alice, fakeFP(fmt.Sprint("m", i))) != target {
		t.Fatal("expected the v1 grinding attack to succeed (it is the bug being fixed)")
	}
	t.Logf("v1: forged a matching code after %d keys", i+1)

	// v2, mallory as initiator towards bob: bob commits first, mallory must
	// send her nonce before seeing bob's, and bob answers one nonce per request.
	mToBob, bobNonce := fakeFP("mallory-to-bob"), nonce(0xb0)
	bobCommit := Commit(bob, mToBob, bobNonce)
	mNonce := nonce(0x33) // chosen without knowing bobNonce
	codeBob := SAS(mToBob, bob, mNonce, bobNonce)

	// v2, mallory as responder towards alice: she must commit to her key and
	// nonce before alice sends hers. Afterwards she grinds for a nonce that
	// gives alice the same code as bob.
	mToAlice, aliceNonce := fakeFP("mallory-to-alice"), nonce(0xa1)
	mCommit := Commit(mToAlice, alice, nonce(0x44))
	cand := func(i int) []byte {
		h := sha256.Sum256([]byte(fmt.Sprint("n", i)))
		return h[:]
	}
	i, ok = grind(limit, codeBob, func(i int) string { return SAS(alice, mToAlice, aliceNonce, cand(i)) })
	if !ok {
		t.Fatal("grinding should find a nonce that matches if nothing checked the commitment")
	}
	if VerifyCommit(mCommit, mToAlice, alice, cand(i)) {
		t.Fatal("alice accepted a ground nonce that does not match the commitment")
	}
	// Grinding mallory's key as well does not help: the commitment binds it.
	if VerifyCommit(mCommit, fakeFP(fmt.Sprint("m", i)), alice, cand(i)) {
		t.Fatal("alice accepted a ground key")
	}
	// The only reveal alice accepts gives a code mallory could not choose.
	if !VerifyCommit(mCommit, mToAlice, alice, nonce(0x44)) {
		t.Fatal("honest reveal rejected")
	}
	if SAS(alice, mToAlice, aliceNonce, nonce(0x44)) == codeBob {
		t.Fatal("fixed test vectors collide; pick other nonces")
	}
	// And bob's commitment pins his nonce the same way.
	if VerifyCommit(bobCommit, bob, mToBob, cand(i)) {
		t.Fatal("bob's commitment accepted another nonce")
	}
}

func TestLoadIsStable(t *testing.T) {
	dir := t.TempDir()
	first, err := Load(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	second, err := Load(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint != second.Fingerprint {
		t.Fatal("identity changed between loads")
	}
	if len(first.Fingerprint) != 64 {
		t.Fatalf("fingerprint length %d, want 64", len(first.Fingerprint))
	}
}
