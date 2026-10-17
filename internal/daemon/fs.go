package daemon

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

type Entry struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	Dir     bool      `json:"dir"`
	ModTime time.Time `json:"mtime"`
}

var errBadPath = errors.New("invalid path")

// safeJoin resolves a slash-separated relative path inside root and refuses
// anything that would escape it.
func safeJoin(root, rel string) (string, error) {
	rel = strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(rel, "\\", "/")), "/")
	for _, part := range strings.Split(rel, "/") {
		if part == ".." {
			return "", errBadPath
		}
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	full := filepath.Join(rootAbs, filepath.FromSlash(rel))
	if full != rootAbs && !strings.HasPrefix(full, rootAbs+string(filepath.Separator)) {
		return "", errBadPath
	}
	return full, nil
}

func listDir(full string) ([]Entry, error) {
	des, err := os.ReadDir(full)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(des))
	for _, de := range des {
		info, err := de.Info()
		if err != nil {
			continue
		}
		e := Entry{Name: de.Name(), Dir: de.IsDir(), ModTime: info.ModTime()}
		if !e.Dir {
			e.Size = info.Size()
		}
		out = append(out, e)
	}
	return out, nil
}

// zipDir streams a directory as a zip archive (store only, no compression,
// since the link is the bottleneck and most media is already compressed).
func zipDir(w io.Writer, root string) error {
	zw := zip.NewWriter(w)
	base := filepath.Base(root)
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		name := path.Join(base, filepath.ToSlash(rel))
		if info.IsDir() {
			if rel == "." {
				return nil
			}
			_, err := zw.Create(name + "/")
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		hdr, _ := zip.FileInfoHeader(info)
		hdr.Name = name
		hdr.Method = zip.Store
		fw, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = copyBuf(fw, f)
		return err
	})
	if err != nil {
		return err
	}
	return zw.Close()
}

// uniquePath returns p, or p with " (n)" inserted before the extension if it exists.
func uniquePath(p string) string {
	if _, err := os.Stat(p); err != nil {
		return p
	}
	ext := filepath.Ext(p)
	stem := strings.TrimSuffix(p, ext)
	for i := 1; ; i++ {
		c := fmt.Sprintf("%s (%d)%s", stem, i, ext)
		if _, err := os.Stat(c); err != nil {
			return c
		}
	}
}
