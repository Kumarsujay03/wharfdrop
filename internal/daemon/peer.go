package daemon

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Kumarsujay03/wharfdrop/internal/config"
	"github.com/Kumarsujay03/wharfdrop/internal/identity"
)

// ---------- peer-facing server (mTLS, reachable on the LAN) ----------

const maxPendingIn = 8

func (d *Daemon) peerMux() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /info", d.hInfo)
	m.HandleFunc("POST /pair/request", d.hPairRequest)
	m.HandleFunc("GET /pair/status", d.hPairStatus)
	m.HandleFunc("GET /shares", d.trusted(d.hShares))
	m.HandleFunc("GET /ls", d.trusted(d.hLs))
	m.HandleFunc("GET /get", d.trusted(d.hGet))
	m.HandleFunc("POST /send", d.trusted(d.hRecv))
	m.HandleFunc("POST /recv/start", d.trusted(d.hRecvStart))
	m.HandleFunc("PUT /recv/chunk", d.trusted(d.hRecvChunk))
	m.HandleFunc("POST /recv/finish", d.trusted(d.hRecvFinish))
	m.HandleFunc("GET /avatar", d.trusted(d.hAvatar))
	return m
}

func callerFP(r *http.Request) string {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return ""
	}
	return identity.FingerprintOf(r.TLS.PeerCertificates[0])
}

// trusted only lets paired devices through.
func (d *Daemon) trusted(h func(http.ResponseWriter, *http.Request, *config.Peer)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fp := callerFP(r)
		p, ok := d.Cfg.Trusted(fp)
		if fp == "" || !ok {
			http.Error(w, "not paired", http.StatusForbidden)
			return
		}
		// remember where we last saw the peer (only write config when it changed)
		if host, _, err := splitHost(r.RemoteAddr); err == nil {
			addr := joinHost(host, d.peerPortOf(fp))
			if p.LastAddr != addr {
				d.Cfg.Update(func(c *config.Config) {
					if pp := c.Peers[fp]; pp != nil {
						pp.LastAddr = addr
					}
				})
			}
		}
		h(w, r, p)
	}
}

func (d *Daemon) peerPortOf(fp string) int {
	if f, ok := d.Disc.Lookup(fp); ok {
		if _, port, err := splitHost(f.Addr); err == nil {
			if n, err := strconv.Atoi(port); err == nil {
				return n
			}
		}
	}
	if p, ok := d.Cfg.Trusted(fp); ok && p.LastAddr != "" {
		if _, port, err := splitHost(p.LastAddr); err == nil {
			if n, err := strconv.Atoi(port); err == nil {
				return n
			}
		}
	}
	return d.Cfg.PeerPort
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (d *Daemon) hInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"name": d.Cfg.Name, "fp": d.ID.Fingerprint, "version": Version})
}

