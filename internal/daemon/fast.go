package daemon

// Faster transfers: connection reuse, larger socket buffers, and large files
// sent as parallel chunks over several TLS connections. Several TCP flows
// recover from Wi-Fi packet loss far better than one.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Kumarsujay03/wharfdrop/internal/config"
)

const (
	chunkSize       = 8 << 20  // bytes per parallel chunk
	parallelMin     = 24 << 20 // files smaller than this use one stream
	parallelStreams = 4
	inboundIdle     = 10 * time.Minute
)

// ---------- connection reuse ----------

var clientCache sync.Map // fingerprint -> *http.Client

// peerHTTP returns a pinned client for a trusted peer, reusing its
// keep-alive connections across requests.
func (d *Daemon) peerHTTP(fp string) *http.Client {
	if c, ok := clientCache.Load(fp); ok {
		return c.(*http.Client)
	}
	c := d.client(fp, nil)
	if tr, ok := c.Transport.(*http.Transport); ok {
		tr.MaxIdleConns = 64
		tr.MaxIdleConnsPerHost = parallelStreams * 2
		tr.IdleConnTimeout = 90 * time.Second
	}
	actual, _ := clientCache.LoadOrStore(fp, c)
	return actual.(*http.Client)
}

func forgetClient(fp string) {
	if c, ok := clientCache.LoadAndDelete(fp); ok {
		c.(*http.Client).CloseIdleConnections()
	}
}

// ---------- server-side socket buffers ----------

type bufListener struct{ net.Listener }

func (l bufListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if tc, ok := c.(*net.TCPConn); ok {
		tc.SetReadBuffer(4 << 20)
		tc.SetWriteBuffer(4 << 20)
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(30 * time.Second)
	}
	return c, nil
}

// ---------- receiver: chunked uploads ----------

type inbound struct {
	mu       sync.Mutex
	peer     string
	f        *os.File
	tmp      string
	full     string
	size     int64
	written  int64 // bytes written (atomic)
	last     time.Time
	t        *Transfer
	finished bool
}

type inboundSet struct {
	mu sync.Mutex
	m  map[string]*inbound
}

var inbounds = inboundSet{m: map[string]*inbound{}}

