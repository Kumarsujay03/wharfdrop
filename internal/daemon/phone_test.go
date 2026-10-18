package daemon

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
)

// phoneClient is a Go stand-in for the phone page's JavaScript.
type phoneClient struct {
	t    *testing.T
	base string
	sid  string
	key  []byte
}

func phoneFromURL(t *testing.T, u string) *phoneClient {
	i := strings.Index(u, "#")
	frag := strings.SplitN(u[i+1:], ".", 2)
	key, _ := base64.RawURLEncoding.DecodeString(frag[1])
	base := strings.TrimSuffix(u[:i], "/")
	return &phoneClient{t: t, base: base, sid: frag[0], key: key}
}

func (c *phoneClient) aead() interface {
	Seal([]byte, []byte, []byte, []byte) []byte
} {
	a, _ := chacha20poly1305.NewX(c.key)
	return a
}

func (c *phoneClient) rpcRaw(body map[string]any) ([]byte, []byte) {
	a, _ := chacha20poly1305.NewX(c.key)
	if _, ok := body["t"]; !ok {
		body["t"] = time.Now().UnixMilli()
	}
	b, _ := json.Marshal(body)
	return seal(a, "rpc|"+c.sid, b), nil
}

func (c *phoneClient) post(sealed []byte) (*http.Response, map[string]any) {
	req, _ := http.NewRequest("POST", c.base+"/api/rpc", bytes.NewReader(sealed))
	req.Header.Set("X-Session", c.sid)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	a, _ := chacha20poly1305.NewX(c.key)
	plain, err := open(a, "rsp|"+c.sid, raw)
	if err != nil {
		return resp, nil
	}
	var out map[string]any
	json.Unmarshal(plain, &out)
	return resp, out
}

func (c *phoneClient) rpc(body map[string]any) map[string]any {
	sealed, _ := c.rpcRaw(body)
	resp, out := c.post(sealed)
	if resp.StatusCode != 200 {
		c.t.Fatalf("rpc %v: status %d %v", body["op"], resp.StatusCode, out)
	}
	return out
}

func (c *phoneClient) download(token string) ([]byte, error) {
	resp, err := http.Get(c.base + "/api/down/" + token)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	a, _ := chacha20poly1305.NewX(c.key)
	var out bytes.Buffer
	var hdr [4]byte
	for i := 0; ; i++ {
		if _, err := io.ReadFull(resp.Body, hdr[:]); err != nil {
			return nil, fmt.Errorf("cut off: %w", err)
		}
		frame := make([]byte, binary.BigEndian.Uint32(hdr[:]))
		if _, err := io.ReadFull(resp.Body, frame); err != nil {
			return nil, err
		}
		if len(frame) == 40 {
			if _, err := open(a, "dn|"+token+"|end|"+strconv.Itoa(i), frame); err != nil {
				return nil, err
			}
			return out.Bytes(), nil
		}
		p, err := open(a, "dn|"+token+"|"+strconv.Itoa(i), frame)
		if err != nil {
			return nil, err
		}
		out.Write(p)
	}
}

func startPhoneLink(t *testing.T) (*Daemon, *phoneClient) {
	d, _ := startNode(t, "computer")
	d.Cfg.Update(func(c *configT) { c.PhonePort = freePort(t) })
	urls, err := d.phones.Start()
	if err != nil {
		t.Fatal(err)
	}
	url := urls[0].URL
	t.Cleanup(func() { d.phones.Stop("test") })
	// test over loopback instead of the LAN address in the QR code
	pc := phoneFromURL(t, url)
	pc.base = "http://127.0.0.1:" + strconv.Itoa(d.phones.port)
	return d, pc
}

