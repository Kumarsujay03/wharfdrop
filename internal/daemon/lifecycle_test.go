package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func phoneOnlineIn(d *Daemon, id string) (found, online bool) {
	for _, p := range d.phones.State().Phones {
		if p.ID == id {
			return true, p.Online
		}
	}
	return false, false
}

// Disconnecting a phone on the computer: the phone's next check gets an
// authenticated "closed" notice and the session no longer works.
func TestPhoneDisconnectedOnComputer(t *testing.T) {
	d, pc := startPhoneLink(t)
	pc.rpc(map[string]any{"op": "hello", "name": "Pixel"})
	if _, on := phoneOnlineIn(d, pc.sid); !on {
		t.Fatal("phone should be online after hello")
	}
	rec := httptest.NewRecorder()
	d.lPhoneForget(rec, httptest.NewRequest("POST", "/api/phone/forget", strings.NewReader(`{"id":"`+pc.sid+`"}`)))
	if found, _ := phoneOnlineIn(d, pc.sid); found {
		t.Fatal("forgotten phone still listed on the computer")
	}
	sealed, _ := pc.rpcRaw(map[string]any{"op": "state"})
	resp, out := pc.post(sealed)
	if resp.StatusCode != 200 || out["closed"] != "forgotten" {
		t.Fatalf("want closed notice, got %d %v", resp.StatusCode, out)
	}
	// a stranger with a wrong key gets no notice
	stranger := *pc
	stranger.key = make([]byte, 32)
	sealed, _ = stranger.rpcRaw(map[string]any{"op": "state"})
	if resp, _ := stranger.post(sealed); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("stranger got %d", resp.StatusCode)
	}
}

// Disconnecting on the phone removes it from the computer immediately.
func TestPhoneDisconnectsItself(t *testing.T) {
	d, pc := startPhoneLink(t)
	pc.rpc(map[string]any{"op": "hello", "name": "Pixel", "remember": true})
	pc.rpc(map[string]any{"op": "disconnect"})
	if found, _ := phoneOnlineIn(d, pc.sid); found {
		t.Fatal("phone still listed after it disconnected")
	}
	sealed, _ := pc.rpcRaw(map[string]any{"op": "state"})
	if resp, _ := pc.post(sealed); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked key still works: %d", resp.StatusCode)
	}
}

// Closing the phone page shows it offline at once, but a reload still works.
func TestPhoneByeShowsOffline(t *testing.T) {
	d, pc := startPhoneLink(t)
	pc.rpc(map[string]any{"op": "hello", "name": "Pixel"})
	sealed, _ := pc.rpcRaw(map[string]any{"op": "bye"})
	resp, err := http.Post(pc.base+"/api/rpc?s="+pc.sid, "application/octet-stream", strings.NewReader(string(sealed)))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("bye: %v %v", err, resp)
	}
	resp.Body.Close()
	if found, on := phoneOnlineIn(d, pc.sid); !found || on {
		t.Fatalf("after bye: found=%v online=%v", found, on)
	}
	pc.rpc(map[string]any{"op": "state"}) // reload reconnects
	if _, on := phoneOnlineIn(d, pc.sid); !on {
		t.Fatal("phone should be online again after reload")
	}
}

// Stopping Wharfdrop tells a connected phone why before the port closes.
func TestStopNotifiesPhone(t *testing.T) {
	d, pc := startPhoneLink(t)
	pc.rpc(map[string]any{"op": "hello", "name": "Pixel"})
	done := make(chan time.Duration)
	go func() {
		t0 := time.Now()
		d.phones.StopWith("stopped", "Wharfdrop on computer was stopped.")
		done <- time.Since(t0)
	}()
	var got map[string]any
	for i := 0; i < 20 && got["closed"] == nil; i++ {
		time.Sleep(100 * time.Millisecond)
		sealed, _ := pc.rpcRaw(map[string]any{"op": "state"})
		_, got = pc.post(sealed)
	}
	if got["closed"] != "stopped" || !strings.Contains(got["message"].(string), "stopped") {
		t.Fatalf("phone got %v", got)
	}
	if took := <-done; took > 2*time.Second {
		t.Fatalf("stop waited %v after the phone was told", took)
	}
	if d.phones.running() {
		t.Fatal("phone port still open")
	}
}

// Stopping with no phone online does not wait.
func TestStopWithoutPhonesIsQuick(t *testing.T) {
	d, _ := startPhoneLink(t)
	t0 := time.Now()
	d.phones.StopWith("stopped", "x")
	if time.Since(t0) > 500*time.Millisecond {
		t.Fatalf("stop took %v", time.Since(t0))
	}
}

