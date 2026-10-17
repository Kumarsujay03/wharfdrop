package daemon

import (
	"io"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type Transfer struct {
	ID       string    `json:"id"`
	Kind     string    `json:"kind"` // send | receive | download
	Peer     string    `json:"peer"`
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Done     int64     `json:"done"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished,omitempty"`
	Error    string    `json:"error,omitempty"`
	Dest     string    `json:"dest,omitempty"`
	cur      int64     // progress counter; accessed atomically
}

func (t *Transfer) snapshot() Transfer {
	c := *t
	c.Done = atomic.LoadInt64(&t.cur)
	return c
}

// Rate returns bytes per second so far.
func (t *Transfer) Rate() float64 {
	el := time.Since(t.Started).Seconds()
	if !t.Finished.IsZero() {
		el = t.Finished.Sub(t.Started).Seconds()
	}
	if el <= 0 {
		return 0
	}
	return float64(atomic.LoadInt64(&t.cur)) / el
}

type Transfers struct {
	mu   sync.Mutex
	list map[string]*Transfer
}

func NewTransfers() *Transfers { return &Transfers{list: map[string]*Transfer{}} }

func (ts *Transfers) Start(kind, peer, name string, size int64) *Transfer {
	t := &Transfer{ID: newID(), Kind: kind, Peer: peer, Name: name, Size: size, Started: time.Now()}
	ts.mu.Lock()
	ts.list[t.ID] = t
	// keep the table bounded
	if len(ts.list) > 200 {
		var old []*Transfer
		for _, x := range ts.list {
			if !x.Finished.IsZero() {
				old = append(old, x)
			}
		}
		sort.Slice(old, func(i, j int) bool { return old[i].Finished.Before(old[j].Finished) })
		for i := 0; i < len(old)-100; i++ {
			delete(ts.list, old[i].ID)
		}
	}
	ts.mu.Unlock()
	return t
}

func (ts *Transfers) Finish(t *Transfer, err error) {
	ts.mu.Lock()
	t.Finished = time.Now()
	if err != nil {
		t.Error = err.Error()
	}
	ts.mu.Unlock()
}

func (ts *Transfers) Get(id string) (Transfer, bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	t, ok := ts.list[id]
	if !ok {
		return Transfer{}, false
	}
	return t.snapshot(), true
}

func (ts *Transfers) List() []Transfer {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	out := make([]Transfer, 0, len(ts.list))
	for _, t := range ts.list {
		out = append(out, t.snapshot())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	return out
}

// countingWriter / countingReader update a transfer's progress.
type countingWriter struct {
	w io.Writer
	t *Transfer
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	atomic.AddInt64(&c.t.cur, int64(n))
	return n, err
}

type countingReader struct {
	r io.Reader
	t *Transfer
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	atomic.AddInt64(&c.t.cur, int64(n))
	return n, err
}

// copyBuf copies with a large buffer; Wi-Fi throughput benefits from fewer syscalls.
func copyBuf(dst io.Writer, src io.Reader) (int64, error) {
	buf := make([]byte, 1<<20)
	return io.CopyBuffer(dst, src, buf)
}
