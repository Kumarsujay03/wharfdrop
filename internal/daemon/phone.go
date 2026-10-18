package daemon

// Phone link: phones join through their web browser by scanning a QR code.
//
// Phone browsers block WebCrypto on plain-HTTP LAN pages, and a self-signed
// HTTPS certificate only trains people to click through warnings. So the
// portal is plain HTTP and everything is encrypted inside the page instead:
// the QR code carries a 256-bit key in the URL fragment (never sent over the
// network), and every request, response and file chunk is sealed with
// XChaCha20-Poly1305 using that key. Associated data binds each message to
// its purpose, upload and position. Without the key a device on the same
// network can neither read nor forge anything. The page itself is served over
// HTTP, so an attacker actively tampering with the LAN while the page loads
// remains out of scope; see SECURITY.md.

import (
	"bytes"
	"context"
	"crypto/cipher"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Kumarsujay03/wharfdrop/internal/config"
	"golang.org/x/crypto/chacha20poly1305"
	"rsc.io/qr"
)

//go:embed all:phone
var phoneFiles embed.FS

const (
	phoneIdle      = 10 * time.Minute
	phoneChunk     = 4 << 20 // plaintext bytes per upload request
	phoneFrame     = 1 << 20 // plaintext bytes per download frame
	phoneOnline    = 12 * time.Second
	phoneClockSkew = 5 * time.Minute
)

type phoneSession struct {
	ID       string
	Name     string
	Key      []byte
	Remember bool
	LastSeen time.Time
	aead     cipher.AEAD
}

type outItem struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	path  string
	temp  bool // file lives in the outbox folder and is deleted afterwards
	Added time.Time
}

type dlToken struct {
	sess    *phoneSession
	path    string
	name    string
	size    int64
	thumb   int
	avatar  bool
	outID   string
	expires time.Time
}

type phoneUpload struct {
	sess    *phoneSession
	f       *os.File
	tmp     string
	full    string
	size    int64
	written int64
	t       *Transfer
	last    time.Time
}

type phoneHub struct {
	d            *Daemon
	mu           sync.Mutex
	srv          *http.Server
	port         int
	sessions     map[string]*phoneSession
	pending      string // session id shown in the current QR code
	lastActivity time.Time
	nonces       map[string]time.Time
	outbox       map[string][]*outItem // phone id -> items
	tokens       map[string]*dlToken
	uploads      map[string]*phoneUpload
	fails        map[string][]time.Time
}

func newPhoneHub(d *Daemon) *phoneHub {
	return &phoneHub{d: d, sessions: map[string]*phoneSession{}, nonces: map[string]time.Time{},
		outbox: map[string][]*outItem{}, tokens: map[string]*dlToken{}, uploads: map[string]*phoneUpload{},
		fails: map[string][]time.Time{}}
}

func newSession(id string, key []byte, name string, remember bool) (*phoneSession, error) {
	a, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	return &phoneSession{ID: id, Key: key, Name: name, Remember: remember, aead: a}, nil
}

func randB64(n int) (string, []byte) {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b), b
}

// seal returns nonce||ciphertext.
func seal(a cipher.AEAD, aad string, plain []byte) []byte {
	nonce := make([]byte, a.NonceSize(), a.NonceSize()+len(plain)+a.Overhead())
	rand.Read(nonce)
	return a.Seal(nonce, nonce, plain, []byte(aad))
}

func open(a cipher.AEAD, aad string, msg []byte) ([]byte, error) {
	if len(msg) < a.NonceSize()+a.Overhead() {
		return nil, errors.New("message too short")
	}
	return a.Open(nil, msg[:a.NonceSize()], msg[a.NonceSize():], []byte(aad))
}

// ---------- lifecycle ----------