func pairNodes(t *testing.T) (a, b *Daemon) {
	t.Helper()
	a, _ = startNode(t, "alpha")
	b, bAddr := startNode(t, "beta")
	pr, err := a.StartPairing(bAddr)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.RespondIncoming(pr.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := a.ConfirmOutgoing(pr.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "pairing", func() bool {
		_, ok1 := b.Cfg.Trusted(a.ID.Fingerprint)
		_, ok2 := a.Cfg.Trusted(b.ID.Fingerprint)
		return ok1 && ok2
	})
	return a, b
}

// halfSent starts a 64 MB upload from a to b and sends the first 8 MB.
func halfSent(t *testing.T, a, b *Daemon) (pc *peerClient, id, part string) {
	t.Helper()
	pc, err := a.peer(b.ID.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id, err = pc.startUpload(ctx, "movie.mkv", 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := pc.putChunk(ctx, id, 0, bytes.NewReader(make([]byte, chunkSize)), chunkSize); err != nil {
		t.Fatal(err)
	}
	part = filepath.Join(b.Cfg.IncomingDir, "movie.mkv.part")
	if _, err := os.Stat(part); err != nil {
		t.Fatal("no partial file on the receiver:", err)
	}
	return pc, id, part
}

func gone(p string) bool { _, err := os.Stat(p); return os.IsNotExist(err) }

// The sender stopping mid-transfer deletes the receiver's partial file.
func TestSenderStopCleansReceiver(t *testing.T) {
	a, b := pairNodes(t)
	_, id, part := halfSent(t, a, b)
	activeUps.Store(id, mustPeer(t, a, b))
	a.abortOutgoing()
	waitFor(t, "partial file removed", func() bool { return gone(part) })
	if _, err := os.Stat(filepath.Join(b.Cfg.IncomingDir, "movie.mkv")); err == nil {
		t.Fatal("an unfinished file was kept")
	}
}

// A goodbye from the sender (e.g. it was killed by an update restart path or
// lost its abort) also deletes what it was sending.
func TestGoodbyeCleansReceiver(t *testing.T) {
	a, b := pairNodes(t)
	_, _, part := halfSent(t, a, b)
	a.mu.Lock()
	a.reachable[b.ID.Fingerprint] = true
	a.mu.Unlock()
	a.sayGoodbye()
	waitFor(t, "partial file removed", func() bool { return gone(part) })
}

// The receiver stopping deletes its own partial files.
func TestReceiverStopCleansUp(t *testing.T) {
	a, b := pairNodes(t)
	_, _, part := halfSent(t, a, b)
	b.requestStop("test")
	waitFor(t, "partial file removed", func() bool { return gone(part) })
	if !gone(filepath.Join(b.Dir, "partial-files.json")) {
		t.Fatal("journal not cleared")
	}
}

// After a crash, the next start deletes what the journal lists, and only
// ".part" files.
func TestJournalCleansAfterCrash(t *testing.T) {
	dir := t.TempDir()
	part := filepath.Join(dir, "big.iso.part")
	keep := filepath.Join(dir, "notes.txt")
	os.WriteFile(part, []byte("x"), 0o644)
	os.WriteFile(keep, []byte("x"), 0o644)
	b, _ := json.Marshal([]string{part, keep})
	os.WriteFile(filepath.Join(dir, "partial-files.json"), b, 0o600)
	j := newPartJournal(dir)
	if n := j.cleanAll(); n != 1 || !gone(part) || gone(keep) {
		t.Fatalf("cleaned %d, part gone %v, other file gone %v", n, gone(part), gone(keep))
	}
}

func mustPeer(t *testing.T, a, b *Daemon) *peerClient {
	pc, err := a.peer(b.ID.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	return pc
}

// Speed is measured over seconds, so a short pause between bursts does not
// show as a collapse.
func TestRateIsSteady(t *testing.T) {
	tr := &Transfer{Started: time.Now().Add(-6 * time.Second), Size: 1 << 30}
	base := tr.Started
	// 10 MB/s for 6 s, but delivered in 1-second bursts with nothing between
	for i := 0; i <= 24; i++ {
		at := base.Add(time.Duration(i) * 250 * time.Millisecond)
		n := int64(i/4) * 10 << 20
		tr.samples = append(tr.samples, sample{at, n})
	}
	tr.cur = 6 * 10 << 20
	r := tr.rateOver(base.Add(6*time.Second), tr.cur, speedWindow) / (1 << 20)
	if r < 8 || r > 12 {
		t.Fatalf("shown speed %.1f MB/s, want about 10", r)
	}
}

// A paired computer that stops says goodbye and shows offline at once.
func TestPeerGoodbye(t *testing.T) {
	a, _ := startNode(t, "alpha")
	b, bAddr := startNode(t, "beta")
	pr, err := a.StartPairing(bAddr)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.RespondIncoming(pr.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := a.ConfirmOutgoing(pr.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "pairing", func() bool {
		_, ok1 := b.Cfg.Trusted(a.ID.Fingerprint)
		_, ok2 := a.Cfg.Trusted(b.ID.Fingerprint)
		return ok1 && ok2
	})
	a.mu.Lock()
	a.reachable[b.ID.Fingerprint] = true
	a.mu.Unlock()
	b.mu.Lock()
	b.reachable[a.ID.Fingerprint] = true
	b.mu.Unlock()
	a.sayGoodbye()
	b.mu.Lock()
	on := b.reachable[a.ID.Fingerprint]
	b.mu.Unlock()
	if on {
		t.Fatal("beta still shows alpha online after goodbye")
	}
}

func TestUITracker(t *testing.T) {
	var u uiTracker
	u.poll("a")
	if u.allClosed(0) {
		t.Fatal("closed without a beacon")
	}
	u.close("a")
	u.poll("b") // the page reloaded
	if u.allClosed(0) {
		t.Fatal("reload treated as close")
	}
	u.close("b")
	if !u.allClosed(0) {
		t.Fatal("last window closed but not detected")
	}
	// another window that polled recently, but before the close, keeps it open
	var v uiTracker
	v.poll("x")
	v.poll("y")
	v.close("x")
	if v.allClosed(0) {
		t.Fatal("stopped while another window is open")
	}
	if v.allClosed(time.Hour) {
		t.Fatal("grace ignored")
	}
}