func TestPhoneUploadAndDownload(t *testing.T) {
	d, pc := startPhoneLink(t)
	st := pc.rpc(map[string]any{"op": "hello", "name": "Test phone"})
	if st["computer"] != "computer" {
		t.Fatalf("hello: %v", st)
	}
	// phone -> computer, two chunks, sent out of order
	data := make([]byte, phoneChunk+12345)
	rand.Read(data)
	up := pc.rpc(map[string]any{"op": "up-start", "name": "IMG_0001.HEIC", "size": len(data)})
	id := up["id"].(string)
	a, _ := chacha20poly1305.NewX(pc.key)
	for _, off := range []int{phoneChunk, 0} {
		end := min(off+phoneChunk, len(data))
		body := seal(a, "up|"+id+"|"+strconv.Itoa(off), data[off:end])
		req, _ := http.NewRequest("PUT", fmt.Sprintf("%s/api/up?s=%s&id=%s&off=%d", pc.base, pc.sid, id, off), bytes.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != 204 {
			t.Fatalf("chunk %d: %v %v", off, err, resp)
		}
	}
	// a chunk sealed for one offset is refused at another (no reordering)
	body := seal(a, "up|"+id+"|0", data[:10])
	req, _ := http.NewRequest("PUT", fmt.Sprintf("%s/api/up?s=%s&id=%s&off=%d", pc.base, pc.sid, id, 10), bytes.NewReader(body))
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode == 204 {
		t.Fatal("chunk replayed at a different offset was accepted")
	}
	pc.rpc(map[string]any{"op": "up-finish", "id": id})
	got, _ := os.ReadFile(filepath.Join(d.Cfg.IncomingDir, "IMG_0001.HEIC"))
	if !bytes.Equal(got, data) {
		t.Fatal("uploaded file differs")
	}

	// computer -> phone
	src := filepath.Join(t.TempDir(), "report.pdf")
	pdf := make([]byte, 3*phoneFrame+77)
	rand.Read(pdf)
	os.WriteFile(src, pdf, 0o644)
	if err := d.phones.offer(pc.sid, &outItem{ID: "x1", Name: "report.pdf", Size: int64(len(pdf)), path: src}); err != nil {
		t.Fatal(err)
	}
	st = pc.rpc(map[string]any{"op": "state"})
	if ob := st["outbox"].([]any); len(ob) != 1 {
		t.Fatalf("outbox: %v", st["outbox"])
	}
	g := pc.rpc(map[string]any{"op": "get", "kind": "outbox", "id": "x1"})
	tok := g["token"].(string)
	back, err := pc.download(tok)
	if err != nil || !bytes.Equal(back, pdf) {
		t.Fatalf("download: %v", err)
	}
	if _, err := pc.download(tok); err == nil {
		t.Fatal("a download token must work only once")
	}
	// empty file: only the end frame
	empty := filepath.Join(t.TempDir(), "empty.txt")
	os.WriteFile(empty, nil, 0o644)
	d.phones.offer(pc.sid, &outItem{ID: "x2", Name: "empty.txt", path: empty})
	g = pc.rpc(map[string]any{"op": "get", "kind": "outbox", "id": "x2"})
	if b, err := pc.download(g["token"].(string)); err != nil || len(b) != 0 {
		t.Fatalf("empty download: %v %d", err, len(b))
	}
}

func TestPhoneLinkRefusesStrangersAndReplays(t *testing.T) {
	_, pc := startPhoneLink(t)
	sealed, _ := pc.rpcRaw(map[string]any{"op": "hello", "name": "P"})
	if resp, _ := pc.post(sealed); resp.StatusCode != 200 {
		t.Fatalf("first request: %d", resp.StatusCode)
	}
	if resp, _ := pc.post(sealed); resp.StatusCode != 401 {
		t.Fatalf("replayed request must be refused, got %d", resp.StatusCode)
	}
	old, _ := pc.rpcRaw(map[string]any{"op": "state", "t": time.Now().Add(-10 * time.Minute).UnixMilli()})
	if resp, _ := pc.post(old); resp.StatusCode != 401 {
		t.Fatalf("stale request must be refused, got %d", resp.StatusCode)
	}
	stranger := &phoneClient{t: t, base: pc.base, sid: pc.sid, key: make([]byte, 32)}
	bad, _ := stranger.rpcRaw(map[string]any{"op": "state"})
	if resp, _ := stranger.post(bad); resp.StatusCode != 401 {
		t.Fatalf("wrong key must be refused, got %d", resp.StatusCode)
	}
	// the page itself is served with a strict content security policy
	resp, err := http.Get(pc.base + "/")
	if err != nil || resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatalf("phone page: %v %v", err, resp)
	}
	if r2, _ := http.Get(pc.base + "/../../etc/passwd"); r2.StatusCode == 200 {
		b, _ := io.ReadAll(r2.Body)
		if bytes.Contains(b, []byte("root:")) {
			t.Fatal("static file server escaped its folder")
		}
	}
}

func TestPhoneLinkClosesAndRemembers(t *testing.T) {
	d, pc := startPhoneLink(t)
	pc.rpc(map[string]any{"op": "hello", "name": "Kept", "remember": true})
	d.phones.Start() // a second QR code
	d.phones.Stop("test")
	if d.phones.running() {
		t.Fatal("phone link should be closed")
	}
	if _, err := http.Get(pc.base + "/"); err == nil {
		t.Fatal("port should be closed")
	}
	// the remembered phone reconnects when the link opens again
	d.phones.Start()
	pc.base = "http://127.0.0.1:" + strconv.Itoa(d.phones.port)
	st := pc.rpc(map[string]any{"op": "state"})
	if st["name"] != "Kept" {
		t.Fatalf("remembered phone lost its session: %v", st)
	}
	found := false
	for _, f := range d.phones.State().Phones {
		if f.Name == "Kept" && f.Remembered {
			found = true
		}
	}
	if !found {
		t.Fatal("remembered phone missing from state")
	}
}