func (d *Daemon) hRecvStart(w http.ResponseWriter, r *http.Request, p *config.Peer) {
	var in struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&in); err != nil || in.Size < 0 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	full, err := safeJoin(d.Cfg.IncomingDir, in.Name)
	if err != nil || in.Name == "" || strings.HasSuffix(in.Name, "/") {
		http.Error(w, "bad name", http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	inbounds.mu.Lock()
	full = uniquePath(full)
	tmp := full + ".part"
	if _, statErr := os.Stat(tmp); statErr == nil {
		busy := false
		for _, other := range inbounds.m {
			if other.tmp == tmp {
				busy = true
				break
			}
		}
		if busy {
			// another upload of the same name is running: use a new name
			full = uniquePath(strings.TrimSuffix(full, filepath.Ext(full)) + " (2)" + filepath.Ext(full))
			tmp = full + ".part"
		} else {
			os.Remove(tmp) // left over from an interrupted transfer
		}
	}
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o644)
	if err == nil {
		err = f.Truncate(in.Size)
	}
	if err != nil {
		inbounds.mu.Unlock()
		if f != nil {
			f.Close()
			os.Remove(tmp)
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id := newID()
	t := d.transfers.Start("receive", p.Name, in.Name, in.Size)
	t.Dest = full
	inbounds.m[id] = &inbound{peer: p.Fingerprint, f: f, tmp: tmp, full: full, size: in.Size, last: time.Now(), t: t}
	inbounds.mu.Unlock()
	writeJSON(w, map[string]any{"id": id, "chunk": chunkSize})
}

func lookupInbound(id, fp string) (*inbound, bool) {
	inbounds.mu.Lock()
	defer inbounds.mu.Unlock()
	in, ok := inbounds.m[id]
	if !ok || in.peer != fp {
		return nil, false
	}
	in.last = time.Now()
	return in, true
}

func (d *Daemon) hRecvChunk(w http.ResponseWriter, r *http.Request, p *config.Peer) {
	in, ok := lookupInbound(r.URL.Query().Get("id"), p.Fingerprint)
	if !ok {
		http.Error(w, "unknown upload", http.StatusNotFound)
		return
	}
	off, err := strconv.ParseInt(r.URL.Query().Get("off"), 10, 64)
	if err != nil || off < 0 || off > in.size {
		http.Error(w, "bad offset", http.StatusBadRequest)
		return
	}
	limit := in.size - off
	if limit > chunkSize*4 {
		limit = chunkSize * 4
	}
	buf := make([]byte, 1<<20)
	var n int64
	body := io.LimitReader(r.Body, limit)
	for {
		k, rerr := body.Read(buf)
		if k > 0 {
			if _, werr := in.f.WriteAt(buf[:k], off+n); werr != nil {
				http.Error(w, werr.Error(), http.StatusInternalServerError)
				return
			}
			n += int64(k)
			atomic.AddInt64(&in.t.cur, int64(k))
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			// The sender retries the whole chunk; undo the progress count.
			atomic.AddInt64(&in.t.cur, -n)
			http.Error(w, rerr.Error(), http.StatusBadRequest)
			return
		}
	}
	atomic.AddInt64(&in.written, n)
	writeJSON(w, map[string]int64{"n": n})
}

func (d *Daemon) hRecvFinish(w http.ResponseWriter, r *http.Request, p *config.Peer) {
	id := r.URL.Query().Get("id")
	in, ok := lookupInbound(id, p.Fingerprint)
	if !ok {
		http.Error(w, "unknown upload", http.StatusNotFound)
		return
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	if r.URL.Query().Get("abort") == "1" {
		d.dropInbound(id, in, errors.New("cancelled by sender"))
		writeJSON(w, map[string]bool{"ok": true})
		return
	}
	if got := atomic.LoadInt64(&in.written); got < in.size {
		http.Error(w, fmt.Sprintf("incomplete: %d of %d bytes", got, in.size), http.StatusConflict)
		return
	}
	err := in.f.Close()
	if err == nil {
		err = os.Rename(in.tmp, in.full)
	}
	inbounds.mu.Lock()
	delete(inbounds.m, id)
	inbounds.mu.Unlock()
	atomic.StoreInt64(&in.t.cur, in.size)
	d.transfers.Finish(in.t, err)
	if err != nil {
		os.Remove(in.tmp)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"saved": in.full})
}

func (d *Daemon) dropInbound(id string, in *inbound, why error) {
	inbounds.mu.Lock()
	delete(inbounds.m, id)
	inbounds.mu.Unlock()
	in.f.Close()
	os.Remove(in.tmp)
	d.transfers.Finish(in.t, why)
}

// expireInbound removes uploads whose sender went away.
func (d *Daemon) expireInbound(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		inbounds.mu.Lock()
		var stale []string
		for id, in := range inbounds.m {
			if time.Since(in.last) > inboundIdle {
				stale = append(stale, id)
			}
		}
		inbounds.mu.Unlock()
		for _, id := range stale {
			if in, ok := inbounds.m[id]; ok {
				d.dropInbound(id, in, errors.New("sender stopped responding"))
			}
		}
	}
}

// ---------- sender ----------

var errNoParallel = errors.New("peer does not support parallel transfers")

// startUpload asks the peer to accept a chunked upload.
func (pc *peerClient) startUpload(ctx context.Context, name string, size int64) (string, error) {
	body, _ := json.Marshal(map[string]any{"name": name, "size": size})
	req, _ := http.NewRequestWithContext(ctx, "POST", pc.d.peerURL(pc.addr, "/recv/start", nil), strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := pc.c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return "", errNoParallel
	}
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var out struct{ ID string }
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.ID == "" {
		return "", errors.New("bad reply from peer")
	}
	return out.ID, nil
}

// putChunk sends one chunk; body must yield exactly length bytes.
func (pc *peerClient) putChunk(ctx context.Context, id string, off int64, body io.Reader, length int64) error {
	q := url.Values{"id": {id}, "off": {strconv.FormatInt(off, 10)}}
	req, _ := http.NewRequestWithContext(ctx, "PUT", pc.d.peerURL(pc.addr, "/recv/chunk", q), body)
	req.ContentLength = length
	resp, err := pc.c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("chunk at %d: %s", off, resp.Status)
	}
	return nil
}

