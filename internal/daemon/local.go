package daemon

import (
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Kumarsujay03/wharfdrop/internal/config"
	"github.com/Kumarsujay03/wharfdrop/internal/discovery"
	"github.com/Kumarsujay03/wharfdrop/internal/identity"
)

//go:embed ui.html
var uiHTML []byte

// ---------- loopback API used by the CLI and the web UI ----------

func (d *Daemon) localMux() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(uiHTML)
	})
	api := http.NewServeMux()
	api.HandleFunc("GET /api/state", d.lState)
	api.HandleFunc("POST /api/pair", d.lPair)
	api.HandleFunc("POST /api/pair/confirm", d.lPairConfirm)
	api.HandleFunc("POST /api/pair/cancel", d.lPairCancel)
	api.HandleFunc("POST /api/pair/respond", d.lPairRespond)
	api.HandleFunc("POST /api/unpair", d.lUnpair)
	api.HandleFunc("POST /api/shares", d.lShareAdd)
	api.HandleFunc("DELETE /api/shares", d.lShareDel)
	api.HandleFunc("POST /api/settings", d.lSettings)
	api.HandleFunc("GET /api/peer/shares", d.lPeerShares)
	api.HandleFunc("GET /api/peer/ls", d.lPeerLs)
	api.HandleFunc("GET /api/peer/get", d.lPeerGet)
	api.HandleFunc("POST /api/peer/download", d.lPeerDownload)
	api.HandleFunc("POST /api/send", d.lSendUpload)
	api.HandleFunc("POST /api/send-path", d.lSendPath)
	api.HandleFunc("GET /api/avatar", d.lAvatarGet)
	api.HandleFunc("POST /api/avatar", d.lAvatarSet)
	api.HandleFunc("DELETE /api/avatar", d.lAvatarDelete)
	api.HandleFunc("GET /api/peer/avatar", d.lPeerAvatar)
	api.HandleFunc("POST /api/send/start", d.lSendStart)
	api.HandleFunc("PUT /api/send/chunk", d.lSendChunk)
	api.HandleFunc("POST /api/send/finish", d.lSendFinish)
	api.HandleFunc("GET /api/transfers/{id}", d.lTransfer)
	api.HandleFunc("GET /api/update", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, d.updateState()) })
	api.HandleFunc("POST /api/update/check", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, d.CheckUpdate(r.Context())) })
	api.HandleFunc("POST /api/update/install", d.lUpdateInstall)
	api.HandleFunc("POST /api/shutdown", d.lShutdown)
	api.HandleFunc("POST /api/open-folder", d.lOpenFolder)
	m.Handle("/api/", d.auth(api))
	return m
}

