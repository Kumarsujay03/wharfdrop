package identity

import (
	"regexp"
	"testing"
)

func TestSASIsSymmetricAndSixDigits(t *testing.T) {
	a, b := "aa11", "bb22"
	if SAS(a, b) != SAS(b, a) {
		t.Fatal("SAS must not depend on argument order")
	}
	if !regexp.MustCompile(`^\d{6}$`).MatchString(SAS(a, b)) {
		t.Fatalf("SAS %q is not six digits", SAS(a, b))
	}
	if SAS(a, b) == SAS(a, "cc33") {
		t.Fatal("different peers should give different codes")
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
