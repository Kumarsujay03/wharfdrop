package daemon

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// partJournal remembers unfinished ".part" files so they never outlive the
// transfer that created them: they are removed when the transfer fails or is
// cancelled, when Wharfdrop stops, and, after a crash or power cut, the next
// time it starts. The list is saved in the config folder.
type partJournal struct {
	path  string
	mu    sync.Mutex
	files map[string]bool
	dirty chan struct{}
}

func newPartJournal(dir string) *partJournal {
	j := &partJournal{path: filepath.Join(dir, "partial-files.json"), files: map[string]bool{}, dirty: make(chan struct{}, 1)}
	go j.flusher()
	return j
}

func (j *partJournal) add(p string) {
	j.mu.Lock()
	j.files[p] = true
	j.mu.Unlock()
	j.markDirty()
}

func (j *partJournal) forget(p string) {
	j.mu.Lock()
	delete(j.files, p)
	j.mu.Unlock()
	j.markDirty()
}

// drop deletes an unfinished file.
func (j *partJournal) drop(p string) {
	os.Remove(p)
	j.forget(p)
}

// keep turns a finished ".part" file into the real file.
func (j *partJournal) keep(tmp, full string) error {
	err := os.Rename(tmp, full)
	if err != nil {
		os.Remove(tmp)
	}
	j.forget(tmp)
	return err
}

func (j *partJournal) markDirty() {
	select {
	case j.dirty <- struct{}{}:
	default:
	}
}

// flusher saves the list at most a few times a second, so folders of many
// small files do not rewrite it for every file.
func (j *partJournal) flusher() {
	for range j.dirty {
		j.save()
		time.Sleep(300 * time.Millisecond)
	}
}

func (j *partJournal) save() {
	j.mu.Lock()
	list := make([]string, 0, len(j.files))
	for p := range j.files {
		list = append(list, p)
	}
	j.mu.Unlock()
	if len(list) == 0 {
		os.Remove(j.path)
		return
	}
	b, _ := json.Marshal(list)
	tmp := j.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, j.path)
	}
}

// cleanAll removes every unfinished file: the ones open now and the ones a
// previous run left behind. Only ".part" files are ever touched.
func (j *partJournal) cleanAll() int {
	var saved []string
	if b, err := os.ReadFile(j.path); err == nil {
		json.Unmarshal(b, &saved)
	}
	j.mu.Lock()
	for p := range j.files {
		saved = append(saved, p)
	}
	j.files = map[string]bool{}
	j.mu.Unlock()
	n := 0
	for _, p := range saved {
		if !strings.HasSuffix(p, ".part") {
			continue
		}
		if os.Remove(p) == nil {
			n++
		}
	}
	os.Remove(j.path)
	if n > 0 {
		log.Printf("removed %d unfinished file(s)", n)
	}
	return n
}
