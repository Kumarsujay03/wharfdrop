package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func pairedNodes(t *testing.T) (*Daemon, *Daemon) {
	a, _ := startNode(t, "alpha")
	b, bAddr := startNode(t, "beta")
	pr, err := a.StartPairing(bAddr)
	if err != nil {
		t.Fatal(err)
	}
	b.RespondIncoming(pr.ID, true)
	a.ConfirmOutgoing(pr.ID)
	waitFor(t, "pairing", func() bool { _, ok := a.Cfg.Trusted(b.ID.Fingerprint); return ok })
	return a, b
}

func TestParallelSendLargeFile(t *testing.T) {
	a, b := pairedNodes(t)
	src := filepath.Join(t.TempDir(), "big.bin")
	data := make([]byte, parallelMin+chunkSize*3+12345) // not a multiple of the chunk size
	rand.Read(data)
	os.WriteFile(src, data, 0o644)

	pc, _ := a.peer(b.ID.Fingerprint)
	tr, err := pc.SendFile(context.Background(), "big.bin", src)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Streams != parallelStreams {
		t.Fatalf("expected the parallel path, got %d streams", tr.Streams)
	}
	got, _ := os.ReadFile(filepath.Join(b.Cfg.IncomingDir, "big.bin"))
	if !bytes.Equal(got, data) {
		t.Fatal("received file differs")
	}
	if _, err := os.Stat(filepath.Join(b.Cfg.IncomingDir, "big.bin.part")); err == nil {
		t.Fatal(".part file left behind")
	}
	rx := b.transfers.List()
	if len(rx) != 1 || rx[0].Done != int64(len(data)) || rx[0].Error != "" {
		t.Fatalf("receiver transfer wrong: %+v", rx)
	}
}

func TestBrowserChunkedUpload(t *testing.T) {
	a, b := pairedNodes(t)
	data := make([]byte, 3*chunkSize+777)
	rand.Read(data)

	call := func(method, url string, body []byte) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, url, bytes.NewReader(body))
		req.Header.Set("X-Token", a.Token)
		a.localMux().ServeHTTP(rec, req)
		return rec
	}
	start, _ := json.Marshal(map[string]any{"peer": b.ID.Fingerprint, "name": "clip.mkv", "size": len(data)})
	rec := call("POST", "/api/send/start", start)
	if rec.Code != 200 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	var st struct{ ID string }
	json.NewDecoder(rec.Body).Decode(&st)
	// send chunks out of order, as parallel uploads would
	for _, off := range []int{2 * chunkSize, 0, 3 * chunkSize, chunkSize} {
		end := min(off+chunkSize, len(data))
		if r := call("PUT", "/api/send/chunk?id="+st.ID+"&off="+strconv.Itoa(off), data[off:end]); r.Code != 200 {
			t.Fatalf("chunk %d: %d %s", off, r.Code, r.Body)
		}
	}
	if r := call("PUT", "/api/send/chunk?id="+st.ID+"&off="+strconv.Itoa(len(data)), []byte("overflow")); r.Code == 200 {
		t.Fatal("a chunk past the end must be refused")
	}
	if r := call("POST", "/api/send/finish?id="+st.ID, nil); r.Code != 200 {
		t.Fatalf("finish: %d %s", r.Code, r.Body)
	}
	got, _ := os.ReadFile(filepath.Join(b.Cfg.IncomingDir, "clip.mkv"))
	if !bytes.Equal(got, data) {
		t.Fatal("received file differs")
	}
}

func TestIncompleteUploadIsRefused(t *testing.T) {
	a, b := pairedNodes(t)
	pc, _ := a.peer(b.ID.Fingerprint)
	ctx := context.Background()
	id, err := pc.startUpload(ctx, "half.bin", 2*chunkSize)
	if err != nil {
		t.Fatal(err)
	}
	pc.putChunk(ctx, id, 0, bytes.NewReader(make([]byte, chunkSize)), chunkSize)
	if err := pc.finishUpload(ctx, id, false); err == nil {
		t.Fatal("finishing a half-sent file must fail")
	}
	pc.finishUpload(ctx, id, true)
	if _, err := os.Stat(filepath.Join(b.Cfg.IncomingDir, "half.bin")); err == nil {
		t.Fatal("aborted upload must not appear")
	}
}

func TestAvatarRoundTrip(t *testing.T) {
	a, b := pairedNodes(t)
	img := image.NewRGBA(image.Rect(0, 0, 800, 500))
	for i := range img.Pix {
		img.Pix[i] = 180
	}
	img.Set(400, 250, color.RGBA{255, 0, 0, 255})
	var buf bytes.Buffer
	png.Encode(&buf, img)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/avatar", &buf)
	req.Header.Set("X-Token", b.Token)
	b.localMux().ServeHTTP(rec, req)
	if rec.Code != 200 || b.AvatarVersion() == 0 {
		t.Fatalf("set avatar: %d %s", rec.Code, rec.Body)
	}
	// alpha fetches beta's picture through its loopback API
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/peer/avatar?peer="+b.ID.Fingerprint, nil)
	req.Header.Set("X-Token", a.Token)
	a.localMux().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("peer avatar: %d", rec.Code)
	}
	cfg, err := jpeg.DecodeConfig(rec.Body)
	if err != nil || cfg.Width != avatarSize || cfg.Height != avatarSize {
		t.Fatalf("avatar is %dx%d (%v), want square %d", cfg.Width, cfg.Height, err, avatarSize)
	}
	// rejects non-images
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/api/avatar", bytes.NewReader([]byte("<svg onload=alert(1)>")))
	req.Header.Set("X-Token", b.Token)
	b.localMux().ServeHTTP(rec, req)
	if rec.Code == 200 {
		t.Fatal("non-image avatar must be refused")
	}
}

func TestRunningTransferHasNoFinishedField(t *testing.T) {
	ts := NewTransfers()
	tr := ts.Start("send", "p", "f", 10)
	b, _ := json.Marshal(ts.List()[0])
	if bytes.Contains(b, []byte(`"finished"`)) {
		t.Fatalf("running transfer must not carry finished: %s", b)
	}
	ts.Finish(tr, nil)
	b, _ = json.Marshal(ts.List()[0])
	if !bytes.Contains(b, []byte(`"finished"`)) {
		t.Fatalf("finished transfer must carry finished: %s", b)
	}
	var back Transfer
	if err := json.Unmarshal(b, &back); err != nil || back.Finished.IsZero() {
		t.Fatalf("round trip lost finished: %v", err)
	}
}

func TestResendAfterInterruptedTransfer(t *testing.T) {
	a, b := pairedNodes(t)
	// a stale partial file from a transfer that was cut off
	os.MkdirAll(b.Cfg.IncomingDir, 0o755)
	os.WriteFile(filepath.Join(b.Cfg.IncomingDir, "movie.mkv.part"), []byte("stale"), 0o644)
	src := filepath.Join(t.TempDir(), "movie.mkv")
	data := make([]byte, parallelMin+1000)
	rand.Read(data)
	os.WriteFile(src, data, 0o644)
	pc, _ := a.peer(b.ID.Fingerprint)
	if _, err := pc.SendFile(context.Background(), "movie.mkv", src); err != nil {
		t.Fatalf("resend after interruption failed: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(b.Cfg.IncomingDir, "movie.mkv"))
	if !bytes.Equal(got, data) {
		t.Fatal("received file differs")
	}
	if _, err := os.Stat(filepath.Join(b.Cfg.IncomingDir, "movie.mkv.part")); err == nil {
		t.Fatal("stale .part file should be gone")
	}
}