// Start opens the phone port (if needed) and creates a fresh QR session.
func (h *phoneHub) Start() (url string, alts []string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.srv == nil {
		port := 48082
		h.d.Cfg.View(func(c *config.Config) {
			if c.PhonePort != 0 {
				port = c.PhonePort
			}
		})
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			return "", nil, fmt.Errorf("could not open phone port %d: %w", port, err)
		}
		h.port = port
		h.srv = &http.Server{Handler: h.mux(), ReadHeaderTimeout: 15 * time.Second}
		go func(srv *http.Server) {
			if err := srv.Serve(bufListener{ln}); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("phone link: %v", err)
			}
		}(h.srv)
		// remembered phones can reconnect without a new QR code
		h.d.Cfg.View(func(c *config.Config) {
			for _, p := range c.Phones {
				key, err := base64.RawURLEncoding.DecodeString(p.Key)
				if err != nil || len(key) != 32 {
					continue
				}
				if s, err := newSession(p.ID, key, p.Name, true); err == nil {
					h.sessions[p.ID] = s
				}
			}
		})
		log.Printf("phone link open on port %d", port)
	}
	if old := h.pending; old != "" {
		if s := h.sessions[old]; s != nil && s.LastSeen.IsZero() {
			delete(h.sessions, old) // an unused QR code is replaced
		}
	}
	id, _ := randB64(12)
	keyText, key := randB64(32)
	s, err := newSession(id, key, "", false)
	if err != nil {
		return "", nil, err
	}
	h.sessions[id] = s
	h.pending = id
	h.lastActivity = time.Now()
	frag := "#" + id + "." + keyText
	addrs := lanAddrs()
	for i, a := range addrs {
		u := fmt.Sprintf("http://%s/%s", net.JoinHostPort(a, strconv.Itoa(h.port)), frag)
		if i == 0 {
			url = u
		} else {
			alts = append(alts, u)
		}
	}
	if url == "" {
		return "", nil, errors.New("this computer has no local network address")
	}
	return url, alts, nil
}

// Stop closes the phone port and forgets temporary sessions and outbox files.
func (h *phoneHub) Stop(why string) {
	h.mu.Lock()
	srv := h.srv
	h.srv = nil
	h.pending = ""
	for id, s := range h.sessions {
		if !s.Remember {
			delete(h.sessions, id)
		}
	}
	for _, items := range h.outbox {
		for _, it := range items {
			if it.temp {
				os.Remove(it.path)
			}
		}
	}
	h.outbox = map[string][]*outItem{}
	h.tokens = map[string]*dlToken{}
	for id, u := range h.uploads {
		u.f.Close()
		os.Remove(u.tmp)
		h.d.transfers.Finish(u.t, errors.New("phone link closed"))
		delete(h.uploads, id)
	}
	h.mu.Unlock()
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		srv.Shutdown(ctx)
		cancel()
		log.Printf("phone link closed (%s)", why)
	}
}

func (h *phoneHub) running() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.srv != nil
}

func (h *phoneHub) touch() {
	h.mu.Lock()
	h.lastActivity = time.Now()
	h.mu.Unlock()
}

// idleLoop closes the phone link after 10 minutes without activity.
func (h *phoneHub) idleLoop(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			h.Stop("Wharfdrop stopped")
			return
		case <-t.C:
		}
		h.mu.Lock()
		on := h.srv != nil
		idle := time.Since(h.lastActivity)
		busy := len(h.uploads) > 0
		now := time.Now()
		for n, at := range h.nonces {
			if now.Sub(at) > 2*phoneClockSkew {
				delete(h.nonces, n)
			}
		}
		for k, tok := range h.tokens {
			if now.After(tok.expires) {
				delete(h.tokens, k)
			}
		}
		h.mu.Unlock()
		if on && busy {
			h.touch()
			continue
		}
		if on && idle > phoneIdle {
			h.Stop("10 minutes without activity")
		}
	}
}

// lanAddrs lists private IPv4 addresses, most likely home-network first.
func lanAddrs() []string {
	var a, b, c, other []string
	for _, ip := range LocalAddrs() {
		switch {
		case strings.HasPrefix(ip, "192.168."):
			a = append(a, ip)
		case strings.HasPrefix(ip, "10."):
			b = append(b, ip)
		case strings.HasPrefix(ip, "172."):
			c = append(c, ip)
		default:
			other = append(other, ip)
		}
	}
	return append(append(append(a, b...), c...), other...)
}

// QRPNG encodes text as a QR code PNG.
func QRPNG(text string) ([]byte, error) {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return nil, err
	}
	code.Scale = 8
	return code.PNG(), nil
}

// ---------- phone-facing HTTP ----------

