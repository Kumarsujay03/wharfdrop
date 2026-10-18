package daemon

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/gif" // register decoders for thumbnails
	"image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
)

const (
	maxThumbSource = 64 << 20 // do not decode images larger than this
	maxThumbSize   = 640
)

// writeThumb decodes a JPEG/PNG/GIF and writes a JPEG no larger than size px.
// It reports false if the file is not a decodable image.
func writeThumb(w http.ResponseWriter, full string, size int) bool {
	if size > maxThumbSize {
		size = maxThumbSize
	}
	f, err := os.Open(full)
	if err != nil {
		return false
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || fi.Size() > maxThumbSource {
		return false
	}
	src, _, err := image.Decode(io.LimitReader(f, maxThumbSource))
	if err != nil {
		return false
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, downscale(src, size), &jpeg.Options{Quality: 82}); err != nil {
		return false
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.Header().Set("Cache-Control", "private, max-age=600")
	w.Write(buf.Bytes())
	return true
}

// downscale fits src into size×size, averaging a 3×3 grid of samples per
// output pixel (good enough for thumbnails and fast without x/image).
func downscale(src image.Image, size int) image.Image {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= size && sh <= size {
		return src
	}
	dw, dh := size, size
	if sw > sh {
		dh = max(1, sh*size/sw)
	} else {
		dw = max(1, sw*size/sh)
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	const n = 3
	for y := 0; y < dh; y++ {
		for x := 0; x < dw; x++ {
			var r, g, bl, a uint32
			for sy := 0; sy < n; sy++ {
				for sx := 0; sx < n; sx++ {
					px := b.Min.X + (x*n+sx)*sw/(dw*n)
					py := b.Min.Y + (y*n+sy)*sh/(dh*n)
					cr, cg, cb, ca := src.At(px, py).RGBA()
					r, g, bl, a = r+cr, g+cg, bl+cb, a+ca
				}
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i+0] = uint8(r / (n * n) >> 8)
			dst.Pix[i+1] = uint8(g / (n * n) >> 8)
			dst.Pix[i+2] = uint8(bl / (n * n) >> 8)
			dst.Pix[i+3] = uint8(a / (n * n) >> 8)
		}
	}
	return dst
}

// Types the browser may render inline. Anything that can run script (HTML,
// SVG as a document, XML, JS) is shown as plain text instead.
var inlineSafe = map[string]string{
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".gif": "image/gif", ".webp": "image/webp",
	".bmp": "image/bmp", ".avif": "image/avif", ".ico": "image/x-icon",
	".mp4": "video/mp4", ".m4v": "video/mp4", ".webm": "video/webm", ".mov": "video/quicktime", ".mkv": "video/x-matroska", ".ogv": "video/ogg",
	".mp3": "audio/mpeg", ".m4a": "audio/mp4", ".aac": "audio/aac", ".wav": "audio/wav", ".ogg": "audio/ogg", ".oga": "audio/ogg", ".opus": "audio/ogg", ".flac": "audio/flac",
	".pdf": "application/pdf",
}

// previewHeaders makes an inline response safe to show at the UI's origin.
func previewHeaders(h http.Header, name string) {
	ext := strings.ToLower(path.Ext(name))
	if ct, ok := inlineSafe[ext]; ok {
		h.Set("Content-Type", ct)
	} else {
		h.Set("Content-Type", "text/plain; charset=utf-8")
	}
	h.Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": name}))
	h.Set("X-Content-Type-Options", "nosniff")
	if ext == ".pdf" {
		// The browser's PDF viewer does not run in a sandbox; scripts stay blocked.
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'none'; object-src 'self'; style-src 'unsafe-inline'; img-src 'self' data:")
	} else {
		h.Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src 'self' data:; media-src 'self'; style-src 'unsafe-inline'")
	}
}

// OpenRaw fetches a remote file, forwarding a Range header and preview options.
// It accepts 200 and 206 responses.
func (pc *peerClient) OpenRaw(ctx context.Context, share, p string, rangeHdr string, preview bool, thumb int) (*http.Response, error) {
	q := url.Values{"share": {share}, "path": {p}}
	if preview {
		q.Set("preview", "1")
	}
	if thumb > 0 {
		q.Set("thumb", strconv.Itoa(thumb))
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", pc.d.peerURL(pc.addr, "/get", q), nil)
	if rangeHdr != "" {
		req.Header.Set("Range", rangeHdr)
	}
	resp, err := pc.c.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 && resp.StatusCode != 206 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		resp.Body.Close()
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return resp, nil
}