func (pc *peerClient) finishUpload(ctx context.Context, id string, abort bool) error {
	q := url.Values{"id": {id}}
	if abort {
		q.Set("abort", "1")
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", pc.d.peerURL(pc.addr, "/recv/finish", q), nil)
	resp, err := pc.c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

// SendFile sends a local file, in parallel chunks when it is large enough
// and the peer supports it.
func (pc *peerClient) SendFile(ctx context.Context, name, path string) (*Transfer, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.Size() < parallelMin {
		return pc.SendStream(ctx, name, fi.Size(), f)
	}
	id, err := pc.startUpload(ctx, name, fi.Size())
	if errors.Is(err, errNoParallel) {
		return pc.SendStream(ctx, name, fi.Size(), f)
	}
	pname, _ := pc.d.Cfg.Trusted(pc.fp)
	t := pc.d.transfers.Start("send", pname.Name, name, fi.Size())
	t.Streams = parallelStreams
	if err != nil {
		pc.d.transfers.Finish(t, err)
		return t, err
	}
	err = pc.sendChunks(ctx, id, fi.Size(), t, func(off, n int64) io.Reader { return io.NewSectionReader(f, off, n) })
	if err == nil {
		err = pc.finishUpload(ctx, id, false)
	} else {
		pc.finishUpload(context.Background(), id, true)
	}
	pc.d.transfers.Finish(t, err)
	return t, err
}

// sendChunks uploads [0,size) with parallelStreams workers. Each chunk is
// retried up to three times.
func (pc *peerClient) sendChunks(ctx context.Context, id string, size int64, t *Transfer, open func(off, n int64) io.Reader) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var next int64
	var firstErr error
	var once sync.Once
	var wg sync.WaitGroup
	for w := 0; w < parallelStreams; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				off := atomic.AddInt64(&next, chunkSize) - chunkSize
				if off >= size || ctx.Err() != nil {
					return
				}
				n := min(int64(chunkSize), size-off)
				var err error
				for attempt := 0; attempt < 3; attempt++ {
					cr := &countingReader{r: open(off, n), t: t}
					err = pc.putChunk(ctx, id, off, cr, n)
					if err == nil {
						break
					}
					atomic.AddInt64(&t.cur, -cr.n)
					if ctx.Err() != nil {
						break
					}
					time.Sleep(time.Duration(attempt+1) * 300 * time.Millisecond)
				}
				if err != nil {
					once.Do(func() { firstErr = err; cancel() })
					return
				}
			}
		}()
	}
	wg.Wait()
	return firstErr
}

// ---------- browser uploads in parallel chunks ----------

type outbound struct {
	pc   *peerClient
	id   string
	t    *Transfer
	size int64
}

var outbounds sync.Map // local id -> *outbound

func (d *Daemon) lSendStart(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Peer, Name string
		Size       int64
	}
	if err := decode(r, &in); err != nil || in.Name == "" || in.Size < 0 {
		fail(w, errors.New("peer, name and size required"), 400)
		return
	}
	pc, err := d.peer(in.Peer)
	if err != nil {
		fail(w, err, 400)
		return
	}
	id, err := pc.startUpload(r.Context(), in.Name, in.Size)
	if errors.Is(err, errNoParallel) {
		fail(w, err, 409)
		return
	}
	if err != nil {
		fail(w, err, 502)
		return
	}
	pname, _ := d.Cfg.Trusted(pc.fp)
	t := d.transfers.Start("send", pname.Name, in.Name, in.Size)
	t.Streams = parallelStreams
	lid := newID()
	outbounds.Store(lid, &outbound{pc: pc, id: id, t: t, size: in.Size})
	writeJSON(w, map[string]any{"id": lid, "chunk": chunkSize, "streams": parallelStreams})
}

func (d *Daemon) lSendChunk(w http.ResponseWriter, r *http.Request) {
	v, ok := outbounds.Load(r.URL.Query().Get("id"))
	if !ok {
		fail(w, errors.New("unknown upload"), 404)
		return
	}
	ob := v.(*outbound)
	off, err := strconv.ParseInt(r.URL.Query().Get("off"), 10, 64)
	if err != nil || off < 0 || off > ob.size || r.ContentLength < 0 || off+r.ContentLength > ob.size {
		fail(w, errors.New("bad offset or length"), 400)
		return
	}
	cr := &countingReader{r: r.Body, t: ob.t}
	if err := ob.pc.putChunk(r.Context(), ob.id, off, cr, r.ContentLength); err != nil {
		atomic.AddInt64(&ob.t.cur, -cr.n)
		fail(w, err, 502)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (d *Daemon) lSendFinish(w http.ResponseWriter, r *http.Request) {
	lid := r.URL.Query().Get("id")
	v, ok := outbounds.LoadAndDelete(lid)
	if !ok {
		fail(w, errors.New("unknown upload"), 404)
		return
	}
	ob := v.(*outbound)
	abort := r.URL.Query().Get("abort") == "1"
	err := ob.pc.finishUpload(r.Context(), ob.id, abort)
	if abort && err == nil {
		err = errors.New("cancelled")
	}
	d.transfers.Finish(ob.t, err)
	if err != nil && !abort {
		fail(w, err, 502)
		return
	}
	writeJSON(w, ob.t.snapshot())
}

// ensure tls is referenced for older toolchains' unused-import checks
var _ = tls.VersionTLS13
