// Package discovery announces this device on the LAN and listens for others.
// It uses UDP multicast plus limited broadcast so that devices on the 2.4 GHz
// and 5 GHz bands of the same router (same L2 network) see each other.
package discovery

import (
	"encoding/json"
	"log"
	"net"
	"sort"
	"sync"
	"time"
)

const (
	group   = "239.255.70.70"
	port    = 47070
	version = 1
)

type Beacon struct {
	V    int    `json:"v"`
	Name string `json:"name"`
	FP   string `json:"fp"`
	Port int    `json:"port"`
}

type Found struct {
	Name     string    `json:"name"`
	FP       string    `json:"fp"`
	Addr     string    `json:"addr"` // host:port of the peer API
	LastSeen time.Time `json:"last_seen"`
}

type Discovery struct {
	self  func() Beacon
	mu    sync.Mutex
	found map[string]*Found
	quiet map[string]time.Time // fingerprint -> ignore its beacons until
	stop  chan struct{}
}

func New(self func() Beacon) *Discovery {
	return &Discovery{self: self, found: map[string]*Found{}, quiet: map[string]time.Time{}, stop: make(chan struct{})}
}

func (d *Discovery) Start() {
	go d.listen()
	go d.announce()
}

func (d *Discovery) Stop() { close(d.stop) }

// Peers returns peers seen in the last 15 seconds.
func (d *Discovery) Peers() []Found {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []Found
	for fp, f := range d.found {
		if time.Since(f.LastSeen) > 15*time.Second {
			delete(d.found, fp)
			continue
		}
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Forget drops a peer that said goodbye and ignores its beacons briefly, so a
// beacon still in flight does not bring it back.
func (d *Discovery) Forget(fp string) {
	d.mu.Lock()
	delete(d.found, fp)
	d.quiet[fp] = time.Now().Add(4 * time.Second)
	d.mu.Unlock()
}

func (d *Discovery) Lookup(fp string) (Found, bool) {
	for _, p := range d.Peers() {
		if p.FP == fp {
			return p, true
		}
	}
	return Found{}, false
}

func (d *Discovery) announce() {
	targets := []*net.UDPAddr{
		{IP: net.ParseIP(group), Port: port},
		{IP: net.IPv4bcast, Port: port},
		{IP: net.IPv4(127, 0, 0, 1), Port: port}, // several nodes on one machine (testing)
	}
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		b, _ := json.Marshal(d.self())
		for _, tgt := range targets {
			c, err := net.DialUDP("udp4", nil, tgt)
			if err != nil {
				continue
			}
			c.Write(b)
			c.Close()
		}
		select {
		case <-t.C:
		case <-d.stop:
			return
		}
	}
}

func (d *Discovery) listen() {
	conn, err := net.ListenMulticastUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP(group), Port: port})
	if err != nil {
		log.Printf("discovery: listen failed: %v (peers can still be added by IP)", err)
		return
	}
	defer conn.Close()
	conn.SetReadBuffer(1 << 16)
	buf := make([]byte, 2048)
	myFP := d.self().FP
	for {
		select {
		case <-d.stop:
			return
		default:
		}
		conn.SetReadDeadline(time.Now().Add(time.Second))
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		var b Beacon
		if json.Unmarshal(buf[:n], &b) != nil || b.V != version || b.FP == "" || b.FP == myFP {
			continue
		}
		addr := net.JoinHostPort(src.IP.String(), itoa(b.Port))
		d.mu.Lock()
		if until, ok := d.quiet[b.FP]; ok {
			if time.Now().Before(until) {
				d.mu.Unlock()
				continue
			}
			delete(d.quiet, b.FP)
		}
		d.found[b.FP] = &Found{Name: b.Name, FP: b.FP, Addr: addr, LastSeen: time.Now()}
		d.mu.Unlock()
	}
}

func itoa(i int) string {
	return fmtInt(i)
}

func fmtInt(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	n := len(b)
	for i > 0 {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
	}
	return string(b[n:])
}
