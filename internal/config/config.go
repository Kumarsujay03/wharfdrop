// Package config persists device settings, trusted peers and shares.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Peer struct {
	Fingerprint string `json:"fingerprint"`
	Name        string `json:"name"`
	LastAddr    string `json:"last_addr,omitempty"` // host:port last seen
}

// Phone is a phone remembered through the phone link.
type Phone struct {
	ID    string    `json:"id"`
	Name  string    `json:"name"`
	Key   string    `json:"key"` // base64url session key, shared only via the QR code
	Added time.Time `json:"added"`
}

type Share struct {
	Name     string   `json:"name"`
	Path     string   `json:"path"`
	Peers    []string `json:"peers"` // fingerprints, or ["*"] for every paired peer
	ReadOnly bool     `json:"read_only"`
}

type Config struct {
	mu          sync.Mutex `json:"-"`
	path        string
	Name        string           `json:"name"`
	PeerPort    int              `json:"peer_port"`
	UIPort      int              `json:"ui_port"`
	IncomingDir string           `json:"incoming_dir"`
	AutoUpdate  string           `json:"auto_update"` // auto | notify | off
	PhonePort   int              `json:"phone_port,omitempty"`
	Phones      []*Phone         `json:"phones,omitempty"`
	Peers       map[string]*Peer `json:"peers"`
	Shares      []*Share         `json:"shares"`
}

func Load(dir string) (*Config, error) {
	c := &Config{path: filepath.Join(dir, "config.json"), Peers: map[string]*Peer{}}
	b, err := os.ReadFile(c.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		if err := json.Unmarshal(b, c); err != nil {
			return nil, err
		}
	}
	if c.Peers == nil {
		c.Peers = map[string]*Peer{}
	}
	if c.Name == "" {
		h, _ := os.Hostname()
		if h == "" {
			h = "device"
		}
		c.Name = h
	}
	if c.PeerPort == 0 {
		c.PeerPort = 48080
	}
	if c.UIPort == 0 {
		c.UIPort = 48081
	}
	if c.AutoUpdate == "" {
		c.AutoUpdate = "auto"
	}
	if c.IncomingDir == "" {
		home, _ := os.UserHomeDir()
		c.IncomingDir = filepath.Join(home, "Wharfdrop")
	}
	return c, c.Save()
}

func (c *Config) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.saveLocked()
}

func (c *Config) saveLocked() error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// Update runs fn under the lock and persists the result.
func (c *Config) Update(fn func(c *Config)) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	fn(c)
	return c.saveLocked()
}

// View runs fn under the lock without saving.
func (c *Config) View(fn func(c *Config)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fn(c)
}

func (c *Config) Trusted(fp string) (*Peer, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.Peers[fp]
	return p, ok
}

// SharesFor returns the shares a given peer may access.
func (c *Config) SharesFor(fp string) []*Share {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*Share
	for _, s := range c.Shares {
		for _, p := range s.Peers {
			if p == "*" || p == fp {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

func (c *Config) ShareFor(fp, name string) (*Share, bool) {
	for _, s := range c.SharesFor(fp) {
		if s.Name == name {
			return s, true
		}
	}
	return nil, false
}
