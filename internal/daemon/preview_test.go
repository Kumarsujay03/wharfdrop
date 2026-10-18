package daemon

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreviewHeadersNeverRenderActiveContent(t *testing.T) {
	for _, name := range []string{"evil.html", "evil.htm", "evil.svg", "evil.xml", "evil.js", "notes.md", "noext"} {
		h := http.Header{}
		previewHeaders(h, name)
		if ct := h.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
			t.Errorf("%s: Content-Type %q, want text/plain", name, ct)
		}
		if !strings.HasPrefix(h.Get("Content-Security-Policy"), "sandbox") {
			t.Errorf("%s: missing sandbox CSP", name)
		}
		if h.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: missing nosniff", name)
		}
	}
	h := http.Header{}
	previewHeaders(h, "clip.MP4")
	if h.Get("Content-Type") != "video/mp4" || !strings.HasPrefix(h.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("video headers wrong: %v", h)
	}
	h = http.Header{}
	previewHeaders(h, "doc.pdf")
	if h.Get("Content-Type") != "application/pdf" || !strings.Contains(h.Get("Content-Security-Policy"), "script-src 'none'") {
		t.Fatalf("pdf headers wrong: %v", h)
	}
}

func TestDownscaleKeepsAspect(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 1800, 1200))
	for i := range src.Pix {
		src.Pix[i] = 200
	}
	got := downscale(src, 360).Bounds()
	if got.Dx() != 360 || got.Dy() != 240 {
		t.Fatalf("got %dx%d, want 360x240", got.Dx(), got.Dy())
	}
	small := image.NewRGBA(image.Rect(0, 0, 100, 50))
	if downscale(small, 360) != image.Image(small) {
		t.Fatal("small images must not be upscaled")
	}
}

func TestThumbnailEndpointAndPreviewNotTracked(t *testing.T) {
	a, _ := startNode(t, "alpha")
	b, bAddr := startNode(t, "beta")
	pr, err := a.StartPairing(bAddr)
	if err != nil {
		t.Fatal(err)
	}
	b.RespondIncoming(pr.ID, true)
	a.ConfirmOutgoing(pr.ID)
	waitFor(t, "pairing", func() bool { _, ok := a.Cfg.Trusted(b.ID.Fingerprint); return ok })

	dir := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 1000, 500))
	for x := 0; x < 1000; x++ {
		for y := 0; y < 500; y++ {
			img.Set(x, y, color.RGBA{uint8(x), 128, uint8(y), 255})
		}
	}
	f, _ := os.Create(filepath.Join(dir, "photo.png"))
	png.Encode(f, img)
	f.Close()
	os.WriteFile(filepath.Join(dir, "page.html"), []byte("<script>alert(1)</script>"), 0o644)
	b.Cfg.Update(func(c *configT) { c.Shares = append(c.Shares, &shareT{Name: "s", Path: dir, Peers: []string{"*"}}) })

	pc, _ := a.peer(b.ID.Fingerprint)
	ctx := context.Background()
	resp, err := pc.OpenRaw(ctx, "s", "photo.png", "", true, 200)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	th, err := jpeg.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("thumbnail is not a JPEG: %v", err)
	}
	if th.Bounds().Dx() != 200 || th.Bounds().Dy() != 100 {
		t.Fatalf("thumbnail is %v", th.Bounds())
	}
	if _, err := pc.OpenRaw(ctx, "s", "page.html", "", true, 200); err == nil {
		t.Fatal("thumbnail of a non-image must fail")
	}

	// Range request for seeking, through the loopback proxy, as the browser would.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/peer/get?inline=1&share=s&path=page.html&peer="+b.ID.Fingerprint, nil)
	req.Header.Set("Range", "bytes=0-7")
	a.lPeerGet(rec, req)
	if rec.Code != 206 || rec.Body.String() != "<script>" {
		t.Fatalf("range preview: code %d body %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("html preview served as %q", rec.Header().Get("Content-Type"))
	}
	if n := len(a.transfers.List()) + len(b.transfers.List()); n != 0 {
		t.Fatalf("previews created %d transfer entries", n)
	}
}