// hPairRequest: a remote device asks to pair. We compute the SAS from the
// client certificate it presented and queue it for the local user.
func (d *Daemon) hPairRequest(w http.ResponseWriter, r *http.Request) {
	fp := callerFP(r)
	if fp == "" {
		http.Error(w, "client certificate required", http.StatusBadRequest)
		return
	}
	var body struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Port int    `json:"port"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil || body.ID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	host, _, _ := splitHost(r.RemoteAddr)
	req := &PairRequest{ID: body.ID, PeerFP: fp, PeerName: body.Name,
		PeerAddr: joinHost(host, body.Port), Code: identity.SAS(fp, d.ID.Fingerprint),
		Status: "pending", Created: time.Now()}
	d.mu.Lock()
	// limit unauthenticated requests so a noisy LAN device cannot flood the UI
	if len(d.pendingIn) >= maxPendingIn {
		d.mu.Unlock()
		http.Error(w, "too many pending pairing requests", http.StatusTooManyRequests)
		return
	}
	// one pending request per peer
	for id, p := range d.pendingIn {
		if p.PeerFP == fp {
			delete(d.pendingIn, id)
		}
	}
	d.pendingIn[req.ID] = req
	d.mu.Unlock()
	if _, already := d.Cfg.Trusted(fp); already {
		// re-pair: accept silently; keys unchanged
		d.mu.Lock()
		req.Status = "accepted"
		d.mu.Unlock()
	}
	writeJSON(w, map[string]any{"code": req.Code, "name": d.Cfg.Name})
}

func (d *Daemon) hPairStatus(w http.ResponseWriter, r *http.Request) {
	fp := callerFP(r)
	id := r.URL.Query().Get("id")
	d.mu.Lock()
	p, ok := d.pendingIn[id]
	status := "unknown"
	if ok && p.PeerFP == fp {
		status = p.Status
	}
	d.mu.Unlock()
	writeJSON(w, map[string]string{"status": status})
}

func (d *Daemon) hShares(w http.ResponseWriter, r *http.Request, p *config.Peer) {
	type s struct {
		Name     string `json:"name"`
		ReadOnly bool   `json:"read_only"`
	}
	out := []s{}
	for _, sh := range d.Cfg.SharesFor(p.Fingerprint) {
		out = append(out, s{sh.Name, sh.ReadOnly})
	}
	writeJSON(w, out)
}

func (d *Daemon) resolve(r *http.Request, p *config.Peer) (string, *config.Share, error) {
	q := r.URL.Query()
	sh, ok := d.Cfg.ShareFor(p.Fingerprint, q.Get("share"))
	if !ok {
		return "", nil, errors.New("no such share")
	}
	full, err := safeJoin(sh.Path, q.Get("path"))
	return full, sh, err
}

func (d *Daemon) hLs(w http.ResponseWriter, r *http.Request, p *config.Peer) {
	full, _, err := d.resolve(r, p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	entries, err := listDir(full)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, entries)
}

func (d *Daemon) hGet(w http.ResponseWriter, r *http.Request, p *config.Peer) {
	full, _, err := d.resolve(r, p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	info, err := os.Stat(full)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if info.IsDir() {
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": info.Name() + ".zip"}))
		t := d.transfers.Start("send", p.Name, info.Name()+"/", 0)
		err := zipDir(&countingWriter{w, t}, full)
		d.transfers.Finish(t, err)
		return
	}
	q := r.URL.Query()
	if n, _ := strconv.Atoi(q.Get("thumb")); n > 0 {
		if !writeThumb(w, full, n) {
			http.Error(w, "no thumbnail for this file", http.StatusUnsupportedMediaType)
		}
		return
	}
	f, err := os.Open(full)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer f.Close()
	if q.Get("preview") == "1" {
		// Previews (video seeking, images, text) are not listed as transfers.
		http.ServeContent(w, r, info.Name(), info.ModTime(), f)
		return
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": info.Name()}))
	t := d.transfers.Start("send", p.Name, info.Name(), info.Size())
	// ServeContent gives us Range support for resumable downloads.
	http.ServeContent(&countingRW{w, t}, r, info.Name(), info.ModTime(), f)
	d.transfers.Finish(t, nil)
}

type countingRW struct {
	http.ResponseWriter
	t *Transfer
}

func (c *countingRW) Write(p []byte) (int, error) {
	n, err := c.ResponseWriter.Write(p)
	atomic.AddInt64(&c.t.cur, int64(n))
	return n, err
}

// hRecv accepts a pushed file into the incoming directory.
func (d *Daemon) hRecv(w http.ResponseWriter, r *http.Request, p *config.Peer) {
	name := r.URL.Query().Get("name")
	size, _ := strconv.ParseInt(r.URL.Query().Get("size"), 10, 64)
	full, err := safeJoin(d.Cfg.IncomingDir, name)
	if err != nil || name == "" || strings.HasSuffix(name, "/") {
		http.Error(w, "bad name", http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	full = uniquePath(full)
	tmp := full + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	t := d.transfers.Start("receive", p.Name, name, size)
	t.Dest = full
	_, err = copyBuf(f, &countingReader{r: r.Body, t: t})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, full)
	}
	if err != nil {
		os.Remove(tmp)
		d.transfers.Finish(t, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	d.transfers.Finish(t, nil)
	writeJSON(w, map[string]any{"saved": full})
}

// ---------- client side: talking to a peer ----------

// client returns an HTTP client that only accepts the given fingerprint
// (or, during pairing, records whatever it sees via onCert).
func (d *Daemon) client(expectFP string, onCert func(*x509.Certificate)) *http.Client {
	tc := &tls.Config{
		Certificates:       []tls.Certificate{d.ID.Cert},
		InsecureSkipVerify: true, // replaced by fingerprint pinning below
		MinVersion:         tls.VersionTLS13,
		NextProtos:         []string{"http/1.1"},
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("peer sent no certificate")
			}
			fp := identity.FingerprintOf(cs.PeerCertificates[0])
			if onCert != nil {
				onCert(cs.PeerCertificates[0])
			}
			if expectFP != "" && fp != expectFP {
				return fmt.Errorf("peer identity mismatch (expected %s, got %s)", identity.Short(expectFP), identity.Short(fp))
			}
			return nil
		},
	}
	tr := &http.Transport{TLSClientConfig: tc, ForceAttemptHTTP2: false,
		TLSNextProto:    map[string]func(string, *tls.Conn) http.RoundTripper{},
		WriteBufferSize: 1 << 20, ReadBufferSize: 1 << 20,
		DialContext:         (&netDialer{}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second}
	return &http.Client{Transport: tr}
}

// peerAddr finds how to reach a trusted peer: live discovery first, then the last known address.
func (d *Daemon) peerAddr(fp string) (string, error) {
	if f, ok := d.Disc.Lookup(fp); ok {
		return f.Addr, nil
	}
	if p, ok := d.Cfg.Trusted(fp); ok && p.LastAddr != "" {
		return p.LastAddr, nil
	}
	return "", errors.New("peer is not reachable (not seen on the network)")
}

func (d *Daemon) peerURL(addr, p string, q url.Values) string {
	u := url.URL{Scheme: "https", Host: addr, Path: p, RawQuery: q.Encode()}
	return u.String()
}

type peerClient struct {
	d    *Daemon
	fp   string
	addr string
	c    *http.Client
}

func (d *Daemon) peer(fp string) (*peerClient, error) {
	if _, ok := d.Cfg.Trusted(fp); !ok {
		return nil, errors.New("peer is not paired")
	}
	addr, err := d.peerAddr(fp)
	if err != nil {
		return nil, err
	}
	return &peerClient{d: d, fp: fp, addr: addr, c: d.peerHTTP(fp)}, nil
}

func (pc *peerClient) get(ctx context.Context, p string, q url.Values) (*http.Response, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", pc.d.peerURL(pc.addr, p, q), nil)
	resp, err := pc.c.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		resp.Body.Close()
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return resp, nil
}

func (pc *peerClient) Shares(ctx context.Context) ([]map[string]any, error) {
	resp, err := pc.get(ctx, "/shares", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out []map[string]any
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

func (pc *peerClient) Ls(ctx context.Context, share, p string) ([]Entry, error) {
	resp, err := pc.get(ctx, "/ls", url.Values{"share": {share}, "path": {p}})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out []Entry
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

// Open streams a remote file (or zip of a directory).
func (pc *peerClient) Open(ctx context.Context, share, p string) (*http.Response, error) {
	return pc.get(ctx, "/get", url.Values{"share": {share}, "path": {p}})
}

// Download saves a remote path into destDir, tracking progress.
func (pc *peerClient) Download(ctx context.Context, share, p, destDir string) (*Transfer, error) {
	resp, err := pc.Open(ctx, share, p)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	name := path.Base(p)
	if name == "." || name == "/" || name == "" {
		name = share
	}
	if _, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil && params["filename"] != "" {
		name = params["filename"]
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, err
	}
	full := uniquePath(filepath.Join(destDir, filepath.Base(name)))
	f, err := os.Create(full + ".part")
	if err != nil {
		return nil, err
	}
	pname, _ := pc.d.Cfg.Trusted(pc.fp)
	t := pc.d.transfers.Start("download", pname.Name, name, resp.ContentLength)
	t.Dest = full
	_, err = copyBuf(f, &countingReader{r: resp.Body, t: t})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(full+".part", full)
	}
	if err != nil {
		os.Remove(full + ".part")
	}
	pc.d.transfers.Finish(t, err)
	return t, err
}

// SendStream pushes a stream to the peer's incoming directory under name.
func (pc *peerClient) SendStream(ctx context.Context, name string, size int64, r io.Reader) (*Transfer, error) {
	pname, _ := pc.d.Cfg.Trusted(pc.fp)
	t := pc.d.transfers.Start("send", pname.Name, name, size)
	q := url.Values{"name": {name}, "size": {strconv.FormatInt(size, 10)}}
	req, _ := http.NewRequestWithContext(ctx, "POST", pc.d.peerURL(pc.addr, "/send", q), &countingReader{r: r, t: t})
	req.ContentLength = size
	resp, err := pc.c.Do(req)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			err = fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
		}
	}
	pc.d.transfers.Finish(t, err)
	return t, err
}

// SendPath sends a local file, or every file under a local directory.
func (pc *peerClient) SendPath(ctx context.Context, local string) ([]*Transfer, error) {
	info, err := os.Stat(local)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		t, err := pc.SendFile(ctx, info.Name(), local)
		return []*Transfer{t}, err
	}
	var out []*Transfer
	base := filepath.Base(local)
	err = filepath.Walk(local, func(p string, fi os.FileInfo, err error) error {
		if err != nil || !fi.Mode().IsRegular() {
			return err
		}
		rel, _ := filepath.Rel(local, p)
		t, err := pc.SendFile(ctx, path.Join(base, filepath.ToSlash(rel)), p)
		out = append(out, t)
		return err
	})
	return out, err
}

// ---------- pairing (outgoing) ----------

// StartPairing contacts addr, learns its certificate, sends a pair request and
// returns the request with the code to show the local user. It keeps polling
// the remote side in the background.
func (d *Daemon) StartPairing(addr string) (*PairRequest, error) {
	var seen *x509.Certificate
	c := d.client("", func(cert *x509.Certificate) { seen = cert })
	id := newID()
	body, _ := json.Marshal(map[string]any{"id": id, "name": d.Cfg.Name, "port": d.Cfg.PeerPort})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	req, _ := http.NewRequestWithContext(ctx, "POST", d.peerURL(addr, "/pair/request", nil), strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("could not reach %s: %w", addr, err)
	}
	defer resp.Body.Close()
	var reply struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil || seen == nil {
		return nil, errors.New("bad pairing reply")
	}
	fp := identity.FingerprintOf(seen)
	code := identity.SAS(fp, d.ID.Fingerprint)
	if code != reply.Code {
		return nil, errors.New("pairing code mismatch: possible interception, aborting")
	}
	pctx, pcancel := context.WithCancel(context.Background())
	pr := &PairRequest{ID: id, PeerFP: fp, PeerName: reply.Name, PeerAddr: addr, Code: code,
		Status: "pending", Created: time.Now(), cancel: pcancel}
	d.mu.Lock()
	for oid, p := range d.pendingOut {
		if p.PeerFP == fp {
			if p.cancel != nil {
				p.cancel()
			}
			delete(d.pendingOut, oid)
		}
	}
	d.pendingOut[id] = pr
	d.mu.Unlock()
	go d.pollPairing(pctx, pr, addr)
	return pr, nil
}

func (d *Daemon) pollPairing(ctx context.Context, pr *PairRequest, addr string) {
	c := d.client(pr.PeerFP, nil)
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", d.peerURL(addr, "/pair/status", url.Values{"id": {pr.ID}}), nil)
		resp, err := c.Do(req)
		if err != nil {
			continue
		}
		var st struct{ Status string }
		json.NewDecoder(resp.Body).Decode(&st)
		resp.Body.Close()
		d.mu.Lock()
		switch st.Status {
		case "accepted":
			pr.Remote = true
		case "denied", "unknown":
			pr.Status = "denied"
			d.mu.Unlock()
			return
		}
		done := pr.Remote && pr.Local
		d.mu.Unlock()
		if done {
			d.finishPairing(pr)
			return
		}
	}
}

func (d *Daemon) finishPairing(pr *PairRequest) {
	d.Cfg.Update(func(c *config.Config) {
		c.Peers[pr.PeerFP] = &config.Peer{Fingerprint: pr.PeerFP, Name: pr.PeerName, LastAddr: pr.PeerAddr}
	})
	d.mu.Lock()
	pr.Status = "accepted"
	d.mu.Unlock()
}

// ConfirmOutgoing: the local user confirmed the code matches.
func (d *Daemon) ConfirmOutgoing(id string) error {
	d.mu.Lock()
	pr, ok := d.pendingOut[id]
	if !ok {
		d.mu.Unlock()
		return errors.New("no such pairing")
	}
	pr.Local = true
	done := pr.Remote
	d.mu.Unlock()
	if done {
		d.finishPairing(pr)
	}
	return nil
}

func (d *Daemon) CancelOutgoing(id string) {
	d.mu.Lock()
	if pr, ok := d.pendingOut[id]; ok {
		if pr.cancel != nil {
			pr.cancel()
		}
		pr.Status = "cancelled"
	}
	d.mu.Unlock()
}

// RespondIncoming: local user allowed or denied a request from a remote device.
func (d *Daemon) RespondIncoming(id string, allow bool) error {
	d.mu.Lock()
	pr, ok := d.pendingIn[id]
	if !ok {
		d.mu.Unlock()
		return errors.New("no such pairing request")
	}
	if allow {
		pr.Status = "accepted"
	} else {
		pr.Status = "denied"
	}
	d.mu.Unlock()
	if allow {
		return d.Cfg.Update(func(c *config.Config) {
			c.Peers[pr.PeerFP] = &config.Peer{Fingerprint: pr.PeerFP, Name: pr.PeerName, LastAddr: pr.PeerAddr}
		})
	}
	return nil
}

func (d *Daemon) Unpair(fp string) error {
	forgetClient(fp)
	return d.Cfg.Update(func(c *config.Config) {
		delete(c.Peers, fp)
		for _, s := range c.Shares {
			s.Peers = removeStr(s.Peers, fp)
		}
	})
}

func removeStr(xs []string, s string) []string {
	out := xs[:0]
	for _, x := range xs {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}
