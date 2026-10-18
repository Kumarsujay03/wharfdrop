package daemon

import (
	"net/http"
	"net/http/httptest"
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
