package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSafeJoinStaysInsideRoot(t *testing.T) {
	root := t.TempDir()
	cases := map[string]bool{ // path -> must stay inside root
		"":              true,
		"a/b.txt":       true,
		"../etc/passwd": true, // cleaned to etc/passwd under root
		"a/../../x":     true,
		`..\..\windows`: true,
		"/abs/path":     true,
	}
	for in := range cases {
		got, err := safeJoin(root, in)
		if err != nil {
			continue // rejecting is also fine
		}
		rel, _ := filepath.Rel(root, got)
		if rel == ".." || len(rel) > 2 && rel[:3] == ".."+string(filepath.Separator) {
			t.Errorf("safeJoin(%q) escaped root: %s", in, got)
		}
	}
}

func TestUniquePath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "clip.mp4")
	if uniquePath(p) != p {
		t.Fatal("free name should be kept")
	}
	os.WriteFile(p, nil, 0o644)
	if got := uniquePath(p); got != filepath.Join(dir, "clip (1).mp4") {
		t.Fatalf("got %s", got)
	}
}