// auth requires the per-install token; it stops other local users/processes
// (and any web page you visit) from driving the daemon.
func (d *Daemon) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := r.Header.Get("X-Token")
		if tok == "" {
			tok = r.URL.Query().Get("t")
		}
		if subtle.ConstantTimeCompare([]byte(tok), []byte(d.Token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func fail(w http.ResponseWriter, err error, code int) {
	writeJSONStatus(w, code, map[string]string{"error": err.Error()})
}

func writeJSONStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func decode(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v)
}

type peerView struct {
	FP       string    `json:"fp"`
	Name     string    `json:"name"`
	Addr     string    `json:"addr"`
	Online   bool      `json:"online"`
	Trusted  bool      `json:"trusted"`
	LastSeen time.Time `json:"last_seen,omitempty"`
}

type State struct {
	Self struct {
		Name    string   `json:"name"`
		FP      string   `json:"fp"`
		Short   string   `json:"short"`
		Port    int      `json:"port"`
		Addrs   []string `json:"addrs"`
		Version string   `json:"version"`
		Avatar  int64    `json:"avatar"` // avatar version (0 = none)
	} `json:"self"`
	Incoming   string          `json:"incoming"`
	Peers      []peerView      `json:"peers"`
	Shares     []*config.Share `json:"shares"`
	PendingIn  []PairRequest   `json:"pending_in"`
	PendingOut []PairRequest   `json:"pending_out"`
	Transfers  []Transfer      `json:"transfers"`
	Update     UpdateState     `json:"update"`
}

func (d *Daemon) state() State {
	var s State
	s.Self.FP = d.ID.Fingerprint
	s.Self.Short = identity.Short(d.ID.Fingerprint)
	s.Self.Addrs = LocalAddrs()
	s.Self.Version = Version
	s.Self.Avatar = d.AvatarVersion()
	seen := map[string]discovery.Found{}
	for _, f := range d.Disc.Peers() {
		seen[f.FP] = f
	}
	d.Cfg.View(func(c *config.Config) {
		s.Self.Name = c.Name
		s.Self.Port = c.PeerPort
		s.Incoming = c.IncomingDir
		s.Shares = append([]*config.Share{}, c.Shares...)
		for fp, p := range c.Peers {
			pv := peerView{FP: fp, Name: p.Name, Addr: p.LastAddr, Trusted: true}
			if f, ok := seen[fp]; ok {
				pv.Online, pv.Addr, pv.Name, pv.LastSeen = true, f.Addr, f.Name, f.LastSeen
				delete(seen, fp)
			} else {
				d.mu.Lock()
				pv.Online = d.reachable[fp]
				d.mu.Unlock()
			}
			s.Peers = append(s.Peers, pv)
		}
	})
	for _, f := range seen {
		s.Peers = append(s.Peers, peerView{FP: f.FP, Name: f.Name, Addr: f.Addr, Online: true, LastSeen: f.LastSeen})
	}
	if s.Shares == nil {
		s.Shares = []*config.Share{}
	}
	if s.Peers == nil {
		s.Peers = []peerView{}
	}
	s.PendingIn, s.PendingOut = []PairRequest{}, []PairRequest{}
	d.mu.Lock()
	for _, p := range d.pendingIn {
		s.PendingIn = append(s.PendingIn, *p)
	}
	for _, p := range d.pendingOut {
		s.PendingOut = append(s.PendingOut, *p)
	}
	d.mu.Unlock()
	s.Transfers = d.transfers.List()
	s.Update = d.updateState()
	return s
}

func (d *Daemon) lState(w http.ResponseWriter, r *http.Request) { writeJSON(w, d.state()) }

func (d *Daemon) lPair(w http.ResponseWriter, r *http.Request) {
	var in struct{ FP, Addr string }
	decode(r, &in)
	addr := in.Addr
	if addr == "" && in.FP != "" {
		if f, ok := d.Disc.Lookup(in.FP); ok {
			addr = f.Addr
		}
	}
	if addr == "" {
		fail(w, errors.New("peer not found; give an address like 192.168.1.5:48080"), 400)
		return
	}
	if !strings.Contains(addr, ":") {
		addr = joinHost(addr, 48080)
	}
	pr, err := d.StartPairing(addr)
	if err != nil {
		fail(w, err, 502)
		return
	}
	writeJSON(w, pr)
}

func (d *Daemon) lPairConfirm(w http.ResponseWriter, r *http.Request) {
	var in struct{ ID string }
	decode(r, &in)
	if err := d.ConfirmOutgoing(in.ID); err != nil {
		fail(w, err, 404)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (d *Daemon) lPairCancel(w http.ResponseWriter, r *http.Request) {
	var in struct{ ID string }
	decode(r, &in)
	d.CancelOutgoing(in.ID)
	writeJSON(w, map[string]bool{"ok": true})
}

func (d *Daemon) lPairRespond(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID    string
		Allow bool
	}
	decode(r, &in)
	if err := d.RespondIncoming(in.ID, in.Allow); err != nil {
		fail(w, err, 404)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (d *Daemon) lUnpair(w http.ResponseWriter, r *http.Request) {
	var in struct{ FP string }
	decode(r, &in)
	if err := d.Unpair(in.FP); err != nil {
		fail(w, err, 500)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (d *Daemon) lShareAdd(w http.ResponseWriter, r *http.Request) {
	var in config.Share
	if err := decode(r, &in); err != nil || in.Name == "" || in.Path == "" {
		fail(w, errors.New("name and path required"), 400)
		return
	}
	abs, err := filepath.Abs(in.Path)
	if err == nil {
		var st os.FileInfo
		st, err = os.Stat(abs)
		if err == nil && !st.IsDir() {
			err = errors.New("share path must be a directory")
		}
	}
	if err != nil {
		fail(w, err, 400)
		return
	}
	in.Path = abs
	if len(in.Peers) == 0 {
		in.Peers = []string{"*"}
	}
	in.ReadOnly = true
	d.Cfg.Update(func(c *config.Config) {
		for i, s := range c.Shares {
			if s.Name == in.Name {
				c.Shares[i] = &in
				return
			}
		}
		c.Shares = append(c.Shares, &in)
	})
	writeJSON(w, in)
}

func (d *Daemon) lShareDel(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	d.Cfg.Update(func(c *config.Config) {
		out := c.Shares[:0]
		for _, s := range c.Shares {
			if s.Name != name {
				out = append(out, s)
			}
		}
		c.Shares = out
	})
	writeJSON(w, map[string]bool{"ok": true})
}

func (d *Daemon) lSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name, Incoming string
		AutoUpdate     string `json:"auto_update"`
	}
	decode(r, &in)
	switch in.AutoUpdate {
	case "", "auto", "notify", "off":
	default:
		fail(w, errors.New("auto_update must be auto, notify or off"), 400)
		return
	}
	if in.Incoming != "" {
		if err := os.MkdirAll(in.Incoming, 0o755); err != nil {
			fail(w, err, 400)
			return
		}
	}
	d.Cfg.Update(func(c *config.Config) {
		if in.Name != "" {
			c.Name = in.Name
		}
		if in.Incoming != "" {
			c.IncomingDir = in.Incoming
		}
		if in.AutoUpdate != "" {
			c.AutoUpdate = in.AutoUpdate
		}
	})
	writeJSON(w, map[string]bool{"ok": true})
}

func (d *Daemon) lPeerShares(w http.ResponseWriter, r *http.Request) {
	pc, err := d.peer(r.URL.Query().Get("peer"))
	if err != nil {
		fail(w, err, 400)
		return
	}
	out, err := pc.Shares(r.Context())
	if err != nil {
		fail(w, err, 502)
		return
	}
	if out == nil {
		out = []map[string]any{}
	}
	writeJSON(w, out)
}

func (d *Daemon) lPeerLs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	pc, err := d.peer(q.Get("peer"))
	if err != nil {
		fail(w, err, 400)
		return
	}
	out, err := pc.Ls(r.Context(), q.Get("share"), q.Get("path"))
	if err != nil {
		fail(w, err, 502)
		return
	}
	if out == nil {
		out = []Entry{}
	}
	writeJSON(w, out)
}

// lPeerGet proxies a remote file to the browser: as a download, or with
// inline=1 as a sandboxed preview (Range requests pass through for video
// seeking), or with thumb=N as a small JPEG thumbnail.
func (d *Daemon) lPeerGet(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	pc, err := d.peer(q.Get("peer"))
	if err != nil {
		fail(w, err, 400)
		return
	}
	inline := q.Get("inline") == "1"
	thumb, _ := strconv.Atoi(q.Get("thumb"))
	name := path.Base(q.Get("path"))
	rangeHdr := ""
	if inline {
		rangeHdr = r.Header.Get("Range")
	}
	resp, err := pc.OpenRaw(r.Context(), q.Get("share"), q.Get("path"), rangeHdr, inline || thumb > 0, thumb)
	if err != nil {
		fail(w, err, 502)
		return
	}
	defer resp.Body.Close()
	h := w.Header()
	for _, k := range []string{"Content-Type", "Content-Length", "Content-Disposition", "Content-Range", "Accept-Ranges", "Last-Modified", "Cache-Control"} {
		if v := resp.Header.Get(k); v != "" {
			h.Set(k, v)
		}
	}
	if thumb > 0 {
		h.Set("Content-Type", "image/jpeg")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "sandbox; default-src 'none'")
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		return
	}
	if inline {
		previewHeaders(h, name)
		w.WriteHeader(resp.StatusCode)
		copyBuf(w, resp.Body)
		return
	}
	if h.Get("Content-Disposition") == "" {
		h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	}
	pname, _ := d.Cfg.Trusted(pc.fp)
	t := d.transfers.Start("download", pname.Name, name, resp.ContentLength)
	t.Dest = "(browser)"
	_, err = copyBuf(&countingWriter{w, t}, resp.Body)
	d.transfers.Finish(t, err)
}

// lPeerDownload saves a remote path to a local directory (CLI use).
func (d *Daemon) lPeerDownload(w http.ResponseWriter, r *http.Request) {
	var in struct{ Peer, Share, Path, Dest string }
	decode(r, &in)
	pc, err := d.peer(in.Peer)
	if err != nil {
		fail(w, err, 400)
		return
	}
	if in.Dest == "" {
		in.Dest = d.Cfg.IncomingDir
	}
	t, err := pc.Download(context.Background(), in.Share, in.Path, in.Dest)
	if err != nil {
		fail(w, err, 502)
		return
	}
	writeJSON(w, t.snapshot())
}

// lSendUpload receives multipart files from the browser and streams each to the peer.
func (d *Daemon) lSendUpload(w http.ResponseWriter, r *http.Request) {
	pc, err := d.peer(r.URL.Query().Get("peer"))
	if err != nil {
		fail(w, err, 400)
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		fail(w, err, 400)
		return
	}
	var sent []Transfer
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			fail(w, err, 400)
			return
		}
		// part.FileName() strips directories; parse the header ourselves so
		// folder drops keep their relative paths.
		name := ""
		if _, params, err := mime.ParseMediaType(part.Header.Get("Content-Disposition")); err == nil {
			name = strings.ReplaceAll(params["filename"], "\\", "/")
		}
		if name == "" {
			continue
		}
		// The browser can't tell us the size up front per part; we stream with
		// chunked encoding and the UI reports the browser-side upload progress.
		t, err := pc.SendStream(r.Context(), name, -1, part)
		if t != nil {
			sent = append(sent, t.snapshot())
		}
		if err != nil {
			fail(w, fmt.Errorf("%s: %w", name, err), 502)
			return
		}
	}
	writeJSON(w, sent)
}

