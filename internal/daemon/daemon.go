// Package daemon runs the Wharfdrop node: LAN discovery, the mutual-TLS peer
// API, the pairing state machine, and the loopback-only API used by the CLI
// and the web UI.
package daemon

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Kumarsujay03/wharfdrop/internal/config"
	"github.com/Kumarsujay03/wharfdrop/internal/discovery"
	"github.com/Kumarsujay03/wharfdrop/internal/identity"
)

// Version is overridden at release build time with -ldflags "-X ...daemon.Version=vX.Y.Z".
var Version = "0.1.0-dev"

// PairRequest tracks one pairing attempt, incoming or outgoing.
type PairRequest struct {
	ID       string    `json:"id"`
	PeerFP   string    `json:"peer_fp"`
	PeerName string    `json:"peer_name"`
	PeerAddr string    `json:"peer_addr"`
	Code     string    `json:"code"`
	Status   string    `json:"status"` // pending | accepted | denied | cancelled
	Local    bool      `json:"local_confirmed"`
	Remote   bool      `json:"remote_confirmed"`
	Created  time.Time `json:"created"`
	cancel   context.CancelFunc
}

type Daemon struct {
	Dir   string
	Cfg   *config.Config
	ID    *identity.Identity
	Disc  *discovery.Discovery
	Token string

	mu         sync.Mutex
	pendingIn  map[string]*PairRequest
	pendingOut map[string]*PairRequest
	transfers  *Transfers
	reachable  map[string]bool // trusted peers that answered a probe recently
	updMu      sync.Mutex
	upd        UpdateState
	restart    chan struct{}
	phones     *phoneHub
	shutdown   chan struct{}
	peerSrv    *http.Server
	localSrv   *http.Server
	ui         uiTracker
}

func New(dir string) (*Daemon, error) {
	cfg, err := config.Load(dir)
	if err != nil {
		return nil, err
	}
	id, err := identity.Load(dir, cfg.Name)
	if err != nil {
		return nil, err
	}
	tok, err := loadOrCreateToken(filepath.Join(dir, "ui-token"))
	if err != nil {
		return nil, err
	}
	d := &Daemon{Dir: dir, Cfg: cfg, ID: id, Token: tok,
		pendingIn: map[string]*PairRequest{}, pendingOut: map[string]*PairRequest{},
		transfers: NewTransfers(), reachable: map[string]bool{},
		restart: make(chan struct{}, 1), shutdown: make(chan struct{}, 1)}
	d.phones = newPhoneHub(d)
	d.Disc = discovery.New(func() discovery.Beacon {
		var b discovery.Beacon
		cfg.View(func(c *config.Config) {
			b = discovery.Beacon{V: 1, Name: c.Name, FP: id.Fingerprint, Port: c.PeerPort}
		})
		return b
	})
	return d, nil
}

func loadOrCreateToken(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err == nil && len(strings.TrimSpace(string(b))) >= 32 {
		return strings.TrimSpace(string(b)), nil
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(raw)
	return tok, os.WriteFile(path, []byte(tok), 0o600)
}

// Run starts everything and blocks until ctx is cancelled.
func (d *Daemon) Run(ctx context.Context) error {
	if err := os.MkdirAll(d.Cfg.IncomingDir, 0o755); err != nil {
		return err
	}
	d.Disc.Start()
	defer d.Disc.Stop()

	peerLn, err := net.Listen("tcp", fmt.Sprintf(":%d", d.Cfg.PeerPort))
	if err != nil {
		return fmt.Errorf("peer port %d: %w", d.Cfg.PeerPort, err)
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{d.ID.Cert},
		MinVersion:   tls.VersionTLS13,
		ClientAuth:   tls.RequestClientCert, // identity is checked per request
		// Pinning replaces chain validation: handlers compare fingerprints.
		VerifyPeerCertificate: func([][]byte, [][]*x509.Certificate) error { return nil },
		NextProtos:            []string{"http/1.1"}, // HTTP/1.1 streams large bodies faster than h2 flow control
	}
	d.peerSrv = &http.Server{Handler: d.peerMux(), TLSConfig: tlsCfg,
		ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := d.peerSrv.ServeTLS(bufListener{peerLn}, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("peer server: %v", err)
		}
	}()

	localLn, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", d.Cfg.UIPort))
	if err != nil {
		return fmt.Errorf("ui port %d: %w", d.Cfg.UIPort, err)
	}
	d.localSrv = &http.Server{Handler: d.localMux(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := d.localSrv.Serve(localLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("local server: %v", err)
		}
	}()

	log.Printf("Wharfdrop %s  device=%q  id=%s", Version, d.Cfg.Name, identity.Short(d.ID.Fingerprint))
	log.Printf("peer API on :%d (mTLS)   web UI: %s", d.Cfg.PeerPort, d.UIURL())
	log.Printf("incoming files go to %s", d.Cfg.IncomingDir)
	go d.expirePairings(ctx)
	go d.probePeers(ctx)
	go d.updateLoop(ctx)
	go d.expireInbound(ctx)
	go d.phones.idleLoop(ctx)

	go d.uiWatch(ctx)

	var result error
	var cname string
	d.Cfg.View(func(c *config.Config) { cname = c.Name })
	select {
	case <-ctx.Done():
		d.phones.StopWith("stopped", "Wharfdrop on "+cname+" was stopped.")
	case <-d.shutdown:
		log.Printf("stopping (%s)", d.stopReason())
		d.phones.StopWith("stopped", "Wharfdrop on "+cname+" was stopped.")
	case <-d.restart:
		log.Printf("restarting into the updated version")
		d.phones.StopWith("updating", "Wharfdrop on "+cname+" is updating. Open the phone link again in a moment.")
		result = ErrRestart
	}
	if result == nil {
		d.sayGoodbye() // paired computers mark us offline right away
	}
	shut, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	d.peerSrv.Shutdown(shut)
	d.localSrv.Shutdown(shut)
	return result
}

func (d *Daemon) UIURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/?t=%s", d.Cfg.UIPort, d.Token)
}

func (d *Daemon) expirePairings(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.mu.Lock()
			for id, p := range d.pendingIn {
				if time.Since(p.Created) > 5*time.Minute {
					delete(d.pendingIn, id)
				}
			}
			for id, p := range d.pendingOut {
				if time.Since(p.Created) > 5*time.Minute {
					if p.cancel != nil {
						p.cancel()
					}
					delete(d.pendingOut, id)
				}
			}
			d.mu.Unlock()
		}
	}
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// probePeers checks paired devices at their last known address, so they show
// as online even on networks where multicast discovery is filtered.
func (d *Daemon) probePeers(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		var peers []*config.Peer
		d.Cfg.View(func(c *config.Config) {
			for _, p := range c.Peers {
				if p.LastAddr != "" {
					cp := *p
					peers = append(peers, &cp)
				}
			}
		})
		for _, p := range peers {
			if _, ok := d.Disc.Lookup(p.Fingerprint); ok {
				continue
			}
			pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			req, _ := http.NewRequestWithContext(pctx, "GET", d.peerURL(p.LastAddr, "/info", nil), nil)
			resp, err := d.client(p.Fingerprint, nil).Do(req)
			ok := err == nil
			if ok {
				resp.Body.Close()
			}
			cancel()
			d.mu.Lock()
			d.reachable[p.Fingerprint] = ok
			d.mu.Unlock()
		}
	}
}