func (h *phoneHub) mux() http.Handler {
	m := http.NewServeMux()
	static, _ := fs.Sub(phoneFiles, "phone")
	fileServer := http.FileServer(http.FS(static))
	m.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && !strings.HasPrefix(r.URL.Path, "/noble/") && r.URL.Path != "/phone.js" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' blob: data:; media-src blob:; style-src 'self' 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'")
		if r.URL.Path == "/" {
			r.URL.Path = "/phone.html"
		}
		fileServer.ServeHTTP(w, r)
	})
	m.HandleFunc("POST /api/rpc", h.rpc)
	m.HandleFunc("PUT /api/up", h.upChunk)
	m.HandleFunc("GET /api/down/{token}", h.down)
	return m
}

func clientIP(r *http.Request) string {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}

// tooManyFails limits unauthenticated guessing from one address.
func (h *phoneHub) tooManyFails(ip string, add bool) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	recent := h.fails[ip][:0]
	for _, t := range h.fails[ip] {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	if add {
		recent = append(recent, now)
	}
	h.fails[ip] = recent
	return len(recent) > 30
}

func (h *phoneHub) session(id string) *phoneSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessions[id]
}

type rpcReq struct {
	Op       string `json:"op"`
	T        int64  `json:"t"` // phone clock, ms
	Name     string `json:"name,omitempty"`
	Remember bool   `json:"remember,omitempty"`
	Share    string `json:"share,omitempty"`
	Path     string `json:"path,omitempty"`
	ID       string `json:"id,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Thumb    int    `json:"thumb,omitempty"`
}

func (h *phoneHub) rpc(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if h.tooManyFails(ip, false) {
		http.Error(w, "too many attempts", http.StatusTooManyRequests)
		return
	}
	s := h.session(r.Header.Get("X-Session"))
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var plain []byte
	if s != nil && err == nil {
		plain, err = open(s.aead, "rpc|"+s.ID, body)
	}
	if s == nil || err != nil {
		h.tooManyFails(ip, true)
		http.Error(w, "unknown or expired phone link", http.StatusUnauthorized)
		return
	}
	var req rpcReq
	if err := json.Unmarshal(plain, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// replay protection: fresh timestamp and an unseen nonce
	if d := time.Since(time.UnixMilli(req.T)); d > phoneClockSkew || d < -phoneClockSkew {
		http.Error(w, "phone clock is too far off", http.StatusUnauthorized)
		return
	}
	nonceKey := s.ID + string(body[:s.aead.NonceSize()])
	h.mu.Lock()
	if _, seen := h.nonces[nonceKey]; seen {
		h.mu.Unlock()
		http.Error(w, "replayed request", http.StatusUnauthorized)
		return
	}
	h.nonces[nonceKey] = time.Now()
	s.LastSeen = time.Now()
	if req.Op != "state" {
		// background status checks keep the phone "online" but do not keep
		// the link open; only real actions do
		h.lastActivity = time.Now()
	}
	if h.pending == s.ID {
		h.pending = "" // the QR code has been used
	}
	h.mu.Unlock()

	out, status := h.handleRPC(s, &req)
	b, _ := json.Marshal(out)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write(seal(s.aead, "rsp|"+s.ID, b))
}

func errOut(msg string) map[string]any { return map[string]any{"error": msg} }

func (h *phoneHub) handleRPC(s *phoneSession, req *rpcReq) (any, int) {
	switch req.Op {
	case "hello":
		name := strings.TrimSpace(req.Name)
		if len(name) > 40 {
			name = name[:40]
		}
		if name == "" {
			name = "Phone"
		}
		h.mu.Lock()
		s.Name = name
		if req.Remember != s.Remember {
			s.Remember = req.Remember
		}
		h.mu.Unlock()
		h.persistPhone(s)
		return h.phoneState(s), 200
	case "state":
		return h.phoneState(s), 200
	case "ls":
		sh, ok := h.phoneShare(req.Share)
		if !ok {
			return errOut("no such shared folder"), 404
		}
		full, err := safeJoin(sh.Path, req.Path)
		if err != nil {
			return errOut("bad path"), 400
		}
		ents, err := listDir(full)
		if err != nil {
			return errOut("cannot open this folder"), 404
		}
		sort.Slice(ents, func(i, j int) bool {
			if ents[i].Dir != ents[j].Dir {
				return ents[i].Dir
			}
			return strings.ToLower(ents[i].Name) < strings.ToLower(ents[j].Name)
		})
		return map[string]any{"entries": ents}, 200
	case "up-start":
		return h.upStart(s, req)
	case "up-finish":
		return h.upFinish(s, req.ID, false)
	case "up-abort":
		return h.upFinish(s, req.ID, true)
	case "get":
		return h.makeToken(s, req)
	case "outbox-done":
		h.mu.Lock()
		items := h.outbox[s.ID]
		for i, it := range items {
			if it.ID == req.ID {
				if it.temp {
					os.Remove(it.path)
				}
				h.outbox[s.ID] = append(items[:i:i], items[i+1:]...)
				break
			}
		}
		h.mu.Unlock()
		return h.phoneState(s), 200
	case "forget":
		h.mu.Lock()
		s.Remember = false
		h.mu.Unlock()
		h.persistPhone(s)
		return map[string]bool{"ok": true}, 200
	}
	return errOut("unknown request"), 400
}

func (h *phoneHub) phoneState(s *phoneSession) map[string]any {
	d := h.d
	var shares []string
	for _, sh := range d.Cfg.SharesFor("*") {
		shares = append(shares, sh.Name)
	}
	h.mu.Lock()
	items := append([]*outItem{}, h.outbox[s.ID]...)
	name, remember := s.Name, s.Remember
	idleLeft := phoneIdle - time.Since(h.lastActivity)
	h.mu.Unlock()
	var cname string
	d.Cfg.View(func(c *config.Config) { cname = c.Name })
	return map[string]any{
		"computer": cname, "avatar": d.AvatarVersion() != 0, "shares": shares,
		"outbox": items, "name": name, "remember": remember, "idle": int(idleLeft.Seconds()),
	}
}

// phoneShare returns a share the phones may see: shared with all paired
// devices ("*").
func (h *phoneHub) phoneShare(name string) (*config.Share, bool) {
	for _, sh := range h.d.Cfg.SharesFor("*") {
		if sh.Name == name {
			return sh, true
		}
	}
	return nil, false
}

// persistPhone saves or removes a remembered phone in the config.
func (h *phoneHub) persistPhone(s *phoneSession) {
	h.d.Cfg.Update(func(c *config.Config) {
		out := c.Phones[:0]
		for _, p := range c.Phones {
			if p.ID != s.ID {
				out = append(out, p)
			}
		}
		c.Phones = out
		if s.Remember {
			c.Phones = append(c.Phones, &config.Phone{ID: s.ID, Name: s.Name,
				Key: base64.RawURLEncoding.EncodeToString(s.Key), Added: time.Now()})
		}
	})
}

// ---------- phone -> computer uploads ----------

func (h *phoneHub) upStart(s *phoneSession, req *rpcReq) (any, int) {
	d := h.d
	name := strings.ReplaceAll(req.Name, "\\", "/")
	full, err := safeJoin(d.Cfg.IncomingDir, name)
	if err != nil || name == "" || strings.HasSuffix(name, "/") || req.Size < 0 {
		return errOut("bad file name"), 400
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return errOut(err.Error()), 500
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	full = uniquePath(full)
	tmp := full + ".part"
	os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o644)
	if err == nil {
		err = f.Truncate(req.Size)
	}
	if err != nil {
		if f != nil {
			f.Close()
			os.Remove(tmp)
		}
		return errOut(err.Error()), 500
	}
	id, _ := randB64(12)
	t := d.transfers.Start("receive", s.Name, name, req.Size)
	t.Dest = full
	h.uploads[id] = &phoneUpload{sess: s, f: f, tmp: tmp, full: full, size: req.Size, t: t, last: time.Now()}
	return map[string]any{"id": id, "chunk": phoneChunk}, 200
}

func (h *phoneHub) upChunk(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	h.mu.Lock()
	u := h.uploads[q.Get("id")]
	h.mu.Unlock()
	s := h.session(q.Get("s"))
	if u == nil || s == nil || u.sess != s {
		h.tooManyFails(clientIP(r), true)
		http.Error(w, "unknown upload", http.StatusNotFound)
		return
	}
	off, err := strconv.ParseInt(q.Get("off"), 10, 64)
	if err != nil || off < 0 || off >= max(u.size, 1) {
		http.Error(w, "bad offset", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, phoneChunk+64))
	if err != nil {
		http.Error(w, "read failed", http.StatusBadRequest)
		return
	}
	plain, err := open(s.aead, "up|"+q.Get("id")+"|"+strconv.FormatInt(off, 10), body)
	if err != nil || off+int64(len(plain)) > u.size {
		http.Error(w, "chunk rejected", http.StatusBadRequest)
		return
	}
	if _, err := u.f.WriteAt(plain, off); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	atomic.AddInt64(&u.written, int64(len(plain)))
	atomic.AddInt64(&u.t.cur, int64(len(plain)))
	h.touch()
	w.WriteHeader(http.StatusNoContent)
}

func (h *phoneHub) upFinish(s *phoneSession, id string, abort bool) (any, int) {
	h.mu.Lock()
	u := h.uploads[id]
	if u == nil || u.sess != s {
		h.mu.Unlock()
		return errOut("unknown upload"), 404
	}
	if !abort && atomic.LoadInt64(&u.written) < u.size {
		h.mu.Unlock()
		return errOut("upload is incomplete"), 409
	}
	delete(h.uploads, id)
	h.mu.Unlock()
	err := u.f.Close()
	if abort {
		os.Remove(u.tmp)
		h.d.transfers.Finish(u.t, errors.New("cancelled on the phone"))
		return map[string]bool{"ok": true}, 200
	}
	if err == nil {
		err = os.Rename(u.tmp, u.full)
	}
	atomic.StoreInt64(&u.t.cur, u.size)
	h.d.transfers.Finish(u.t, err)
	if err != nil {
		os.Remove(u.tmp)
		return errOut(err.Error()), 500
	}
	return map[string]any{"saved": filepath.Base(u.full)}, 200
}

// ---------- computer -> phone downloads ----------

func (h *phoneHub) makeToken(s *phoneSession, req *rpcReq) (any, int) {
	d := h.d
	tok := &dlToken{sess: s, expires: time.Now().Add(2 * time.Minute)}
	switch req.Kind {
	case "outbox":
		h.mu.Lock()
		for _, it := range h.outbox[s.ID] {
			if it.ID == req.ID {
				tok.path, tok.name, tok.size, tok.outID = it.path, it.Name, it.Size, it.ID
			}
		}
		h.mu.Unlock()
		if tok.path == "" {
			return errOut("this file is no longer offered"), 404
		}
	case "share", "thumb":
		sh, ok := h.phoneShare(req.Share)
		if !ok {
			return errOut("no such shared folder"), 404
		}
		full, err := safeJoin(sh.Path, req.Path)
		if err != nil {
			return errOut("bad path"), 400
		}
		fi, err := os.Stat(full)
		if err != nil || fi.IsDir() {
			return errOut("not a file"), 404
		}
		tok.path, tok.name, tok.size = full, fi.Name(), fi.Size()
		if req.Kind == "thumb" {
			tok.thumb = min(max(req.Thumb, 64), 512)
		}
	case "avatar":
		tok.path, tok.name, tok.avatar = d.avatarPath(), "avatar.jpg", true
		if fi, err := os.Stat(tok.path); err == nil {
			tok.size = fi.Size()
		} else {
			return errOut("no picture"), 404
		}
	default:
		return errOut("unknown kind"), 400
	}
	id, _ := randB64(18)
	h.mu.Lock()
	h.tokens[id] = tok
	h.mu.Unlock()
	return map[string]any{"token": id, "name": tok.name, "size": tok.size}, 200
}

// down streams a file as encrypted frames: [u32 length][nonce||ciphertext].
// Frame i is bound to "dn|token|i"; an empty final frame bound to
// "dn|token|end|i" marks a complete download, so truncation is detectable.
func (h *phoneHub) down(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("token")
	h.mu.Lock()
	tok := h.tokens[id]
	delete(h.tokens, id) // single use
	h.mu.Unlock()
	if tok == nil || time.Now().After(tok.expires) {
		h.tooManyFails(clientIP(r), true)
		http.Error(w, "expired", http.StatusNotFound)
		return
	}
	var src io.Reader
	var size int64
	if tok.thumb > 0 {
		rec := &memWriter{h: http.Header{}}
		if !writeThumb(rec, tok.path, tok.thumb) {
			http.Error(w, "no thumbnail", http.StatusUnsupportedMediaType)
			return
		}
		src, size = bytes.NewReader(rec.buf.Bytes()), int64(rec.buf.Len())
	} else {
		f, err := os.Open(tok.path)
		if err != nil {
			http.Error(w, "cannot open", http.StatusNotFound)
			return
		}
		defer f.Close()
		src, size = f, tok.size
	}
	var t *Transfer
	if tok.thumb == 0 && !tok.avatar {
		t = h.d.transfers.Start("send", tok.sess.Name, tok.name, size)
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Size", strconv.FormatInt(size, 10))
	buf := make([]byte, phoneFrame)
	var hdr [4]byte
	var err error
	for i := 0; ; i++ {
		n, rerr := io.ReadFull(src, buf)
		if n > 0 {
			frame := seal(tok.sess.aead, "dn|"+id+"|"+strconv.Itoa(i), buf[:n])
			binary.BigEndian.PutUint32(hdr[:], uint32(len(frame)))
			if _, err = w.Write(hdr[:]); err == nil {
				_, err = w.Write(frame)
			}
			if err != nil {
				break
			}
			if t != nil {
				atomic.AddInt64(&t.cur, int64(n))
			}
			h.touch()
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			end := seal(tok.sess.aead, "dn|"+id+"|end|"+strconv.Itoa(i+boolInt(n > 0)), nil)
			binary.BigEndian.PutUint32(hdr[:], uint32(len(end)))
			w.Write(hdr[:])
			w.Write(end)
			break
		}
		if rerr != nil {
			err = rerr
			break
		}
	}
	if t != nil {
		h.d.transfers.Finish(t, err)
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

type memWriter struct {
	h   http.Header
	buf bytes.Buffer
}

func (m *memWriter) Header() http.Header         { return m.h }
func (m *memWriter) Write(b []byte) (int, error) { return m.buf.Write(b) }
func (m *memWriter) WriteHeader(int)             {}

// ---------- desktop (loopback) API ----------

type PhoneView struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Online     bool      `json:"online"`
	Remembered bool      `json:"remembered"`
	LastSeen   time.Time `json:"last_seen,omitempty"`
	Outbox     []outItem `json:"outbox"`
}

type PhoneState struct {
	Running bool        `json:"running"`
	Port    int         `json:"port"`
	IdleIn  int         `json:"idle_in"` // seconds until auto-close
	Phones  []PhoneView `json:"phones"`
}

func (h *phoneHub) State() PhoneState {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := PhoneState{Running: h.srv != nil, Port: h.port, Phones: []PhoneView{}}
	if st.Running {
		st.IdleIn = int((phoneIdle - time.Since(h.lastActivity)).Seconds())
	}
	seen := map[string]bool{}
	for _, s := range h.sessions {
		if s.LastSeen.IsZero() && !s.Remember {
			continue // QR code not scanned yet
		}
		pv := PhoneView{ID: s.ID, Name: s.Name, Remembered: s.Remember, LastSeen: s.LastSeen,
			Online: st.Running && time.Since(s.LastSeen) < phoneOnline, Outbox: []outItem{}}
		for _, it := range h.outbox[s.ID] {
			pv.Outbox = append(pv.Outbox, *it)
		}
		st.Phones = append(st.Phones, pv)
		seen[s.ID] = true
	}
	h.d.Cfg.View(func(c *config.Config) {
		for _, p := range c.Phones {
			if !seen[p.ID] {
				st.Phones = append(st.Phones, PhoneView{ID: p.ID, Name: p.Name, Remembered: true, Outbox: []outItem{}})
			}
		}
	})
	sort.Slice(st.Phones, func(i, j int) bool { return st.Phones[i].Name < st.Phones[j].Name })
	return st
}

func (d *Daemon) lPhoneStart(w http.ResponseWriter, r *http.Request) {
	url, alts, err := d.phones.Start()
	if err != nil {
		fail(w, err, 500)
		return
	}
	png, err := QRPNG(url)
	if err != nil {
		fail(w, err, 500)
		return
	}
	writeJSON(w, map[string]any{"url": url, "alts": alts,
		"qr": "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), "state": d.phones.State()})
}

func (d *Daemon) lPhoneStop(w http.ResponseWriter, r *http.Request) {
	d.phones.Stop("closed on this computer")
	writeJSON(w, d.phones.State())
}

func (d *Daemon) lPhoneForget(w http.ResponseWriter, r *http.Request) {
	var in struct{ ID string }
	decode(r, &in)
	h := d.phones
	h.mu.Lock()
	delete(h.sessions, in.ID)
	for _, it := range h.outbox[in.ID] {
		if it.temp {
			os.Remove(it.path)
		}
	}
	delete(h.outbox, in.ID)
	h.mu.Unlock()
	d.Cfg.Update(func(c *config.Config) {
		out := c.Phones[:0]
		for _, p := range c.Phones {
			if p.ID != in.ID {
				out = append(out, p)
			}
		}
		c.Phones = out
	})
	writeJSON(w, d.phones.State())
}

func (h *phoneHub) outboxDir() string {
	dir := filepath.Join(h.d.Dir, "phone-outbox")
	os.MkdirAll(dir, 0o700)
	return dir
}

func (h *phoneHub) offer(phoneID string, it *outItem) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.sessions[phoneID]; !ok {
		return errors.New("this phone is not connected; open the phone link first")
	}
	h.outbox[phoneID] = append(h.outbox[phoneID], it)
	h.lastActivity = time.Now()
	return nil
}

// lPhoneSend accepts files from the desktop browser and offers them to a phone.
func (d *Daemon) lPhoneSend(w http.ResponseWriter, r *http.Request) {
	phoneID := r.URL.Query().Get("phone")
	if d.phones.session(phoneID) == nil {
		fail(w, errors.New("this phone is not connected; open the phone link first"), 400)
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		fail(w, err, 400)
		return
	}
	var added []outItem
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			fail(w, err, 400)
			return
		}
		name := ""
		if _, params, err := mime.ParseMediaType(part.Header.Get("Content-Disposition")); err == nil {
			name = path.Base(strings.ReplaceAll(params["filename"], "\\", "/"))
		}
		if name == "" || name == "." || name == "/" {
			continue
		}
		id, _ := randB64(9)
		dst := filepath.Join(d.phones.outboxDir(), id)
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			fail(w, err, 500)
			return
		}
		n, err := copyBuf(f, part)
		f.Close()
		if err != nil {
			os.Remove(dst)
			fail(w, err, 500)
			return
		}
		it := &outItem{ID: id, Name: name, Size: n, path: dst, temp: true, Added: time.Now()}
		if err := d.phones.offer(phoneID, it); err != nil {
			os.Remove(dst)
			fail(w, err, 400)
			return
		}
		added = append(added, *it)
	}
	writeJSON(w, added)
}

// lPhoneSendPath offers local files (CLI) without copying them.
func (d *Daemon) lPhoneSendPath(w http.ResponseWriter, r *http.Request) {
	var in struct{ Phone, Path string }
	decode(r, &in)
	fi, err := os.Stat(in.Path)
	if err != nil || fi.IsDir() {
		fail(w, errors.New("choose a file (folders are not supported for phones yet)"), 400)
		return
	}
	id, _ := randB64(9)
	it := &outItem{ID: id, Name: fi.Name(), Size: fi.Size(), path: in.Path, Added: time.Now()}
	if err := d.phones.offer(in.Phone, it); err != nil {
		fail(w, err, 400)
		return
	}
	writeJSON(w, it)
}

func (d *Daemon) lPhoneUnoffer(w http.ResponseWriter, r *http.Request) {
	var in struct{ Phone, ID string }
	decode(r, &in)
	h := d.phones
	h.mu.Lock()
	items := h.outbox[in.Phone]
	for i, it := range items {
		if it.ID == in.ID {
			if it.temp {
				os.Remove(it.path)
			}
			h.outbox[in.Phone] = append(items[:i:i], items[i+1:]...)
			break
		}
	}
	h.mu.Unlock()
	writeJSON(w, h.State())
}