func (d *Daemon) lSendPath(w http.ResponseWriter, r *http.Request) {
	var in struct{ Peer, Path string }
	decode(r, &in)
	pc, err := d.peer(in.Peer)
	if err != nil {
		fail(w, err, 400)
		return
	}
	ts, err := pc.SendPath(context.Background(), in.Path)
	if err != nil {
		fail(w, err, 502)
		return
	}
	out := []Transfer{}
	for _, t := range ts {
		if t != nil {
			out = append(out, t.snapshot())
		}
	}
	writeJSON(w, out)
}

func (d *Daemon) lTransfer(w http.ResponseWriter, r *http.Request) {
	t, ok := d.transfers.Get(r.PathValue("id"))
	if !ok {
		fail(w, errors.New("no such transfer"), 404)
		return
	}
	writeJSON(w, t)
}

func (d *Daemon) lUpdateInstall(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := d.InstallUpdate(ctx); err != nil {
		fail(w, err, 409)
		return
	}
	writeJSON(w, d.updateState())
}

// lShutdown stops the whole node: peer API, discovery, update checks and UI.
func (d *Daemon) lShutdown(w http.ResponseWriter, r *http.Request) {
	if d.busy() && r.URL.Query().Get("force") != "1" {
		fail(w, errors.New("a transfer is in progress; stop anyway with: wharfdrop stop --force"), 409)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
	go func() {
		time.Sleep(200 * time.Millisecond) // let the reply go out
		select {
		case d.shutdown <- struct{}{}:
		default:
		}
	}()
}

// lOpenFolder opens the received-files folder in the system file manager.
// Only this fixed folder can be opened; the request cannot name a path.
func (d *Daemon) lOpenFolder(w http.ResponseWriter, r *http.Request) {
	var dir string
	d.Cfg.View(func(c *config.Config) { dir = c.IncomingDir })
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail(w, err, 500)
		return
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer", dir)
	case "darwin":
		cmd = exec.Command("open", dir)
	default:
		cmd = exec.Command("xdg-open", dir)
	}
	if err := cmd.Start(); err != nil {
		fail(w, fmt.Errorf("could not open %s: %w", dir, err), 500)
		return
	}
	go cmd.Wait()
	writeJSON(w, map[string]bool{"ok": true})
}
