package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func startNode(t *testing.T, name string) (*Daemon, string) {
	t.Helper()
	dir := t.TempDir()
	peer, ui := freePort(t), freePort(t)
	cfg, _ := json.Marshal(map[string]any{"name": name, "peer_port": peer, "ui_port": ui,
		"incoming_dir": filepath.Join(dir, "incoming")})
	os.WriteFile(filepath.Join(dir, "config.json"), cfg, 0o600)
	d, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	addr := fmt.Sprintf("127.0.0.1:%d", peer)
	for i := 0; i < 50; i++ { // wait for the listener
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	return d, addr
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestPairSendShareDownload runs two nodes in-process and exercises the main flows.
func TestPairSendShareDownload(t *testing.T) {
	a, _ := startNode(t, "alpha")
	b, bAddr := startNode(t, "beta")

	// Unpaired devices are refused.
	if _, err := a.peer(b.ID.Fingerprint); err == nil {
		t.Fatal("expected 'not paired' before pairing")
	}

	// Pairing: both sides must compute the same code and both must confirm.
	pr, err := a.StartPairing(bAddr)
	if err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	in := b.pendingIn[pr.ID]
	b.mu.Unlock()
	if in == nil || in.Code != pr.Code {
		t.Fatalf("codes differ or request missing: %+v vs %s", in, pr.Code)
	}
	if err := b.RespondIncoming(pr.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := a.ConfirmOutgoing(pr.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "pairing", func() bool { _, ok := a.Cfg.Trusted(b.ID.Fingerprint); return ok })
	if _, ok := b.Cfg.Trusted(a.ID.Fingerprint); !ok {
		t.Fatal("beta does not trust alpha")
	}

	// Send a file and a folder from alpha to beta.
	src := t.TempDir()
	payload := make([]byte, 3<<20)
	rand.Read(payload)
	os.WriteFile(filepath.Join(src, "blob.bin"), payload, 0o644)
	os.MkdirAll(filepath.Join(src, "folder", "deep"), 0o755)
	os.WriteFile(filepath.Join(src, "folder", "deep", "n.txt"), []byte("hi"), 0o644)

	pc, err := a.peer(b.ID.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := pc.SendPath(ctx, filepath.Join(src, "blob.bin")); err != nil {
		t.Fatal(err)
	}
	if _, err := pc.SendPath(ctx, filepath.Join(src, "folder")); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(b.Cfg.IncomingDir, "blob.bin"))
	if !bytes.Equal(got, payload) {
		t.Fatal("sent file differs")
	}
	if b, _ := os.ReadFile(filepath.Join(b.Cfg.IncomingDir, "folder", "deep", "n.txt")); string(b) != "hi" {
		t.Fatal("folder structure not kept")
	}
	// Sending again must not overwrite.
	pc.SendPath(ctx, filepath.Join(src, "blob.bin"))
	if _, err := os.Stat(filepath.Join(b.Cfg.IncomingDir, "blob (1).bin")); err != nil {
		t.Fatal("second copy should be renamed, not overwritten")
	}

	// Beta shares a folder; alpha lists and downloads, and cannot escape it.
	shareDir := t.TempDir()
	os.WriteFile(filepath.Join(shareDir, "movie.mkv"), payload, 0o644)
	os.WriteFile(filepath.Join(filepath.Dir(shareDir), "secret.txt"), []byte("no"), 0o644)
	b.Cfg.Update(func(c *configT) {
		c.Shares = append(c.Shares, &shareT{Name: "vids", Path: shareDir, Peers: []string{"*"}, ReadOnly: true})
	})

	toB, _ := a.peer(b.ID.Fingerprint)
	ents, err := toB.Ls(ctx, "vids", "")
	if err != nil || len(ents) != 1 || ents[0].Name != "movie.mkv" {
		t.Fatalf("ls: %v %+v", err, ents)
	}
	dest := t.TempDir()
	if _, err := toB.Download(ctx, "vids", "movie.mkv", dest); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "movie.mkv")); !bytes.Equal(got, payload) {
		t.Fatal("downloaded file differs")
	}
	if _, err := toB.Download(ctx, "vids", "../secret.txt", dest); err == nil {
		t.Fatal("path traversal must fail")
	}
	if _, err := toB.Ls(ctx, "nope", ""); err == nil {
		t.Fatal("unknown share must fail")
	}

	// After unpairing, beta refuses alpha.
	b.Unpair(a.ID.Fingerprint)
	if _, err := toB.Ls(ctx, "vids", ""); err == nil {
		t.Fatal("unpaired peer must be refused")
	}
}
