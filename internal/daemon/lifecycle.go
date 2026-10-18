package daemon

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/Kumarsujay03/wharfdrop/internal/config"
)

// lifeCtx is cancelled when Wharfdrop starts stopping. Sends and downloads
// that should outlive the request that started them use it.
func (d *Daemon) lifeCtx() context.Context {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.life == nil {
		return context.Background()
	}
	return d.life
}

// ---------- peer online status ----------

// markOK records that a paired computer just answered a request. During a
// big transfer the link is saturated and the 3-second status check can time
// out, so recent transfer traffic also counts as "online".
func (d *Daemon) markOK(fp string) {
	d.mu.Lock()
	d.lastOK[fp] = time.Now()
	d.reachable[fp] = true
	d.mu.Unlock()
}

// recentlyOK reports whether fp answered in the last 15 s. Callers hold d.mu.
func (d *Daemon) recentlyOK(fp string) bool {
	return time.Since(d.lastOK[fp]) < 15*time.Second
}

// ---------- goodbye to paired computers ----------

// hBye (peer API): a paired computer is stopping; show it offline now.
func (d *Daemon) hBye(w http.ResponseWriter, r *http.Request, p *config.Peer) {
	d.Disc.Forget(p.Fingerprint)
	d.dropInboundFrom(p.Fingerprint, errors.New(p.Name+" stopped; the unfinished file was deleted"))
	d.mu.Lock()
	d.reachable[p.Fingerprint] = false
	delete(d.lastOK, p.Fingerprint)
	d.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// sayGoodbye tells online paired computers that we are stopping.
func (d *Daemon) sayGoodbye() {
	var fps []string
	d.Cfg.View(func(c *config.Config) {
		for fp := range c.Peers {
			fps = append(fps, fp)
		}
	})
	var wg sync.WaitGroup
	for _, fp := range fps {
		online := false
		if _, ok := d.Disc.Lookup(fp); ok {
			online = true
		}
		d.mu.Lock()
		online = online || d.reachable[fp]
		d.mu.Unlock()
		if !online {
			continue
		}
		pc, err := d.peer(fp)
		if err != nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "POST", d.peerURL(pc.addr, "/bye", nil), nil)
			if resp, err := pc.c.Do(req); err == nil {
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()
}

// ---------- stop when the Wharfdrop window is closed ----------

// uiTracker follows open Wharfdrop windows. Each page load polls with its own
// random tab id and sends a "closed" beacon when it goes away.
type uiTracker struct {
	mu     sync.Mutex
	tabs   map[string]time.Time // tab id -> last poll
	closed time.Time            // last "closed" beacon; zero when none pending
	reason string
}

func (u *uiTracker) poll(tab string) {
	if tab == "" || len(tab) > 64 {
		return
	}
	u.mu.Lock()
	if u.tabs == nil {
		u.tabs = map[string]time.Time{}
	}
	u.tabs[tab] = time.Now()
	u.mu.Unlock()
}

func (u *uiTracker) close(tab string) {
	u.mu.Lock()
	delete(u.tabs, tab)
	u.closed = time.Now()
	u.mu.Unlock()
}

// allClosed reports whether a window was closed at least grace ago and no
// other window has checked in since. A hidden tab may be throttled to about
// one poll a minute, so one seen in the last 90 s still counts as open.
func (u *uiTracker) allClosed(grace time.Duration) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed.IsZero() {
		return false
	}
	now := time.Now()
	for id, at := range u.tabs {
		if at.After(u.closed) {
			u.closed = time.Time{} // reloaded, or another window is open
			return false
		}
		if now.Sub(at) > 90*time.Second {
			delete(u.tabs, id)
		}
	}
	return now.Sub(u.closed) >= grace && len(u.tabs) == 0
}

func (d *Daemon) stopReason() string {
	d.ui.mu.Lock()
	defer d.ui.mu.Unlock()
	if d.ui.reason != "" {
		return d.ui.reason
	}
	return "requested from the web UI or `wharfdrop stop`"
}

func (d *Daemon) requestStop(reason string) {
	d.ui.mu.Lock()
	d.ui.reason = reason
	d.ui.mu.Unlock()
	select {
	case d.shutdown <- struct{}{}:
	default:
	}
}

func (d *Daemon) stopOnClose() bool {
	on := true
	d.Cfg.View(func(c *config.Config) {
		if c.StopOnClose != nil {
			on = *c.StopOnClose
		}
	})
	return on
}

func (d *Daemon) lUIClosed(w http.ResponseWriter, r *http.Request) {
	d.ui.close(r.URL.Query().Get("tab"))
	w.WriteHeader(http.StatusNoContent)
}

// uiWatch stops Wharfdrop when its last window was closed. It needs a
// "closed" beacon, so the CLI and `wharfdrop start` without a window are never
// affected, and it waits for running transfers to finish.
func (d *Daemon) uiWatch(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if !d.ui.allClosed(uiCloseGrace) || !d.stopOnClose() || d.busy() {
			continue
		}
		log.Printf("the last Wharfdrop window was closed")
		d.requestStop("the Wharfdrop window was closed")
		return
	}
}

var uiCloseGrace = 10 * time.Second
