package daemon

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/Kumarsujay03/wharfdrop/internal/config"
)

const (
	avatarSize    = 256
	maxAvatarIn   = 20 << 20
	peerAvatarTTL = 5 * time.Minute
)

func (d *Daemon) avatarPath() string { return filepath.Join(d.Dir, "avatar.jpg") }

// AvatarVersion is the avatar's modification time, or 0 when there is none.
func (d *Daemon) AvatarVersion() int64 {
	if fi, err := os.Stat(d.avatarPath()); err == nil {
		return fi.ModTime().UnixNano() / int64(time.Millisecond)
	}
	return 0
}

// setAvatar decodes an uploaded picture, crops it to a centred square and
// stores a 256x256 JPEG.
func (d *Daemon) setAvatar(r io.Reader) error {
	src, _, err := image.Decode(io.LimitReader(r, maxAvatarIn))
	if err != nil {
		return errors.New("use a JPG, PNG or GIF picture")
	}
	b := src.Bounds()
	side := min(b.Dx(), b.Dy())
	if side < 16 {
		return errors.New("picture is too small")
	}
	x0 := b.Min.X + (b.Dx()-side)/2
	y0 := b.Min.Y + (b.Dy()-side)/2
	var sq image.Image = src
	if si, ok := src.(interface {
		SubImage(image.Rectangle) image.Image
	}); ok {
		sq = si.SubImage(image.Rect(x0, y0, x0+side, y0+side))
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, downscale(sq, avatarSize), &jpeg.Options{Quality: 88}); err != nil {
		return err
	}
	tmp := d.avatarPath() + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, d.avatarPath())
}

func serveJPEG(w http.ResponseWriter, b []byte, maxAge int) {
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.Header().Set("Cache-Control", "private, max-age="+strconv.Itoa(maxAge))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(b)
}

// hAvatar (peer API): a paired device fetches our picture.
func (d *Daemon) hAvatar(w http.ResponseWriter, r *http.Request, p *config.Peer) {
	b, err := os.ReadFile(d.avatarPath())
	if err != nil {
		http.NotFound(w, r)
		return
	}
	serveJPEG(w, b, 300)
}

func (d *Daemon) lAvatarGet(w http.ResponseWriter, r *http.Request) {
	b, err := os.ReadFile(d.avatarPath())
	if err != nil {
		http.NotFound(w, r)
		return
	}
	serveJPEG(w, b, 3600)
}

func (d *Daemon) lAvatarSet(w http.ResponseWriter, r *http.Request) {
	if err := d.setAvatar(r.Body); err != nil {
		fail(w, err, 400)
		return
	}
	writeJSON(w, map[string]int64{"version": d.AvatarVersion()})
}

func (d *Daemon) lAvatarDelete(w http.ResponseWriter, r *http.Request) {
	os.Remove(d.avatarPath())
	writeJSON(w, map[string]bool{"ok": true})
}

type cachedAvatar struct {
	b   []byte
	at  time.Time
	nok bool
}

var peerAvatars sync.Map // fp -> cachedAvatar

// lPeerAvatar returns a paired device's picture, cached for a few minutes.
func (d *Daemon) lPeerAvatar(w http.ResponseWriter, r *http.Request) {
	fp := r.URL.Query().Get("peer")
	if v, ok := peerAvatars.Load(fp); ok {
		c := v.(cachedAvatar)
		if time.Since(c.at) < peerAvatarTTL {
			if c.nok {
				http.NotFound(w, r)
			} else {
				serveJPEG(w, c.b, 300)
			}
			return
		}
	}
	pc, err := d.peer(fp)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	resp, err := pc.get(r.Context(), "/avatar", nil)
	if err != nil {
		peerAvatars.Store(fp, cachedAvatar{at: time.Now(), nok: true})
		http.NotFound(w, r)
		return
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if _, err := jpeg.DecodeConfig(bytes.NewReader(b)); err != nil {
		peerAvatars.Store(fp, cachedAvatar{at: time.Now(), nok: true})
		http.NotFound(w, r)
		return
	}
	peerAvatars.Store(fp, cachedAvatar{b: b, at: time.Now()})
	serveJPEG(w, b, 300)
}
