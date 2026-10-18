package daemon

import (
	"encoding/json"
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
	Rate     float64   `json:"rate"`          // current speed in bytes/s (smoothed)
	Avg      float64   `json:"avg"`           // average speed since start
	ETA      float64   `json:"eta,omitempty"` // seconds left, when size is known
	Streams  int       `json:"streams,omitempty"`
	cur      int64     // progress counter; accessed atomically
	samples  []sample  // recent progress samples (guarded by Transfers.mu)
}

type sample struct {
	at time.Time
	n  int64
}

const (
	sampleEvery = 250 * time.Millisecond
	speedWindow = 5 * time.Second  // shown speed: bytes moved in the last 5 s
	etaWindow   = 15 * time.Second // time left: steadier, over the last 15 s
)

// rateOver is the average speed over roughly the last w, from the samples.
func (t *Transfer) rateOver(now time.Time, done int64, w time.Duration) float64 {
	var from *sample
	for i := range t.samples {
		if now.Sub(t.samples[i].at) <= w {
			from = &t.samples[i]
			break
		}
	}
	if from == nil {
		return 0
	}
	// windows shorter than a second are too noisy to show
	if dt := now.Sub(from.at).Seconds(); dt >= 1 {
		return float64(done-from.n) / dt
	}
	if el := now.Sub(t.Started).Seconds(); el >= 1 {
		return float64(done) / el
	}
	return 0
}

// snapshot reports progress. Network writes go out in bursts (socket buffers
// fill, then wait for the Wi-Fi), so speed is measured over a few seconds
// rather than between two polls.
func (t *Transfer) snapshot() Transfer {
	now := time.Now()
	done := atomic.LoadInt64(&t.cur)
	t.ETA = 0
	if t.Finished.IsZero() {
		if len(t.samples) == 0 {
			t.samples = append(t.samples, sample{t.Started, 0})
		}
		if now.Sub(t.samples[len(t.samples)-1].at) >= sampleEvery {
			t.samples = append(t.samples, sample{now, done})
		}
		// keep a little more than the longest window
		for len(t.samples) > 2 && now.Sub(t.samples[1].at) > etaWindow+time.Second {
			t.samples = t.samples[1:]
		}
		t.Rate = t.rateOver(now, done, speedWindow)
		if r := t.rateOver(now, done, etaWindow); t.Size > 0 && r > 1 {
			t.ETA = float64(t.Size-done) / r
		}
	} else {
		t.Rate, t.samples = 0, nil
	}
	end := now
	if !t.Finished.IsZero() {
		end = t.Finished
	}
	if el := end.Sub(t.Started).Seconds(); el > 0 {
		t.Avg = float64(done) / el
	}
	c := *t
	c.Done = done
	c.samples = nil
	return c
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
	n int64 // bytes read through this reader
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	atomic.AddInt64(&c.t.cur, int64(n))
	c.n += int64(n)
	return n, err
}

// copyBuf copies with a large buffer; Wi-Fi throughput benefits from fewer syscalls.
func copyBuf(dst io.Writer, src io.Reader) (int64, error) {
	buf := make([]byte, 1<<20)
	return io.CopyBuffer(dst, src, buf)
}

// MarshalJSON leaves "finished" out while a transfer is running. A zero
// time.Time would otherwise be sent as "0001-01-01T00:00:00Z", which the web
// UI reads as finished.
func (t Transfer) MarshalJSON() ([]byte, error) {
	type plain Transfer
	var fin *time.Time
	if !t.Finished.IsZero() {
		f := t.Finished
		fin = &f
	}
	return json.Marshal(struct {
		plain
		Finished *time.Time `json:"finished,omitempty"`
	}{plain(t), fin})
}
